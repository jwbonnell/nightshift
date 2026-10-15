package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestResolveRefusesDraft(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeDraftSpec(t, homeDir, "demo", "", repo)

	// A draft isn't found via the ready-dir candidates, so this exercises
	// the gate via the "arg as given" path, as `nightshift run <path>`
	// pointed straight at the draft file would.
	draftPath := filepath.Join(home.DraftDir(homeDir), "demo.md")
	_, err := resolveSpec(Options{SpecArg: draftPath, Home: homeDir})
	if err == nil {
		t.Fatalf("expected an error for a spec still in draft")
	}
	want := "demo.md is still in specs/draft/ — run 'nightshift ready demo' when you mean it"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
}

func TestRunRefusesOutsideHome(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()

	// A spec that lives in some other ready/ directory entirely, outside
	// this Home.
	elsewhere := filepath.Join(t.TempDir(), "specs", "ready")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outsidePath := filepath.Join(elsewhere, "demo.md")
	content := "---\nrepo: " + repo + "\nverify: true\n---\n"
	if err := os.WriteFile(outsidePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	_, err := resolveSpec(Options{SpecArg: outsidePath, Home: homeDir})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if !strings.HasPrefix(err.Error(), "spec must live under ") {
		t.Fatalf("err = %q, want prefix %q", err.Error(), "spec must live under ")
	}
}

func TestResolveSpecRequiredFields(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("missing repo", func(t *testing.T) {
		homeDir := t.TempDir()
		if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(home.ReadyDir(homeDir), "demo.md")
		if err := os.WriteFile(path, []byte("---\nverify: true\n---\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		want := "spec demo has no 'repo:' frontmatter — add the checkout the agent should clone"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("missing verify", func(t *testing.T) {
		homeDir := t.TempDir()
		if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		path := filepath.Join(home.ReadyDir(homeDir), "demo.md")
		if err := os.WriteFile(path, []byte("---\nrepo: "+repo+"\n---\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		want := "spec demo has no 'verify:' frontmatter — add the command that must pass before any commit"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("repo not a git checkout", func(t *testing.T) {
		homeDir := t.TempDir()
		notRepo := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", notRepo)
		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err == nil || !strings.Contains(err.Error(), "is not a git repository") {
			t.Fatalf("err = %v, want it to mention 'is not a git repository'", err)
		}
	})

	t.Run("defaults when nothing else is set", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)
		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Model != "sonnet" || sp.Planner != "opus" || sp.MaxIter != 10 || sp.IterTimeout.String() != "30m0s" {
			t.Fatalf("defaults = model=%q planner=%q maxIter=%d iterTimeout=%s",
				sp.Model, sp.Planner, sp.MaxIter, sp.IterTimeout)
		}
	})
}

func TestModelPlannerIterTimeoutPrecedence(t *testing.T) {
	repo := newSourceRepo(t)

	for _, f := range []struct {
		field   string
		fmLine  string
		fmWant  string
		envName string
		envVal  string
		cfgLine string
		cfgWant string
		get     func(sp *Spec) string
	}{
		{"model", "model: fm-model\n", "fm-model", "NIGHTSHIFT_MODEL", "env-model", "NIGHTSHIFT_MODEL=cfg-model\n", "cfg-model",
			func(sp *Spec) string { return sp.Model }},
		{"planner_model", "planner_model: fm-planner\n", "fm-planner", "NIGHTSHIFT_PLANNER_MODEL", "env-planner", "NIGHTSHIFT_PLANNER_MODEL=cfg-planner\n", "cfg-planner",
			func(sp *Spec) string { return sp.Planner }},
		{"iter_timeout", "iter_timeout: 5m\n", "5m0s", "NIGHTSHIFT_ITER_TIMEOUT", "6m", "NIGHTSHIFT_ITER_TIMEOUT=7m\n", "7m0s",
			func(sp *Spec) string { return sp.IterTimeout.String() }},
	} {
		t.Run(f.field, func(t *testing.T) {
			homeDir := t.TempDir()
			writeReadySpec(t, homeDir, "demo", "", repo)

			sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
			if err != nil {
				t.Fatalf("resolveSpec (default): %v", err)
			}
			if sp.src[f.field] != "default" {
				t.Fatalf("%s src = %q, want default", f.field, sp.src[f.field])
			}

			if err := os.WriteFile(home.ConfigPath(homeDir), []byte(f.cfgLine), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
			if err != nil {
				t.Fatalf("resolveSpec (config): %v", err)
			}
			if f.get(sp) != f.cfgWant || sp.src[f.field] != "config" {
				t.Fatalf("%s = %q src=%q, want %q/config", f.field, f.get(sp), sp.src[f.field], f.cfgWant)
			}

			t.Setenv(f.envName, f.envVal)
			sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
			if err != nil {
				t.Fatalf("resolveSpec (env): %v", err)
			}
			if f.get(sp) != f.envVal && !(f.field == "iter_timeout" && f.get(sp) == "6m0s") {
				t.Fatalf("%s = %q src=%q, want env value", f.field, f.get(sp), sp.src[f.field])
			}
			if sp.src[f.field] != "env" {
				t.Fatalf("%s src = %q, want env", f.field, sp.src[f.field])
			}

			writeReadySpec(t, homeDir, "demo", f.fmLine, repo)
			sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
			if err != nil {
				t.Fatalf("resolveSpec (frontmatter): %v", err)
			}
			if f.get(sp) != f.fmWant || sp.src[f.field] != "frontmatter" {
				t.Fatalf("%s = %q src=%q, want %q/frontmatter", f.field, f.get(sp), sp.src[f.field], f.fmWant)
			}
		})
	}
}

func TestMaxIterAndBasePrecedence(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("max_iter: flag beats frontmatter beats env beats config beats default", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)

		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.MaxIter != 10 || sp.src["max_iter"] != "default" {
			t.Fatalf("default: maxIter=%d src=%q", sp.MaxIter, sp.src["max_iter"])
		}

		if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_MAX_ITER=4\n"), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.MaxIter != 4 || sp.src["max_iter"] != "config" {
			t.Fatalf("config: maxIter=%d src=%q", sp.MaxIter, sp.src["max_iter"])
		}

		t.Setenv("NIGHTSHIFT_MAX_ITER", "5")
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.MaxIter != 5 || sp.src["max_iter"] != "env" {
			t.Fatalf("env: maxIter=%d src=%q", sp.MaxIter, sp.src["max_iter"])
		}

		writeReadySpec(t, homeDir, "demo", "max_iter: 7\n", repo)
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.MaxIter != 7 || sp.src["max_iter"] != "frontmatter" {
			t.Fatalf("frontmatter: maxIter=%d src=%q", sp.MaxIter, sp.src["max_iter"])
		}

		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir, MaxIter: 3})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.MaxIter != 3 || sp.src["max_iter"] != "flag" {
			t.Fatalf("flag: maxIter=%d src=%q", sp.MaxIter, sp.src["max_iter"])
		}
	})

	t.Run("max_iter bad value errors", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "max_iter: nope\n", repo)
		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		want := `max_iter "nope": not a positive integer`
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("base: flag beats frontmatter beats env beats config beats default", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)

		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Base != "main" || sp.src["base"] != "default" {
			t.Fatalf("default: base=%q src=%q", sp.Base, sp.src["base"])
		}

		if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_BASE=cfg-base\n"), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Base != "cfg-base" || sp.src["base"] != "config" {
			t.Fatalf("config: base=%q src=%q", sp.Base, sp.src["base"])
		}

		t.Setenv("NIGHTSHIFT_BASE", "env-base")
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Base != "env-base" || sp.src["base"] != "env" {
			t.Fatalf("env: base=%q src=%q", sp.Base, sp.src["base"])
		}

		writeReadySpec(t, homeDir, "demo", "base: fm-base\n", repo)
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Base != "fm-base" || sp.src["base"] != "frontmatter" {
			t.Fatalf("frontmatter: base=%q src=%q", sp.Base, sp.src["base"])
		}

		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir, Base: "flag-base"})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Base != "flag-base" || sp.src["base"] != "flag" {
			t.Fatalf("flag: base=%q src=%q", sp.Base, sp.src["base"])
		}
	})
}

