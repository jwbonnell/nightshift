package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestGateSettings(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	check := func(label string, want int, src string) {
		t.Helper()
		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if sp.GateRounds != want || sp.src["gate_rounds"] != src {
			t.Fatalf("%s: rounds=%d src=%q, want %d %q", label, sp.GateRounds, sp.src["gate_rounds"], want, src)
		}
		if sp.Accept != "" {
			t.Fatalf("%s: accept = %q, want empty", label, sp.Accept)
		}
	}
	check("default", 2, "default")
	if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_GATE_ROUNDS=4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("config", 4, "config")
	t.Setenv("NIGHTSHIFT_GATE_ROUNDS", "5")
	check("env", 5, "env")
	writeReadySpec(t, homeDir, "demo", "gate_rounds: 0\naccept: \"go run ./cmd/check\"\n", repo)
	check2 := func() {
		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatal(err)
		}
		if sp.GateRounds != 0 || sp.src["gate_rounds"] != "frontmatter" || sp.Accept != "go run ./cmd/check" {
			t.Fatalf("frontmatter: rounds=%d src=%q accept=%q", sp.GateRounds, sp.src["gate_rounds"], sp.Accept)
		}
	}
	check2()

	for _, bad := range []string{"6", "-1", "x", "+1"} {
		writeReadySpec(t, homeDir, "demo", "gate_rounds: "+bad+"\n", repo)
		_, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		want := fmt.Sprintf("gate_rounds %q: want 0-5", bad)
		if err == nil || err.Error() != want {
			t.Fatalf("%q: err = %v, want %q", bad, err, want)
		}
	}
}

func TestGateDryRunLine(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "accept: true\n", repo)
	hd := filepath.Join(home.HoldoutDir(homeDir), "demo", "sub")
	if err := os.MkdirAll(hd, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a_test.go", "b_test.go"} {
		if err := os.WriteFile(filepath.Join(hd, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	render := func() string {
		opts := Options{SpecArg: "demo", Home: homeDir, StateRoot: home.RunsDir(homeDir), SbxBin: "sbx", ClaudeBin: "claude"}
		sp, err := resolveSpec(opts)
		if err != nil {
			t.Fatal(err)
		}
		sha, err := resolveBase(sp.Repo, sp.Base)
		if err != nil {
			t.Fatal(err)
		}
		sp.BaseSHA = sha
		var buf strings.Builder
		printPlan(&buf, opts, sp, 2, "default")
		return buf.String()
	}
	if out := render(); !strings.Contains(out, "gate:        accept set · holdout 2 file(s) · rounds 2 (default)\n") {
		t.Fatalf("missing gate line:\n%s", out)
	}
	if err := os.RemoveAll(filepath.Join(home.HoldoutDir(homeDir), "demo")); err != nil {
		t.Fatal(err)
	}
	writeReadySpec(t, homeDir, "demo", "gate_rounds: 0\n", repo)
	if out := render(); !strings.Contains(out, "gate:        accept unset · holdout none · rounds 0 (frontmatter)\n") {
		t.Fatalf("missing gate line:\n%s", out)
	}
}

func TestHoldoutInsideRepoRefused(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	if err := os.MkdirAll(home.HoldoutDir(homeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(repo, "hold")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home.HoldoutDir(homeDir), "demo")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	opts := Options{SpecArg: "demo", Home: homeDir, StateRoot: home.RunsDir(homeDir), SbxBin: "sbx", ClaudeBin: "claude", DryRun: true, Stdout: &strings.Builder{}}
	_, err := Run(opts)
	want := fmt.Sprintf("holdout dir %s must be outside the repo", link)
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}

	// A plain dir outside the repo is fine.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkHoldoutOutsideRepo(homeDir, "demo", repo); err != nil {
		t.Fatalf("outside dir refused: %v", err)
	}
}
