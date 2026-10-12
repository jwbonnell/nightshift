package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/runner"
	"github.com/jwbonnell/nightshift/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.HermeticGit()
	// Safety net: no test may ever exec a real claude, even if the suite
	// happens to run from a real interactive terminal. Tests that want
	// TTY/exec behavior override these per-test and restore via
	// t.Cleanup.
	isTerminal = func() bool { return false }
	execFn = func(argv0 string, argv, envv []string) error {
		panic(fmt.Sprintf("execFn must be overridden per-test; got argv0=%s argv=%v", argv0, argv))
	}
	os.Exit(m.Run())
}

// TestRunHelpMatchesD4 mechanically verifies the Acceptance criterion
// "nightshift run -h match D4": extracts the reference's exact §3.1.3
// stderr byte output from docs/reference/pensieve-spec-run.md, applies
// the documented D1 renames, and diffs it against the real binary's
// `run -h` output (a subprocess, since -h exits via flag.ExitOnError's
// own os.Exit(0)).
func TestRunHelpMatchesD4(t *testing.T) {
	refData, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "pensieve-spec-run.md"))
	if err != nil {
		t.Fatalf("read reference doc: %v", err)
	}
	ref := string(refData)

	label := "exact stderr byte output"
	i := strings.Index(ref, label)
	if i < 0 {
		t.Fatalf("label %q not found in reference doc", label)
	}
	rest := ref[i:]
	fenceMarker := "```\n"
	fenceStart := strings.Index(rest, fenceMarker)
	if fenceStart < 0 {
		t.Fatalf("no fenced block after %q", label)
	}
	rest = rest[fenceStart+len(fenceMarker):]
	end := strings.Index(rest, "```")
	if end < 0 {
		t.Fatalf("unterminated fenced block after %q", label)
	}
	want := rest[:end]

	want = strings.ReplaceAll(want, `\t`, "\t")
	want = strings.ReplaceAll(want, "pensieve-spec-run", "nightshift run")
	want = strings.ReplaceAll(want, "PENSIEVE_SPEC_BASE", "NIGHTSHIFT_BASE")
	want = strings.ReplaceAll(want, "PENSIEVE_SPEC_MAX_CONCURRENT", "NIGHTSHIFT_MAX_CONCURRENT")
	want = strings.ReplaceAll(want, "_specs/ready/", "specs/ready/")
	// nightshift-deps adds --ignore-deps and --jobs, flags the reference has
	// no concept of (dependency satisfaction and parallel nights didn't
	// exist yet); splice them into the baseline at their alphabetical
	// PrintDefaults position.
	want = strings.ReplaceAll(want, "[--max-concurrent N] [--dry-run]\n       nightshift run --next [--until HH:MM] [--max-concurrent N] [--dry-run]",
		"[--max-concurrent N] [--ignore-deps] [--dry-run]\n       nightshift run --next [--until HH:MM] [--max-concurrent N] [--jobs N] [--dry-run]")
	want = strings.ReplaceAll(want, "  -fresh\n    \tdiscard prior state for this spec and start over\n",
		"  -fresh\n    \tdiscard prior state for this spec and start over\n  -ignore-deps\n    \tskip dependency satisfaction and the integrated base\n")
	want = strings.ReplaceAll(want, "  -max-concurrent string\n    \tmax simultaneous runs (default: NIGHTSHIFT_MAX_CONCURRENT/2)\n",
		"  -jobs int\n    \twith --next, run up to N eligible specs at once (default: NIGHTSHIFT_JOBS/1, capped by max-concurrent) (default -1)\n  -max-concurrent string\n    \tmax simultaneous runs (default: NIGHTSHIFT_MAX_CONCURRENT/2)\n")

	bin := buildNightshiftBinary(t)
	cmd := exec.Command(bin, "run", "-h")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run() // -h exits 0; the exit status itself isn't under test here

	if stderr.String() != want {
		t.Fatalf("run -h output does not match D4\ngot:  %q\nwant: %q", stderr.String(), want)
	}
}

func dirExistsForTest(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

func newTestSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init", "-q", "-b", "main")
	runGit("config", "user.email", "a@b.c")
	runGit("config", "user.name", "a")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "initial")
	return dir
}

func buildNightshiftBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nightshift")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v: %s", err, out)
	}
	return bin
}

func TestCLIUsageAndExitCodes(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout bool
		wantStderr bool
	}{
		{"no args", nil, 2, false, true},
		{"help flag", []string{"-h"}, 0, true, false},
		{"help word", []string{"help"}, 0, true, false},
		{"long help flag", []string{"--help"}, 0, true, false},
		{"unknown command", []string{"bogus"}, 2, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := dispatch(c.args, &stdout, &stderr, strings.NewReader(""))
			if got != c.wantExit {
				t.Fatalf("exit = %d, want %d", got, c.wantExit)
			}
			if c.wantStdout && stdout.String() != usage {
				t.Fatalf("stdout = %q, want usage text", stdout.String())
			}
			if c.wantStderr && stderr.String() != usage {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}

	t.Run("version prints nightshift dev", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"version"}, &stdout, &stderr, strings.NewReader(""))
		if got != 0 {
			t.Fatalf("exit = %d, want 0", got)
		}
		want := "nightshift dev\n"
		if stdout.String() != want {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q, want empty", stderr.String())
		}
	})

	t.Run("run with two positionals exits 1", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)
		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "a", "b"}, &stdout, &stderr, strings.NewReader(""))
		if got != 1 {
			t.Fatalf("exit = %d, want 1; stderr=%s", got, stderr.String())
		}
	})

	t.Run("run --bogus exits 2", func(t *testing.T) {
		// flag.ExitOnError calls os.Exit(2) directly on a bad flag, so
		// this has to run as a real subprocess rather than calling
		// dispatch in-process (which would kill the test binary itself).
		bin := buildNightshiftBinary(t)
		cmd := exec.Command(bin, "run", "--bogus")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("expected an *exec.ExitError, got %v (stderr=%s)", err, stderr.String())
		}
		if exitErr.ExitCode() != 2 {
			t.Fatalf("exit code = %d, want 2 (stderr=%s)", exitErr.ExitCode(), stderr.String())
		}
	})
}

// TestCLIRunPassesVersion verifies cmdRun stamps Options.Version from the
// package-level version var (set via -ldflags in a real build) rather than
// leaving the runner to default it itself.
func TestCLIRunPassesVersion(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	oldRunFn, oldVersion := runFn, version
	t.Cleanup(func() {
		runFn = oldRunFn
		version = oldVersion
	})
	version = "v1.2.3"
	var captured runner.Options
	runFn = func(opts runner.Options) (runner.Status, error) {
		captured = opts
		return runner.StatusComplete, nil
	}

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"run", "demo"}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0; stderr=%s", got, stderr.String())
	}
	if captured.Version != "v1.2.3" {
		t.Fatalf("Options.Version = %q, want v1.2.3", captured.Version)
	}
}

// TestIgnoreDeps covers the CLI wiring for --ignore-deps: the flag reaches
// runner.Options unchanged, and combining it with --next is a usage error
// (exit 2), caught before runFn is ever called.
func TestIgnoreDeps(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	t.Run("flag reaches Options", func(t *testing.T) {
		oldRunFn := runFn
		t.Cleanup(func() { runFn = oldRunFn })
		var captured runner.Options
		runFn = func(opts runner.Options) (runner.Status, error) {
			captured = opts
			return runner.StatusComplete, nil
		}

		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "demo", "--ignore-deps"}, &stdout, &stderr, strings.NewReader(""))
		if got != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%s", got, stderr.String())
		}
		if !captured.IgnoreDeps {
			t.Fatalf("Options.IgnoreDeps = false, want true")
		}
	})

	t.Run("--ignore-deps with --next is a usage error", func(t *testing.T) {
		oldRunFn := runFn
		t.Cleanup(func() { runFn = oldRunFn })
		runFn = func(opts runner.Options) (runner.Status, error) {
			t.Fatal("runFn should not be called")
			return runner.StatusError, nil
		}

		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "--next", "--ignore-deps"}, &stdout, &stderr, strings.NewReader(""))
		if got != 2 {
			t.Fatalf("exit = %d, want 2; stderr=%s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--ignore-deps can't be used with --next") {
			t.Fatalf("stderr = %q, missing message", stderr.String())
		}
	})
}