func TestReviewFrontmatterPrecedence(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("review: frontmatter beats env beats config beats default", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)

		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Review != false || sp.src["review"] != "default" {
			t.Fatalf("default: review=%v src=%q", sp.Review, sp.src["review"])
		}

		if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_REVIEW=true\n"), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Review != true || sp.src["review"] != "config" {
			t.Fatalf("config: review=%v src=%q", sp.Review, sp.src["review"])
		}

		t.Setenv("NIGHTSHIFT_REVIEW", "TRUE")
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Review != true || sp.src["review"] != "env" {
			t.Fatalf("env: review=%v src=%q", sp.Review, sp.src["review"])
		}

		writeReadySpec(t, homeDir, "demo", "review: false\n", repo)
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.Review != false || sp.src["review"] != "frontmatter" {
			t.Fatalf("frontmatter: review=%v src=%q", sp.Review, sp.src["review"])
		}
	})

	t.Run("review_model: frontmatter beats env beats config beats default", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)

		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.ReviewModel != "opus" || sp.src["review_model"] != "default" {
			t.Fatalf("default: reviewModel=%q src=%q", sp.ReviewModel, sp.src["review_model"])
		}

		if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_REVIEW_MODEL=cfg-model\n"), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.ReviewModel != "cfg-model" || sp.src["review_model"] != "config" {
			t.Fatalf("config: reviewModel=%q src=%q", sp.ReviewModel, sp.src["review_model"])
		}

		t.Setenv("NIGHTSHIFT_REVIEW_MODEL", "env-model")
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.ReviewModel != "env-model" || sp.src["review_model"] != "env" {
			t.Fatalf("env: reviewModel=%q src=%q", sp.ReviewModel, sp.src["review_model"])
		}

		writeReadySpec(t, homeDir, "demo", "review_model: fm-model\n", repo)
		sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		if sp.ReviewModel != "fm-model" || sp.src["review_model"] != "frontmatter" {
			t.Fatalf("frontmatter: reviewModel=%q src=%q", sp.ReviewModel, sp.src["review_model"])
		}
	})
}

