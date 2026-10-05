package gitx

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := Run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "a@b.c")
	mustGit(t, dir, "config", "user.name", "a")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	out := mustGit(t, dir, "rev-list", "--count", "HEAD")
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("parse rev-list count %q: %v", out, err)
	}
	return n
}

func TestRun(t *testing.T) {
	dir := newRepo(t)

	out, err := Run(dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("Run: %v: %s", err, out)
	}
	if len(out) != 40 {
		t.Fatalf("output = %q, want a 40-char sha with whitespace trimmed", out)
	}

	out, err = Run(dir, "this-is-not-a-git-command")
	if err == nil {
		t.Fatalf("expected an error for a bad subcommand")
	}
	if out == "" {
		t.Fatalf("expected non-empty combined output on error")
	}
}

func TestIsRepo(t *testing.T) {
	dir := newRepo(t)
	if !IsRepo(dir) {
		t.Fatalf("IsRepo(%s) = false, want true", dir)
	}

	notRepo := t.TempDir()
	if IsRepo(notRepo) {
		t.Fatalf("IsRepo(%s) = true, want false", notRepo)
	}
}

func TestGitDir(t *testing.T) {
	dir := newRepo(t)

	gd, err := GitDir(dir, "--git-dir")
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	wantSuffix := filepath.Join(dir, ".git")
	resolvedWant, _ := filepath.EvalSymlinks(wantSuffix)
	if gd != resolvedWant {
		t.Fatalf("GitDir = %q, want %q", gd, resolvedWant)
	}

	// --git-common-dir from a linked worktree resolves back to the main
	// repo's .git, not the worktree's private gitdir file.
	wtDir := t.TempDir()
	mustGit(t, dir, "worktree", "add", "-q", "-b", "wt-branch", wtDir)
	common, err := GitDir(wtDir, "--git-common-dir")
	if err != nil {
		t.Fatalf("GitDir --git-common-dir: %v", err)
	}
	if common != resolvedWant {
		t.Fatalf("common dir = %q, want %q", common, resolvedWant)
	}

	if _, err := GitDir(t.TempDir(), "--git-dir"); err == nil {
		t.Fatalf("expected an error outside a repo")
	}
}

func TestRunRetrySucceedsOnceRefLockReleased(t *testing.T) {
	dir := newRepo(t)
	mustGit(t, dir, "branch", "feature")

	lockPath := filepath.Join(dir, ".git", "refs", "heads", "feature.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant ref lock: %v", err)
	}

	origBackoff := retryBaseBackoff
	retryBaseBackoff = 50 * time.Millisecond
	t.Cleanup(func() { retryBaseBackoff = origBackoff })

	go func() {
		time.Sleep(120 * time.Millisecond)
		os.Remove(lockPath)
	}()

	start := time.Now()
	out, err := RunRetry(dir, "update-ref", "-d", "refs/heads/feature")
	if err != nil {
		t.Fatalf("RunRetry: %v: %s", err, out)
	}
	if time.Since(start) < 100*time.Millisecond {
		t.Fatalf("succeeded in %s, too fast to have actually retried", time.Since(start))
	}

	if _, err := Run(dir, "rev-parse", "--verify", "--quiet", "refs/heads/feature"); err == nil {
		t.Fatalf("branch feature still exists after update-ref -d")
	}
}

func TestRunRetryGivesUpAfterMaxAttempts(t *testing.T) {
	dir := newRepo(t)
	mustGit(t, dir, "branch", "feature")

	lockPath := filepath.Join(dir, ".git", "refs", "heads", "feature.lock")
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant ref lock: %v", err)
	}
	defer os.Remove(lockPath)

	origBackoff := retryBaseBackoff
	retryBaseBackoff = 10 * time.Millisecond
	t.Cleanup(func() { retryBaseBackoff = origBackoff })

	out, err := RunRetry(dir, "update-ref", "-d", "refs/heads/feature")
	if err == nil {
		t.Fatalf("expected an error, lock was never released")
	}
	if !retryableLockErr.MatchString(out) {
		t.Fatalf("output %q does not look like the lock error it should have exhausted retries on", out)
	}
}

func TestCommitNoOpWhenNothingStaged(t *testing.T) {
	dir := newRepo(t)
	before := commitCount(t, dir)

	if err := Commit(dir, "should not happen", "README"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	after := commitCount(t, dir)
	if after != before {
		t.Fatalf("commit count = %d, want unchanged %d", after, before)
	}
}

func TestCommitScopesToPathspec(t *testing.T) {
	dir := newRepo(t)
	before := commitCount(t, dir)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644); err != nil {
		t.Fatalf("write b.txt: %v", err)
	}

	if err := Commit(dir, "add a.txt only", "a.txt"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	after := commitCount(t, dir)
	if after != before+1 {
		t.Fatalf("commit count = %d, want %d", after, before+1)
	}

	show := mustGit(t, dir, "show", "--stat", "--format=", "HEAD")
	if !strings.Contains(show, "a.txt") {
		t.Fatalf("HEAD commit does not mention a.txt: %s", show)
	}
	if strings.Contains(show, "b.txt") {
		t.Fatalf("HEAD commit should not mention b.txt: %s", show)
	}

	status := mustGit(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "b.txt") {
		t.Fatalf("b.txt should still be untracked/dirty, status: %q", status)
	}
}

func TestCommitLockPathUnderGitDir(t *testing.T) {
	dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	if err := Commit(dir, "msg", "a.txt"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	gitDir, err := GitDir(dir, "--git-dir")
	if err != nil {
		t.Fatalf("GitDir: %v", err)
	}
	lockPath := filepath.Join(gitDir, "nightshift-commit.lock")
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("commit lock file left behind at %s (stat err: %v)", lockPath, err)
	}
}
