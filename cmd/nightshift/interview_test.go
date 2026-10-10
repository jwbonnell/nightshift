package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

// fakeClaudeBin writes a tiny shell script that exits 0 and returns its
// absolute path, so tests can point NIGHTSHIFT_HOST_CLAUDE_BIN at it
// without depending on a real `claude` (or lack of one) on the host PATH.
func fakeClaudeBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func TestNewNoInterviewWhenNotTTY(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	srcRepo := newTestSourceRepo(t)

	// isTerminal is forced false by TestMain; this is the default,
	// non-interactive case.
	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"new", "demo", "--repo", srcRepo}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}
	draftPath := filepath.Join(home.DraftDir(homeDir), "demo.md")
	want := draftPath + "\nedit the draft, then: nightshift lint demo\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if !fileExistsForTest(draftPath) {
		t.Fatalf("draft not created at %s", draftPath)
	}
}

func TestNewNoInterviewFlag(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	srcRepo := newTestSourceRepo(t)

	oldIsTerminal := isTerminal
	isTerminal = func() bool { return true }
	t.Cleanup(func() { isTerminal = oldIsTerminal })

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"new", "demo", "--repo", srcRepo, "--no-interview"}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}
	draftPath := filepath.Join(home.DraftDir(homeDir), "demo.md")
	want := draftPath + "\nedit the draft, then: nightshift lint demo\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestNewHintClaudeMissing(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	t.Setenv("NIGHTSHIFT_HOST_CLAUDE_BIN", "nightshift-test-no-such-claude")
	srcRepo := newTestSourceRepo(t)

	oldIsTerminal := isTerminal
	isTerminal = func() bool { return true }
	t.Cleanup(func() { isTerminal = oldIsTerminal })

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"new", "demo", "--repo", srcRepo}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}
	draftPath := filepath.Join(home.DraftDir(homeDir), "demo.md")
	want := draftPath + "\nclaude not found on PATH — edit the draft by hand, then: nightshift lint demo\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestNewHintNoRepo(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	t.Setenv("NIGHTSHIFT_HOST_CLAUDE_BIN", fakeClaudeBin(t))

	oldIsTerminal := isTerminal
	isTerminal = func() bool { return true }
	t.Cleanup(func() { isTerminal = oldIsTerminal })

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"new", "demo"}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}
	draftPath := filepath.Join(home.DraftDir(homeDir), "demo.md")
	want := draftPath + "\npass --repo PATH to be interviewed, or edit the draft by hand\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestNewHintRepoNotGit(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	t.Setenv("NIGHTSHIFT_HOST_CLAUDE_BIN", fakeClaudeBin(t))
	notGit := t.TempDir()

	oldIsTerminal := isTerminal
	isTerminal = func() bool { return true }
	t.Cleanup(func() { isTerminal = oldIsTerminal })

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"new", "demo", "--repo", notGit}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}
	draftPath := filepath.Join(home.DraftDir(homeDir), "demo.md")
	want := draftPath + "\n--repo " + notGit + " is not a git repository — fix it, or edit the draft by hand\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestNewExecsClaudeOnTTY(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	fakeBin := fakeClaudeBin(t)
	t.Setenv("NIGHTSHIFT_HOST_CLAUDE_BIN", fakeBin)
	t.Setenv("NIGHTSHIFT_INTERVIEW_MODEL", "opus")
	srcRepo := newTestSourceRepo(t)

	oldIsTerminal, oldExecFn := isTerminal, execFn
	isTerminal = func() bool { return true }
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var gotArgv []string
	var gotWd string
	execFn = func(argv0 string, argv, envv []string) error {
		gotArgv = argv
		gotWd, _ = os.Getwd()
		return nil
	}
	t.Cleanup(func() {
		isTerminal = oldIsTerminal
		execFn = oldExecFn
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"new", "demo", "--repo", srcRepo}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}

	wantClaude, err := exec.LookPath(fakeBin)
	if err != nil {
		t.Fatalf("lookpath fake claude: %v", err)
	}
	absDraftPath, err := filepath.Abs(filepath.Join(home.DraftDir(homeDir), "demo.md"))
	if err != nil {
		t.Fatalf("abs draft: %v", err)
	}
	absHome, err := filepath.Abs(homeDir)
	if err != nil {
		t.Fatalf("abs home: %v", err)
	}
	absRepo, err := filepath.Abs(srcRepo)
	if err != nil {
		t.Fatalf("abs repo: %v", err)
	}

	settingsPath := filepath.Join(absHome, "interview", "demo", "settings.json")
	promptPath := filepath.Join(absHome, "interview", "demo", "prompt.md")
	wantArgv := []string{
		wantClaude,
		"--model", "opus",
		"--permission-mode", "dontAsk",
		"--tools", "Read,Grep,Glob,Edit,Write",
		"--disallowedTools", "mcp__*",
		"--settings", settingsPath,
		"--append-system-prompt-file", promptPath,
		"--add-dir", absHome,
		"-n", "spec-demo",
		fmt.Sprintf("Interview me to write the nightshift spec %q for this repository. The draft file is %s. Start by asking what I want done — don't read code yet.", "demo", absDraftPath),
	}
	if !reflect.DeepEqual(gotArgv, wantArgv) {
		t.Fatalf("argv =\n%v\nwant\n%v", gotArgv, wantArgv)
	}

	resolvedGotWd, err := filepath.EvalSymlinks(gotWd)
	if err != nil {
		t.Fatalf("eval symlinks got wd: %v", err)
	}
	resolvedWantWd, err := filepath.EvalSymlinks(absRepo)
	if err != nil {
		t.Fatalf("eval symlinks want wd: %v", err)
	}
	if resolvedGotWd != resolvedWantWd {
		t.Fatalf("wd = %s, want %s", gotWd, absRepo)
	}

	if !fileExistsForTest(settingsPath) {
		t.Fatalf("settings.json not created at %s", settingsPath)
	}
	if !fileExistsForTest(promptPath) {
		t.Fatalf("prompt.md not created at %s", promptPath)
	}

	wantStderr := fmt.Sprintf("nightshift: starting spec interview for demo (draft: %s) — exit Claude when done, then run: nightshift lint demo\n", absDraftPath)
	if stderr.String() != wantStderr {
		t.Fatalf("stderr = %q, want %q", stderr.String(), wantStderr)
	}
}

