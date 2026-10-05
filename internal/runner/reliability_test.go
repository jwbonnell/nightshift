package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

func reapedPIDForTest(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("spawn helper process: %v", err)
	}
	return cmd.Process.Pid
}

func writeRawLockForTest(t *testing.T, path string, pid int, label string, boot int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	contents := fmt.Sprintf("%d\n%s\n%s\n%d\n%s\n", pid, label, time.Now().Format(time.RFC3339Nano), boot, "testnonce123456a")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write raw lock: %v", err)
	}
}

func TestScaffoldingNeverCommitted(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}

	clone := filepath.Join(home.RunsDir(homeDir), "demo", "repo")
	log := mustGit(t, clone, "log", "--name-only", "--format=", "HEAD")
	for _, name := range []string{"PROGRESS.md", "QUESTION.md", ".spec-complete"} {
		if strings.Contains(log, name) {
			t.Fatalf("scaffold file %s appears in a commit:\n%s", name, log)
		}
	}
}

func TestRepoHashCanonical(t *testing.T) {
	repo := newSourceRepo(t)
	h1, err := repoHash(repo)
	if err != nil {
		t.Fatalf("repoHash: %v", err)
	}

	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, repo, "worktree", "add", "-q", "-b", "wt-branch", wt)
	h2, err := repoHash(wt)
	if err != nil {
		t.Fatalf("repoHash(worktree): %v", err)
	}
	if h1 != h2 {
		t.Fatalf("worktree hash differs: %s vs %s", h1, h2)
	}

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	h3, err := repoHash(link)
	if err != nil {
		t.Fatalf("repoHash(symlink): %v", err)
	}
	if h1 != h3 {
		t.Fatalf("symlinked repo hash differs: %s vs %s", h1, h3)
	}
}

func TestStaleLocksReclaimed(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	deadPID := reapedPIDForTest(t)
	writeRawLockForTest(t, specLockPath(stateRoot, "demo"), deadPID, "demo", 0)
	writeRawLockForTest(t, slotLockPath(stateRoot, 1), os.Getpid(), "other", 1)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, StateRoot: stateRoot, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil (stale locks should be reclaimed, not busy)", status, err)
	}
}

func TestResultEndedAfterStarted(t *testing.T) {
	repo := newSourceRepo(t)
	now := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	opts := Options{Stdout: io.Discard, Now: now}
	opts.clock = func() time.Time { now = now.Add(time.Minute); return now }
	r := newTestRun(t, repo, "", opts, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}

	body := r.resultBody(status)
	var started, ended string
	for _, line := range strings.Split(body, "\n") {
		if v, ok := strings.CutPrefix(line, "started: "); ok {
			started = v
		}
		if v, ok := strings.CutPrefix(line, "ended: "); ok {
			ended = v
		}
	}
	ts, err := time.Parse(time.RFC3339, started)
	if err != nil {
		t.Fatalf("parse started %q: %v", started, err)
	}
	te, err := time.Parse(time.RFC3339, ended)
	if err != nil {
		t.Fatalf("parse ended %q: %v", ended, err)
	}
	if !te.After(ts) {
		t.Fatalf("ended (%s) not after started (%s)", ended, started)
	}
}

func TestResultNoteShowsBaseRef(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-q", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write x.txt: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "feat/x tip")
	mustGit(t, repo, "checkout", "-q", "main")

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	r := newTestRun(t, repo, "base: feat/x\n", Options{Stdout: io.Discard}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}

	body := r.resultBody(status)
	if !strings.Contains(body, "base_ref: feat/x") {
		t.Fatalf("body missing base_ref: feat/x:\n%s", body)
	}
	wantSuffix := fmt.Sprintf("# base feat/x @ %s", short(r.st.Base))
	if !strings.Contains(body, wantSuffix) {
		t.Fatalf("body missing %q:\n%s", wantSuffix, body)
	}
}

func TestBaseMissingErrorsFullRun(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "base: nope\n", repo)

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: newStubSandbox(),
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
	}
	status, err := Run(opts)
	if status != StatusError || err == nil {
		t.Fatalf("status=%v err=%v, want Error/non-nil", status, err)
	}
	if !strings.Contains(err.Error(), `base "nope" not found in`) {
		t.Fatalf("err = %v", err)
	}
	stateRoot := home.RunsDir(homeDir)
	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("no state dir should have been created")
	}
	if refExists(repo, "refs/heads/spec/demo") {
		t.Fatalf("no branch should have been created")
	}
}

func TestVerifyRunsInFreshSandbox(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 3 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if sb.verifyN < 2 {
		t.Fatalf("verifyN = %d, want >= 2", sb.verifyN)
	}
	if sb.resetVerifyN != sb.verifyN {
		t.Fatalf("resetVerifyN=%d != verifyN=%d: verify must run in a freshly reset sandbox every time", sb.resetVerifyN, sb.verifyN)
	}
}

func TestInvalidShaIsError(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	sb.gitHook = func(args []string, out string) string {
		if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD" {
			return "not-a-sha"
		}
		return out
	}
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)
	status := r.loop()
	if status != StatusError {
		t.Fatalf("status = %v, want Error", status)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "not a commit id") {
		t.Fatalf("r.err = %v", r.err)
	}
	if r.st.LastGreen != r.st.Base {
		t.Fatalf("last_green was corrupted: %q (base=%q)", r.st.LastGreen, r.st.Base)
	}
}

