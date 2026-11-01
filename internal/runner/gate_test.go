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

func writeHoldout(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGateWorkspaceAtLastGreen(t *testing.T) {
	repo := newSourceRepo(t)
	sha := mustGit(t, repo, "rev-parse", "HEAD")
	mustGit(t, repo, "update-ref", "refs/nightshift/review/demo", sha)
	// A later commit on main must not be what the gate sees.
	if err := os.WriteFile(filepath.Join(repo, "later.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "later")

	ws := filepath.Join(t.TempDir(), "gate-ws")
	dir, err := buildGateWorkspace(ws, repo, "refs/nightshift/review/demo")
	if err != nil {
		t.Fatal(err)
	}
	if dir != filepath.Join(ws, "repo") {
		t.Fatalf("dir = %s", dir)
	}
	if got := mustGit(t, dir, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("HEAD = %s, want %s", got, sha)
	}
	if got := mustGit(t, dir, "remote"); got != "" {
		t.Fatalf("remotes = %q, want none", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "later.go")); err == nil {
		t.Fatal("later.go present")
	}
	// Rebuilding replaces the old workspace.
	if _, err := buildGateWorkspace(ws, repo, "refs/nightshift/review/demo"); err != nil {
		t.Fatal(err)
	}
}

func TestHoldoutOverlay(t *testing.T) {
	hold, repo := t.TempDir(), t.TempDir()
	writeHoldout(t, hold, "a/b/x_test.go", "package b\n")
	writeHoldout(t, hold, "top_test.go", "package main\n")
	n, err := overlayHoldouts(hold, repo)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	for rel, want := range map[string]string{"a/b/x_test.go": "package b\n", "top_test.go": "package main\n"} {
		b, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil || string(b) != want {
			t.Fatalf("%s: %q %v", rel, b, err)
		}
	}
	if n, err := overlayHoldouts(filepath.Join(hold, "missing"), repo); n != 0 || err != nil {
		t.Fatalf("missing: %d %v", n, err)
	}
}

func TestHoldoutNotRegular(t *testing.T) {
	hold, repo := t.TempDir(), t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(hold, "link_test.go")); err != nil {
		t.Fatal(err)
	}
	_, err := overlayHoldouts(hold, repo)
	if err == nil || err.Error() != "holdout link_test.go is not a regular file" {
		t.Fatalf("err = %v", err)
	}
}

func TestHoldoutCrossesSymlink(t *testing.T) {
	hold, repo, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Symlink(elsewhere, filepath.Join(repo, "pkg")); err != nil {
		t.Fatal(err)
	}
	writeHoldout(t, hold, "pkg/x_test.go", "x")
	_, err := overlayHoldouts(hold, repo)
	if err == nil || err.Error() != "holdout path pkg/x_test.go crosses a symlink" {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(elsewhere, "x_test.go")); err == nil {
		t.Fatal("wrote through symlink")
	}
}

func TestHoldoutAlreadyExists(t *testing.T) {
	hold, repo := t.TempDir(), t.TempDir()
	writeHoldout(t, repo, "main.go", "orig")
	writeHoldout(t, hold, "main.go", "new")
	_, err := overlayHoldouts(hold, repo)
	if err == nil || err.Error() != "holdout file main.go already exists in the repo" {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "main.go")); string(b) != "orig" {
		t.Fatalf("overwritten: %q", b)
	}
}