func TestRefineRequiresDraft(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"refine", "nope"}, &stdout, &stderr, strings.NewReader(""))
	if got != 1 {
		t.Fatalf("exit = %d, want 1 (stdout=%s)", got, stdout.String())
	}
	want := "nightshift: no draft named nope in specs/draft/ — 'refine' only works on drafts\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRefineExecs(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	fakeBin := fakeClaudeBin(t)
	t.Setenv("NIGHTSHIFT_HOST_CLAUDE_BIN", fakeBin)
	srcRepo := newTestSourceRepo(t)

	var setupOut, setupErr bytes.Buffer
	if got := dispatch([]string{"new", "demo", "--repo", srcRepo, "--no-interview"}, &setupOut, &setupErr, strings.NewReader("")); got != 0 {
		t.Fatalf("new: exit=%d stderr=%s", got, setupErr.String())
	}

	oldIsTerminal, oldExecFn := isTerminal, execFn
	isTerminal = func() bool { return true }
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	var gotArgv []string
	execFn = func(argv0 string, argv, envv []string) error {
		gotArgv = argv
		return nil
	}
	t.Cleanup(func() {
		isTerminal = oldIsTerminal
		execFn = oldExecFn
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore wd: %v", err)
		}
	})

	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"refine", "demo"}, &stdout, &stderr, strings.NewReader(""))
	if got != 0 {
		t.Fatalf("exit = %d, want 0 (stderr=%s)", got, stderr.String())
	}

	absDraftPath, err := filepath.Abs(filepath.Join(home.DraftDir(homeDir), "demo.md"))
	if err != nil {
		t.Fatalf("abs draft: %v", err)
	}
	wantKickoff := fmt.Sprintf("Read the nightshift spec draft at %s, tell me its weakest points against the checklist, then interview me to improve it.", absDraftPath)
	if len(gotArgv) == 0 || gotArgv[len(gotArgv)-1] != wantKickoff {
		t.Fatalf("argv last element = %q, want %q (full argv: %v)", lastOrEmpty(gotArgv), wantKickoff, gotArgv)
	}
}

func lastOrEmpty(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[len(s)-1]
}

func TestUsageListsRefine(t *testing.T) {
	if !strings.Contains(usage, "nightshift refine <slug>") {
		t.Fatalf("usage missing refine line:\n%s", usage)
	}
}