// TestJobsFlag covers the CLI wiring for --jobs: the flag reaches
// runner.Options unchanged, using it without --next is a usage error (exit
// 2), and a value below 1 is a usage error (exit 2) — all caught before
// runFn is ever called.
func TestJobsFlag(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	t.Run("flag reaches Options", func(t *testing.T) {
		oldRunFn := runFn
		t.Cleanup(func() { runFn = oldRunFn })
		var captured runner.Options
		runFn = func(opts runner.Options) (runner.Status, error) {
			captured = opts
			return runner.StatusComplete, nil
		}

		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "--next", "--jobs", "3"}, &stdout, &stderr, strings.NewReader(""))
		if got != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%s", got, stderr.String())
		}
		if captured.Jobs != 3 {
			t.Fatalf("Options.Jobs = %d, want 3", captured.Jobs)
		}
	})

	t.Run("not passed leaves Options.Jobs unset", func(t *testing.T) {
		oldRunFn := runFn
		t.Cleanup(func() { runFn = oldRunFn })
		var captured runner.Options
		runFn = func(opts runner.Options) (runner.Status, error) {
			captured = opts
			return runner.StatusComplete, nil
		}

		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "--next"}, &stdout, &stderr, strings.NewReader(""))
		if got != 0 {
			t.Fatalf("exit = %d, want 0; stderr=%s", got, stderr.String())
		}
		if captured.Jobs != 0 {
			t.Fatalf("Options.Jobs = %d, want 0 (unset)", captured.Jobs)
		}
	})

	t.Run("--jobs without --next is a usage error", func(t *testing.T) {
		oldRunFn := runFn
		t.Cleanup(func() { runFn = oldRunFn })
		runFn = func(opts runner.Options) (runner.Status, error) {
			t.Fatal("runFn should not be called")
			return runner.StatusError, nil
		}

		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "demo", "--jobs", "2"}, &stdout, &stderr, strings.NewReader(""))
		if got != 2 {
			t.Fatalf("exit = %d, want 2; stderr=%s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--jobs can only be used with --next") {
			t.Fatalf("stderr = %q, missing message", stderr.String())
		}
	})

	t.Run("--jobs 0 is a usage error", func(t *testing.T) {
		oldRunFn := runFn
		t.Cleanup(func() { runFn = oldRunFn })
		runFn = func(opts runner.Options) (runner.Status, error) {
			t.Fatal("runFn should not be called")
			return runner.StatusError, nil
		}

		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"run", "--next", "--jobs", "0"}, &stdout, &stderr, strings.NewReader(""))
		if got != 2 {
			t.Fatalf("exit = %d, want 2; stderr=%s", got, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--jobs 0: want 1 or more") {
			t.Fatalf("stderr = %q, missing message", stderr.String())
		}
	})
}

func TestSmokeInitNewReadyDryRunStatus(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	srcRepo := newTestSourceRepo(t)

	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		got := dispatch(args, &stdout, &stderr, strings.NewReader(""))
		return got, stdout.String(), stderr.String()
	}

	if exit, _, stderr := run("init"); exit != 0 {
		t.Fatalf("init: exit=%d stderr=%s", exit, stderr)
	}
	if exit, _, stderr := run("new", "demo", "--repo", srcRepo, "--verify", "true"); exit != 0 {
		t.Fatalf("new: exit=%d stderr=%s", exit, stderr)
	}
	// --no-lint: this test's spec is the stock template, which lint
	// (by design) rejects for its placeholder sections and tasks.
	if exit, _, stderr := run("ready", "demo", "--no-lint"); exit != 0 {
		t.Fatalf("ready: exit=%d stderr=%s", exit, stderr)
	}

	exit, stdout, stderr := run("run", "demo", "--dry-run")
	if exit != 0 {
		t.Fatalf("run --dry-run: exit=%d stderr=%s", exit, stderr)
	}
	if !strings.Contains(stdout, "home:        "+homeDir) {
		t.Fatalf("dry-run output missing the home: line:\n%s", stdout)
	}
	if dirExistsForTest(filepath.Join(homeDir, "runs", "demo")) {
		t.Fatalf("dry-run created a state dir")
	}

	exit, stdout, stderr = run("status")
	if exit != 0 {
		t.Fatalf("status: exit=%d stderr=%s", exit, stderr)
	}
	if !strings.Contains(stdout, "ready, not started: demo") {
		t.Fatalf("status output missing the ready-not-started line:\n%s", stdout)
	}
}

