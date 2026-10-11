package runner

import (
	"context"
	"errors"
	"io"
	"runtime"
	"strings"
	"testing"
)

// recordingNotifier captures the last notification sent, so tests can
// assert on urgency and title without a real notify-send.
type recordingNotifier struct {
	title, msg string
	urgent     bool
}

func (n *recordingNotifier) Notify(title, msg string, urgent bool) {
	n.title, n.msg, n.urgent = title, msg, urgent
}

// wantCloneObjectsLine is the expected clone-objects line for a freshly
// made --no-local clone on this GOOS: "ok" on Linux (where the check can
// actually inspect link counts), a "skip" elsewhere.
func wantCloneObjectsLine() string {
	if runtime.GOOS == "linux" {
		return "ok    clone-objects"
	}
	return "skip  clone-objects (not supported on " + runtime.GOOS + ")"
}

func TestPostRunChecksAllOk(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}, Version: "v1.2.3"}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	rep := r.postRunChecks()
	if rep.fails != 0 || rep.warns != 0 {
		t.Fatalf("fails=%d warns=%d, want 0/0: %v", rep.fails, rep.warns, rep.lines)
	}
	want := []string{
		"ok    sync",
		"ok    scaffold",
		"ok    clone-remotes",
		wantCloneObjectsLine(),
		"ok    sandboxes",
		"ok    nightshift v1.2.3",
	}
	if len(rep.lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(rep.lines), len(want), rep.lines)
	}
	for i, c := range rep.lines {
		if c.String() != want[i] {
			t.Fatalf("line %d = %q, want %q", i, c.String(), want[i])
		}
	}
}

func TestPostRunSyncMismatchFails(t *testing.T) {
	repo := newSourceRepo(t)
	staleSHA := mustGit(t, repo, "rev-parse", "HEAD")

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}
	if r.st.LastGreen == staleSHA {
		t.Fatalf("test setup: expected a new commit past the source repo's initial HEAD")
	}

	// Move the source ref after the run, as if something else touched it.
	mustGit(t, repo, "update-ref", "refs/heads/"+r.st.Branch, staleSHA)

	rep := r.postRunChecks()
	line := rep.lines[checkSyncIdx]
	if line.label != "FAIL" {
		t.Fatalf("sync check = %q, want FAIL", line.String())
	}
	if !strings.Contains(line.text, short(staleSHA)) || !strings.Contains(line.text, short(r.st.LastGreen)) {
		t.Fatalf("sync FAIL text = %q, want both SHAs", line.text)
	}
}

func TestPostRunScaffoldCommitFails(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			// Force past the .git/info/exclude the clone sets up for
			// PROGRESS.md, simulating an agent that commits it anyway.
			commitFileForced(t, dir, "PROGRESS.md", "sneaky\n", "sneaky scaffold commit")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	rep := r.postRunChecks()
	line := rep.lines[checkScaffoldIdx]
	if line.label != "FAIL" {
		t.Fatalf("scaffold check = %q, want FAIL", line.String())
	}
	if !strings.Contains(line.text, "PROGRESS.md") {
		t.Fatalf("scaffold FAIL text = %q, want it to name PROGRESS.md", line.text)
	}
}

// TestReviewFileIsScaffold checks REVIEW.md is treated exactly like the
// other scaffold files: nonScaffoldDirty ignores it, and a commit that
// touches it anyway fails the scaffold check by name.
func TestReviewFileIsScaffold(t *testing.T) {
	if nonScaffoldDirty("?? REVIEW.md") {
		t.Fatalf("nonScaffoldDirty(%q) = true, want false", "?? REVIEW.md")
	}

	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFileForced(t, dir, "REVIEW.md", "sneaky\n", "sneaky scaffold commit")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	rep := r.postRunChecks()
	line := rep.lines[checkScaffoldIdx]
	if line.label != "FAIL" {
		t.Fatalf("scaffold check = %q, want FAIL", line.String())
	}
	if !strings.Contains(line.text, "REVIEW.md") {
		t.Fatalf("scaffold FAIL text = %q, want it to name REVIEW.md", line.text)
	}
}

func TestPostRunCloneRemoteFails(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			mustGit(t, dir, "remote", "add", "origin", "https://example.invalid/evil.git")
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	rep := r.postRunChecks()
	line := rep.lines[checkCloneRemotesIdx]
	if line.label != "FAIL" {
		t.Fatalf("clone-remotes check = %q, want FAIL", line.String())
	}
	if !strings.Contains(line.text, "origin") {
		t.Fatalf("clone-remotes FAIL text = %q, want it to name origin", line.text)
	}
}

func TestPostRunSandboxRemoveFails(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	sb.removeErr = errors.New("boom")

	rn := &recordingNotifier{}
	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: rn}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	// Stand in for runSpec's explicit, pre-finish sb.Remove() call.
	r.removeErr = sb.Remove()

	if _, err := r.finish(status); err != nil {
		t.Fatalf("finish: %v", err)
	}

	line := r.prChecks.lines[checkSandboxesIdx]
	if line.label != "FAIL" || !strings.Contains(line.text, "boom") {
		t.Fatalf("sandboxes check = %q, want a FAIL mentioning boom", line.String())
	}
	if !rn.urgent {
		t.Fatalf("notification not urgent after a FAIL check")
	}
	if !strings.HasSuffix(rn.title, "(checks failed)") {
		t.Fatalf("title = %q, want suffix '(checks failed)'", rn.title)
	}
}

func TestPostRunChecksDontChangeStatus(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			mustGit(t, dir, "remote", "add", "origin", "https://example.invalid/evil.git")
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	finalStatus, err := r.finish(status)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finalStatus != StatusComplete {
		t.Fatalf("finalStatus = %v, want Complete even with a failing check", finalStatus)
	}
	if r.prChecks.fails == 0 {
		t.Fatalf("test setup: expected at least one failing check")
	}
	if ExitCode(finalStatus) != 0 {
		t.Fatalf("ExitCode(%v) = %d, want 0", finalStatus, ExitCode(finalStatus))
	}
}

func TestPostRunVersionDevWarns(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	rep := r.postRunChecks()
	line := rep.lines[checkVersionIdx]
	if line.label != "warn" || line.text != "nightshift dev" {
		t.Fatalf("version check = %q, want 'warn  nightshift dev'", line.String())
	}
}

func TestRemoveCalledOnce(t *testing.T) {
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
		t.Fatalf("Run: status=%v err=%v, want Complete/nil", status, err)
	}
	if sb.removeN != 1 {
		t.Fatalf("Remove called %d times, want exactly 1", sb.removeN)
	}
}

// postRunChecks always appends its six checks in this fixed order.
const (
	checkSyncIdx = iota
	checkScaffoldIdx
	checkCloneRemotesIdx
	checkCloneObjectsIdx
	checkSandboxesIdx
	checkVersionIdx
)
