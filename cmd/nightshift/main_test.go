package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
	if exit, _, stderr := run("ready", "demo"); exit != 0 {
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
	if exit, stderr := run("ready", "demo"); exit != 0 {
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