// TestStatusIgnoresReviewsDir asserts the second half of SPEC.md's
// TestPRNoteTypeDistinct: `nightshift pr`'s notes under reviews/ are a
// different directory from `nightshift review`'s own result/review notes
// under results/, so a decoy file there — even one named to match the
// same slug and the same result/review filename pattern status.go's
// latestNote looks for — must never change what `nightshift status`
// reports for that spec.
func TestStatusIgnoresReviewsDir(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	srcRepo := newTestSourceRepo(t)

	run := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		got := dispatch(args, &stdout, &stderr, strings.NewReader(""))
		return got, stdout.String(), stderr.String()
	}

	if exit, _, stderr := run("init"); exit != 0 {
		t.Fatalf("init: exit=%d stderr=%s", exit, stderr)
	}
	if exit, _, stderr := run("new", "demo", "--repo", srcRepo, "--verify", "true"); exit != 0 {
		t.Fatalf("new: exit=%d stderr=%s", exit, stderr)
	}
	if exit, _, stderr := run("ready", "demo", "--no-lint"); exit != 0 {
		t.Fatalf("ready: exit=%d stderr=%s", exit, stderr)
	}

	for _, name := range []string{"demo-2024-01-01.md", "demo-2024-01-01-review.md"} {
		path := filepath.Join(home.ReviewsDir(homeDir), name)
		if err := os.WriteFile(path, []byte("---\ntype: nightshift-pr-review\n---\ndecoy\n"), 0o644); err != nil {
			t.Fatalf("write decoy %s: %v", name, err)
		}
	}

	exit, stdout, stderr := run("status")
	if exit != 0 {
		t.Fatalf("status: exit=%d stderr=%s", exit, stderr)
	}
	if !strings.Contains(stdout, "ready, not started: demo") {
		t.Fatalf("status output changed by decoy reviews/ notes:\n%s", stdout)
	}
	if strings.Contains(stdout, "done") || strings.Contains(stdout, "2024-01-01") {
		t.Fatalf("status picked up a reviews/ decoy note:\n%s", stdout)
	}
}

func TestUntilInvalidIsUsageError(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	srcRepo := newTestSourceRepo(t)

	run := func(args ...string) (int, string) {
		var stdout, stderr bytes.Buffer
		got := dispatch(args, &stdout, &stderr, strings.NewReader(""))
		return got, stderr.String()
	}

	if exit, stderr := run("init"); exit != 0 {
		t.Fatalf("init: exit=%d stderr=%s", exit, stderr)
	}
	if exit, stderr := run("new", "demo", "--repo", srcRepo, "--verify", "true"); exit != 0 {
		t.Fatalf("new: exit=%d stderr=%s", exit, stderr)
	}
	// --no-lint: this test's spec is the stock template, which lint
	// (by design) rejects for its placeholder sections and tasks.
	if exit, stderr := run("ready", "demo", "--no-lint"); exit != 0 {
		t.Fatalf("ready: exit=%d stderr=%s", exit, stderr)
	}

	for _, bad := range []string{"25:99", "soon"} {
		exit, stderr := run("run", "demo", "--until", bad)
		if exit != 1 {
			t.Fatalf("--until %q: exit = %d, want 1 (stderr=%s)", bad, exit, stderr)
		}
		want := fmt.Sprintf("nightshift: --until %q: want HH:MM (24h, local time)\n", bad)
		if stderr != want {
			t.Fatalf("--until %q: stderr = %q, want %q", bad, stderr, want)
		}
		if dirExistsForTest(filepath.Join(homeDir, "runs", "demo")) {
			t.Fatalf("--until %q: a state dir was created", bad)
		}
	}
}

// fullSpecBody is a clean spec body with all six sections, 2 tasks and 1
// acceptance item — enough to pass every static lint check on its own.
const fullSpecBody = `
# demo

## Goal

Goal text.

## Context

Context text.

## Constraints

- a constraint

## Tasks

- [ ] task one
- [ ] task two

## Acceptance criteria

- [ ] criterion one

## Out of scope

- nothing
`