func TestReviewInvalidBool(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "review: Maybe\n", repo)

	_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	want := `review "Maybe": want true, false or a list of lenses`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestReviewLensListParse(t *testing.T) {
	homeDir := t.TempDir()

	cases := []struct {
		val     string
		want    []string
		wantErr string
	}{
		{val: "true", want: []string{"general"}},
		{val: "false", want: nil},
		{val: "1", want: []string{"general"}},
		{val: "FALSE", want: nil},
		{val: "requirements, security", want: []string{"requirements", "security"}},
		{val: "general,general", want: []string{"general"}},
		{val: "Foo", wantErr: `review "Foo": want true, false or a list of lenses`},
		{val: "", wantErr: `review "": want true, false or a list of lenses`},
		{val: "bogus", wantErr: fmt.Sprintf(`unknown review lens "bogus" (built-in: general, requirements, quality, security; or add %s)`, filepath.Join(home.LensesDir(homeDir), "bogus.md"))},
	}
	for _, c := range cases {
		got, err := parseReviewSetting(c.val, homeDir)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr {
				t.Errorf("parseReviewSetting(%q) err = %v, want %q", c.val, err, c.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseReviewSetting(%q): %v", c.val, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseReviewSetting(%q) = %v, want %v", c.val, got, c.want)
		}
	}
}

func TestCustomLensOverridesAndResolves(t *testing.T) {
	homeDir := t.TempDir()
	if err := os.MkdirAll(home.LensesDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir lenses: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.LensesDir(homeDir), "api-contract.md"), []byte("custom\n"), 0o644); err != nil {
		t.Fatalf("write custom lens: %v", err)
	}

	got, err := parseReviewSetting("api-contract", homeDir)
	if err != nil {
		t.Fatalf("parseReviewSetting: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"api-contract"}) {
		t.Fatalf("got %v, want [api-contract]", got)
	}
}