func TestSandboxBannerDoesNotCorruptState(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "bad\n", "bad")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error {
		if iter == 2 {
			return errMockVerifyFail
		}
		return nil
	}
	sb.banner = "Sandbox demo started successfully.\n"

	r := newTestRun(t, repo, "max_iter: 2\n", Options{Stdout: io.Discard}, sb)
	status := r.loop()
	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if len(r.st.Base) != 40 {
		t.Fatalf("base corrupted: %q", r.st.Base)
	}
	if r.st.LastGreen != r.st.Base {
		t.Fatalf("last_green should still equal base after the reset: %q vs %q", r.st.LastGreen, r.st.Base)
	}
	head := mustGit(t, r.clone, "rev-parse", "HEAD")
	if head != r.st.Base {
		t.Fatalf("HEAD = %s, want reset to base %s", short(head), short(r.st.Base))
	}
}

func TestSyncBackCheckedOutBranch(t *testing.T) {
	repo := newSourceRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	mustGit(t, repo, "worktree", "add", "-q", "-b", "spec/demo", wt)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}

	if r.st.SyncRef != r.pendingSyncRef() {
		t.Fatalf("SyncRef = %q, want the pending ref %q", r.st.SyncRef, r.pendingSyncRef())
	}
	if !refExists(repo, r.pendingSyncRef()) {
		t.Fatalf("pending ref %s should exist in the source repo", r.pendingSyncRef())
	}
	branchSHA := mustGit(t, repo, "rev-parse", "refs/heads/spec/demo")
	mainSHA := mustGit(t, repo, "rev-parse", "main")
	if branchSHA != mainSHA {
		t.Fatalf("refs/heads/spec/demo moved: %s, want it to stay at main %s", short(branchSHA), short(mainSHA))
	}
}

func TestSyncBackFailureIsError(t *testing.T) {
	repo := newSourceRepo(t)

	orphanDir := t.TempDir()
	mustGit(t, orphanDir, "init", "-q", "-b", "main")
	mustGit(t, orphanDir, "config", "user.email", "a@b.c")
	mustGit(t, orphanDir, "config", "user.name", "a")
	if err := os.WriteFile(filepath.Join(orphanDir, "other.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write other.txt: %v", err)
	}
	mustGit(t, orphanDir, "add", "-A")
	mustGit(t, orphanDir, "commit", "-q", "-m", "unrelated")
	orphanSHA := mustGit(t, orphanDir, "rev-parse", "HEAD")

	if out, err := gitx.RunRetry(repo, "fetch", orphanDir, "HEAD"); err != nil {
		t.Fatalf("fetch orphan: %v: %s", err, out)
	}
	if out, err := gitx.Run(repo, "update-ref", "refs/heads/spec/demo", orphanSHA); err != nil {
		t.Fatalf("plant conflicting ref: %v: %s", err, out)
	}

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete (a sync failure defers via SyncOwed, not loop itself)", status)
	}
	if !r.st.SyncOwed {
		t.Fatalf("expected SyncOwed after the per-iteration sync failed (non-fast-forward)")
	}

	// finish's own error return is about the result note itself (which
	// still gets written); the run's substantive failure lands in r.err.
	finalStatus, finishErr := r.finish(status)
	if finalStatus != StatusError {
		t.Fatalf("finish status = %v, want Error", finalStatus)
	}
	if finishErr != nil {
		t.Fatalf("finish returned an unexpected error of its own: %v", finishErr)
	}
	if r.err == nil || !strings.Contains(r.err.Error(), "sync to") {
		t.Fatalf("r.err = %v, want a sync-failure message", r.err)
	}
}

// TestGitRefLockRetry confirms syncBranchBack's copy-back fetch, which
// goes through gitx.RunRetry, survives a transient held ref lock on the
// SOURCE repo's branch ref — the underlying retry mechanism is gitx's own
// (see TestRunRetrySucceedsOnceRefLockReleased), exercised here end to end
// through a real completing run.
func TestGitRefLockRetry(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)

	branch := "spec/" + r.spec.Slug
	lockPath := filepath.Join(repo, ".git", "refs", "heads", branch+".lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("plant ref lock: %v", err)
	}
	go func() {
		time.Sleep(150 * time.Millisecond)
		os.Remove(lockPath)
	}()

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete (sync should retry past the held ref lock)", status)
	}
	if r.st.SyncOwed {
		t.Fatalf("SyncOwed should be false once the retry succeeded")
	}
	if !refExists(repo, "refs/heads/"+branch) {
		t.Fatalf("expected refs/heads/%s to exist after the sync retried successfully", branch)
	}
}

func TestRepoLockTimeout(t *testing.T) {
	repo := newSourceRepo(t)
	origWait := repoLockWait
	repoLockWait = 150 * time.Millisecond
	t.Cleanup(func() { repoLockWait = origWait })

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)

	hash, err := repoHash(repo)
	if err != nil {
		t.Fatalf("repoHash: %v", err)
	}
	lockPath := filepath.Join(r.opts.StateRoot, ".locks", "repo-"+hash+".lock")
	held, err := lock.TryAcquire(lockPath, "someone-else")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer held.Release()

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete (a timed-out repo lock defers via SyncOwed)", status)
	}
	if !r.st.SyncOwed {
		t.Fatalf("expected SyncOwed after the repo lock timed out")
	}
}