// writeFullDraftSpec writes a draft spec whose body is fullSpecBody (or,
// with brokenSections true, a body missing "## Out of scope" so check 2
// FAILs), with repo/verify/max_iter frontmatter that otherwise passes
// checks 1, 6 and 7 against repo.
func writeFullDraftSpec(t *testing.T, homeDir, slug, repo string, brokenSections bool) string {
	t.Helper()
	dir := home.DraftDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := fullSpecBody
	if brokenSections {
		body = strings.Replace(body, "## Out of scope\n\n- nothing\n", "", 1)
	}
	content := "---\nrepo: " + repo + "\nverify: true\nmax_iter: 5\n---\n" + body
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

// stubVerifier is a BaseVerifier test double so tests never need a real
// sbx binary on PATH.
type stubVerifier struct{}

func (stubVerifier) Verify(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
	return "", nil
}

func TestLintExitCodes(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	repo := newTestSourceRepo(t)
	withStubVerifier(t)

	t.Run("clean spec exits 0", func(t *testing.T) {
		writeFullDraftSpec(t, homeDir, "clean", repo, false)
		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"lint", "clean"}, &stdout, &stderr, strings.NewReader(""))
		if got != 0 {
			t.Fatalf("exit = %d, want 0; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
		}
	})

	t.Run("failing spec exits 1", func(t *testing.T) {
		writeFullDraftSpec(t, homeDir, "broken", repo, true)
		var stdout, stderr bytes.Buffer
		got := dispatch([]string{"lint", "broken"}, &stdout, &stderr, strings.NewReader(""))
		if got != 1 {
			t.Fatalf("exit = %d, want 1; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), "FAIL") {
			t.Fatalf("stdout missing a FAIL line:\n%s", stdout.String())
		}
	})

	t.Run("usage errors exit 2", func(t *testing.T) {
		for _, args := range [][]string{{"lint"}, {"lint", "a", "b"}} {
			var stdout, stderr bytes.Buffer
			got := dispatch(args, &stdout, &stderr, strings.NewReader(""))
			if got != 2 {
				t.Fatalf("args=%v: exit = %d, want 2; stderr=%s", args, got, stderr.String())
			}
		}
	})
}

func TestReadyRefusesFailingLint(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	repo := newTestSourceRepo(t)
	withStubVerifier(t)
	path := writeFullDraftSpec(t, homeDir, "demo", repo, true)

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"ready", "demo"}, &stdout, &stderr, strings.NewReader(""))
	if got != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "FAIL") {
		t.Fatalf("lint output not printed to stdout:\n%s", stdout.String())
	}
	wantRefusal := "nightshift: demo failed lint — not moved to specs/ready/\n"
	if stderr.String() != wantRefusal {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantRefusal)
	}
	if !dirExistsForTest(filepath.Dir(path)) || !fileExistsForTest(path) {
		t.Fatalf("draft %s was moved or removed", path)
	}
	if fileExistsForTest(filepath.Join(home.ReadyDir(homeDir), "demo.md")) {
		t.Fatalf("spec was moved to specs/ready/ despite failing lint")
	}
}

// TestReadyRefusesCycle asserts that a dependency cycle between two
// drafts fails lint's depends check and keeps "ready" from moving either
// one out of specs/draft/.
func TestReadyRefusesCycle(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	repo := newTestSourceRepo(t)
	withStubVerifier(t)

	dir := home.DraftDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeSpec := func(slug, dependsOn string) {
		content := "---\nrepo: " + repo + "\nverify: true\nmax_iter: 5\ndepends_on: " + dependsOn + "\n---\n" + fullSpecBody
		if err := os.WriteFile(filepath.Join(dir, slug+".md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write spec: %v", err)
		}
	}
	writeSpec("a", "b")
	writeSpec("b", "a")

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"ready", "a"}, &stdout, &stderr, strings.NewReader(""))
	if got != 1 {
		t.Fatalf("exit = %d, want 1; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "FAIL  depends: dependency cycle: a -> b -> a") {
		t.Fatalf("stdout missing cycle FAIL line:\n%s", stdout.String())
	}
	if fileExistsForTest(filepath.Join(home.ReadyDir(homeDir), "a.md")) {
		t.Fatalf("spec was moved to specs/ready/ despite a dependency cycle")
	}
	if !fileExistsForTest(filepath.Join(home.DraftDir(homeDir), "a.md")) {
		t.Fatalf("draft a.md was removed")
	}
}

