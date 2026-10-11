package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestDependsOnAndOwnsParse(t *testing.T) {
	t.Run("depends_on list and dedup", func(t *testing.T) {
		got, err := parseDependsOn(` a, b , a,"c" `)
		if err != nil {
			t.Fatalf("parseDependsOn: %v", err)
		}
		want := []string{"a", "b", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("depends_on empty", func(t *testing.T) {
		got, err := parseDependsOn("")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("depends_on bad slug", func(t *testing.T) {
		_, err := parseDependsOn("Bad Slug!")
		if err == nil {
			t.Fatal("want error")
		}
		want := `depends_on entry "Bad Slug!": want a spec slug`
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("owns list and dedup", func(t *testing.T) {
		got, err := parseOwns(` internal/api/, go.mod ,internal/api/`)
		if err != nil {
			t.Fatalf("parseOwns: %v", err)
		}
		want := []string{"internal/api/", "go.mod"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("owns empty", func(t *testing.T) {
		got, err := parseOwns("")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})

	for _, bad := range []string{"..", "../x", "/abs/path", "a/../b", ".", "./"} {
		t.Run("owns bad "+bad, func(t *testing.T) {
			_, err := parseOwns(bad)
			if err == nil {
				t.Fatalf("parseOwns(%q): want error", bad)
			}
			want := `owns entry "` + bad + `": want a clean repo-relative path`
			if err.Error() != want {
				t.Fatalf("err = %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestLintDependsCheck covers the lint "depends" check: an unknown
// dependency, a self-dependency and a 3-cycle all FAIL with the exact
// text; an unmet-but-known dependency warns; no depends_on skips; and an
// unparsable sibling spec doesn't hide the others.
func TestLintDependsCheck(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("unknown dependency FAILs", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "depends_on: ghost\n", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		want := `FAIL  depends: demo depends on ghost, which isn't in specs/draft, specs/ready or specs/done` + "\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
		if res.Fails == 0 {
			t.Fatalf("res = %+v, want a FAIL", res)
		}
	})

	t.Run("self-dependency FAILs as a cycle", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "depends_on: demo\n", repo, fullSpecBody)

		var buf bytes.Buffer
		if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf}); err != nil {
			t.Fatalf("Lint: %v", err)
		}
		want := "FAIL  depends: dependency cycle: demo -> demo\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
	})

	t.Run("3-cycle FAILs with the shortest path through this spec", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "a", "depends_on: b\n", repo, fullSpecBody)
		writeLintSpec(t, home.ReadyDir(homeDir), "b", "depends_on: c\n", repo, fullSpecBody)
		writeLintSpec(t, home.ReadyDir(homeDir), "c", "depends_on: a\n", repo, fullSpecBody)

		var buf bytes.Buffer
		if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "a", NoVerify: true, Stdout: &buf}); err != nil {
			t.Fatalf("Lint: %v", err)
		}
		want := "FAIL  depends: dependency cycle: a -> b -> c -> a\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
	})

	t.Run("unmet known dependency warns", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "depends_on: other\n", repo, fullSpecBody)
		writeLintSpec(t, home.ReadyDir(homeDir), "other", "", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		want := "warn  depends: waits on other (not started)\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
		if res.Warns == 0 {
			t.Fatalf("res = %+v, want a warn", res)
		}
	})

	t.Run("no depends_on skips", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

		var buf bytes.Buffer
		if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf}); err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if !strings.Contains(buf.String(), "skip  depends (no depends_on)\n") {
			t.Fatalf("output missing skip line:\n%s", buf.String())
		}
	})

	t.Run("an unparsable sibling spec doesn't hide the others", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "depends_on: other\n", repo, fullSpecBody)
		writeLintSpec(t, home.ReadyDir(homeDir), "other", "", repo, fullSpecBody)
		// "Bad Slug!" fails parseDependsOn, so bogus contributes no edges —
		// but its own slug must still register, so it doesn't matter here;
		// this just proves loadSpecGraph doesn't choke on it.
		writeLintSpec(t, home.ReadyDir(homeDir), "bogus", "depends_on: \"Bad Slug!\"\n", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		want := "warn  depends: waits on other (not started)\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
		if res.Fails != 0 {
			t.Fatalf("res = %+v, want no FAILs (bogus sibling shouldn't leak in)\n%s", res, buf.String())
		}
	})
}

func TestLintAllowsDependsOwns(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "depends_on: other\nowns: internal/api/, go.mod\n", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Warns != 0 {
		t.Fatalf("res = %+v, want zero warns; output:\n%s", res, buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte("unknown key")) {
		t.Fatalf("output unexpectedly warns about depends_on/owns:\n%s", buf.String())
	}
}

// TestDepSatisfiedByCompleteOrDone covers depState's satisfaction rule:
// a complete run.json or a specs/done/ entry satisfies; blocked, red and
// rate-limited do not.
func TestDepSatisfiedByCompleteOrDone(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	opts := Options{Home: homeDir, StateRoot: stateRoot}

	writeRunJSON := func(t *testing.T, slug, status string) {
		t.Helper()
		dir := filepath.Join(stateRoot, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		data := fmt.Sprintf(`{"slug":%q,"status":%q}`, slug, status)
		if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(data), 0o644); err != nil {
			t.Fatalf("write run.json: %v", err)
		}
	}

	if s := depState(opts, "ghost"); s != "not started" {
		t.Fatalf("no run.json: depState = %q, want %q", s, "not started")
	}

	writeRunJSON(t, "a", "complete")
	if s := depState(opts, "a"); s != "complete" {
		t.Fatalf("complete run.json: depState = %q, want complete", s)
	}

	if err := os.MkdirAll(home.DoneDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir done: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.DoneDir(homeDir), "b.md"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write done spec: %v", err)
	}
	if s := depState(opts, "b"); s != "complete" {
		t.Fatalf("specs/done entry: depState = %q, want complete", s)
	}

	for _, status := range []string{"blocked", "red", "rate-limited"} {
		writeRunJSON(t, status, status)
		if s := depState(opts, status); s != status {
			t.Fatalf("%s: depState = %q, want %q (not satisfied)", status, s, status)
		}
	}
}