func TestReviewRoundsPrecedence(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.ReviewRounds != 0 || sp.src["review_rounds"] != "default" {
		t.Fatalf("default: review_rounds=%d src=%q", sp.ReviewRounds, sp.src["review_rounds"])
	}

	if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_REVIEW_ROUNDS=2\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.ReviewRounds != 2 || sp.src["review_rounds"] != "config" {
		t.Fatalf("config: review_rounds=%d src=%q", sp.ReviewRounds, sp.src["review_rounds"])
	}

	t.Setenv("NIGHTSHIFT_REVIEW_ROUNDS", "3")
	sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.ReviewRounds != 3 || sp.src["review_rounds"] != "env" {
		t.Fatalf("env: review_rounds=%d src=%q", sp.ReviewRounds, sp.src["review_rounds"])
	}

	writeReadySpec(t, homeDir, "demo", "review_rounds: 5\n", repo)
	sp, err = resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.ReviewRounds != 5 || sp.src["review_rounds"] != "frontmatter" {
		t.Fatalf("frontmatter: review_rounds=%d src=%q", sp.ReviewRounds, sp.src["review_rounds"])
	}
}

func TestReviewRoundsInvalid(t *testing.T) {
	repo := newSourceRepo(t)

	for _, bad := range []string{"6", "-1", "x"} {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "review_rounds: "+bad+"\n", repo)

		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		want := fmt.Sprintf(`review_rounds %q: want 0-5`, bad)
		if err == nil || err.Error() != want {
			t.Fatalf("review_rounds=%q: err = %v, want %q", bad, err, want)
		}
	}
}

func TestFrontmatterQuotesStripped(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", `model: "opus"
egress: 'a.com', "b.com"
`, repo)

	sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.Model != "opus" {
		t.Fatalf("Model = %q, want %q", sp.Model, "opus")
	}
	want := []string{"a.com", "b.com"}
	if len(sp.Egress) != len(want) || sp.Egress[0] != want[0] || sp.Egress[1] != want[1] {
		t.Fatalf("Egress = %v, want %v", sp.Egress, want)
	}
}

func TestFrontmatterMismatchedQuotesKept(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "model: \"mismatched'\n", repo)

	sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	if sp.Model != `"mismatched'` {
		t.Fatalf("Model = %q, want %q", sp.Model, `"mismatched'`)
	}
}

func TestImageFrontmatter(t *testing.T) {
	repo := newSourceRepo(t)

	cases := []struct {
		fm   string
		want string
	}{
		{"", ""},
		{"image: last-light-flutter:3.27.1\n", "last-light-flutter:3.27.1"},
		{"image: docker.io/me/img:v1\n", "docker.io/me/img:v1"},
		{`image: "quoted:1"` + "\n", "quoted:1"},
	}
	for _, c := range cases {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", c.fm, repo)

		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("fm=%q: resolveSpec: %v", c.fm, err)
		}
		if sp.Image != c.want {
			t.Fatalf("fm=%q: Image = %q, want %q", c.fm, sp.Image, c.want)
		}
	}
}

func TestImageInvalid(t *testing.T) {
	repo := newSourceRepo(t)

	for _, bad := range []string{"-x", "a b"} {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "image: "+bad+"\n", repo)

		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err == nil || !strings.Contains(err.Error(), "image") {
			t.Fatalf("image=%q: err = %v, want error naming image", bad, err)
		}
	}
}

func TestUppercaseMDSpec(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	upperPath := filepath.Join(home.ReadyDir(homeDir), "DEMO.MD")
	content := "---\nrepo: " + repo + "\nverify: true\n---\n"
	if err := os.WriteFile(upperPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	// Found by a lowercase guess with no extension (case-insensitive name
	// resolution within the ready dir).
	sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec(demo): %v", err)
	}
	if sp.Slug != "DEMO" {
		t.Fatalf("Slug = %q, want %q", sp.Slug, "DEMO")
	}
	if sp.Path != upperPath {
		t.Fatalf("Path = %q, want %q (opened by its real name)", sp.Path, upperPath)
	}

	// Found by its exact real name too.
	sp, err = resolveSpec(Options{SpecArg: "DEMO.MD", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec(DEMO.MD): %v", err)
	}
	if sp.Slug != "DEMO" {
		t.Fatalf("Slug = %q, want %q", sp.Slug, "DEMO")
	}
}