func TestReadyNoLintKeepsOldBehavior(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	repo := newTestSourceRepo(t)
	// The stock template fails lint (placeholders, empty tasks), but
	// --no-lint should restore today's repo/verify-only check.
	if err := home.Init(homeDir, "sbx", &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := home.New(homeDir, "demo", repo, "true", time.Now(), &bytes.Buffer{}); err != nil {
		t.Fatalf("new: %v", err)
	}

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"ready", "demo", "--no-lint"}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "FAIL") || strings.Contains(stdout.String(), "ok    ") {
		t.Fatalf("--no-lint printed lint output:\n%s", stdout.String())
	}
	if !fileExistsForTest(filepath.Join(home.ReadyDir(homeDir), "demo.md")) {
		t.Fatalf("spec was not moved to specs/ready/")
	}
}

func TestReadyNoVerifySkipsVerify(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	repo := newTestSourceRepo(t)
	writeFullDraftSpec(t, homeDir, "demo", repo, false)

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"ready", "demo", "--no-verify"}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "skip  verify-on-base (--no-verify)") {
		t.Fatalf("stdout missing the verify-on-base skip line:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "ready: demo") {
		t.Fatalf("stdout missing the ready: line:\n%s", stdout.String())
	}
	if !fileExistsForTest(filepath.Join(home.ReadyDir(homeDir), "demo.md")) {
		t.Fatalf("spec was not moved to specs/ready/")
	}
}

func TestUsageListsLint(t *testing.T) {
	if !strings.Contains(usage, "nightshift lint <spec> [--no-verify]") {
		t.Fatalf("usage missing the lint line:\n%s", usage)
	}
	if !strings.Contains(usage, "nightshift ready <slug> [--no-verify] [--no-lint]") {
		t.Fatalf("usage missing ready's new flags:\n%s", usage)
	}
	if !strings.Contains(usage, "lint a draft, then move it to specs/ready/") {
		t.Fatalf("usage missing ready's updated description:\n%s", usage)
	}
}

func fileExistsForTest(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// TestLintCLIUsesInterruptContext asserts cmdLint and cmdReady each
// install an interrupt-aware context via the newInterruptContext seam,
// rather than a bare context.Background() with no Ctrl+C handling.
func TestLintCLIUsesInterruptContext(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	repo := newTestSourceRepo(t)
	withStubVerifier(t)

	calls := 0
	old := newInterruptContext
	newInterruptContext = func() (context.Context, func()) {
		calls++
		return context.WithCancel(context.Background())
	}
	t.Cleanup(func() { newInterruptContext = old })

	writeFullDraftSpec(t, homeDir, "lint-demo", repo, false)
	var stdout, stderr bytes.Buffer
	if got := cmdLint([]string{"lint-demo"}, &stdout, &stderr); got != 0 {
		t.Fatalf("cmdLint exit = %d, want 0; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}
	if calls != 1 {
		t.Fatalf("cmdLint: newInterruptContext called %d times, want 1", calls)
	}

	writeFullDraftSpec(t, homeDir, "ready-demo", repo, false)
	stdout.Reset()
	stderr.Reset()
	if got := cmdReady([]string{"ready-demo"}, &stdout, &stderr); got != 0 {
		t.Fatalf("cmdReady exit = %d, want 0; stdout=%s stderr=%s", got, stdout.String(), stderr.String())
	}
	if calls != 2 {
		t.Fatalf("cmdReady: newInterruptContext called %d times total, want 2", calls)
	}
}

// withStubVerifier points newBaseVerifier at a BaseVerifier that always
// succeeds, for the duration of the test, so lint's check 8 never needs a
// real sbx binary on PATH.
func withStubVerifier(t *testing.T) {
	t.Helper()
	old := newBaseVerifier
	newBaseVerifier = func(bin string) runner.BaseVerifier { return stubVerifier{} }
	t.Cleanup(func() { newBaseVerifier = old })
}
