package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

// TestFixRoundPassCompletes: review_rounds > 0, but round 1 approves right
// away. The run completes without a hand-back, and finish must not review
// again (the round's own verdict and note are what the result note shows).
func TestFixRoundPassCompletes(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	rv := &seqReviewer{texts: []string{"VERDICT: approve\nSUMMARY: looks good.\n"}}
	opts := Options{Stdout: &bytes.Buffer{}, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\nreview_rounds: 2\n", opts, sb)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want complete", status)
	}
	if len(rv.prompts) != 1 {
		t.Fatalf("reviewer calls = %d, want 1", len(rv.prompts))
	}
	if len(r.reviewRounds) != 1 || r.reviewRounds[0].verdict != "approve" {
		t.Fatalf("reviewRounds = %+v, want one approve", r.reviewRounds)
	}

	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if len(rv.prompts) != 1 {
		t.Fatalf("finish re-reviewed: reviewer calls = %d, want 1", len(rv.prompts))
	}

	body := r.resultBody(finalStatus)
	if !strings.Contains(body, "## Review\n\nround 1: approve — ") {
		t.Fatalf("result note missing round 1: approve:\n%s", body)
	}
}

// TestFixRoundHandsBack: round 1 rejects with one blocking finding.
// REVIEW.md gets the exact hand-back content, .spec-complete is removed
// (proven by the loop continuing into a third agent call instead of
// ending after the second), and PROGRESS.md records the hand-back. That
// next iteration then runs and round 2 approves, so the run completes
// with both rounds on the result note.
func TestFixRoundHandsBack(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		switch iter {
		case 2:
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		case 3:
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	rejectText := "VERDICT: reject\nSUMMARY: still missing the thing.\n\n" +
		"## Acceptance criteria\n- [unmet] Does the thing\n\n## Findings\n- none\n"
	approveText := "VERDICT: approve\nSUMMARY: looks good now.\n"
	rv := &seqReviewer{texts: []string{rejectText, approveText}}

	opts := Options{Stdout: &bytes.Buffer{}, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\nreview_rounds: 2\n", opts, sb)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want complete", status)
	}
	if sb.agentN != 3 {
		t.Fatalf("agent ran %d time(s), want 3 (plan + 2 code iterations) — a hand-back must not end the run", sb.agentN)
	}
	if len(r.reviewRounds) != 2 || r.reviewRounds[0].verdict != "reject" || r.reviewRounds[1].verdict != "approve" {
		t.Fatalf("reviewRounds = %+v, want [reject approve]", r.reviewRounds)
	}

	reviewMD := r.scaffoldRead(r.reviewPath())
	want := "# Review round 1 — blocking findings\n\n" +
		"Fix each item below, then note its id in PROGRESS.md and touch .spec-complete again.\n\n" +
		"- **G1.1** (general) Does the thing\n"
	if reviewMD != want {
		t.Fatalf("REVIEW.md = %q, want %q", reviewMD, want)
	}

	progress := r.scaffoldRead(r.progressPath())
	wantLine := "review round 1: 1 blocking finding(s) in REVIEW.md — fix each, note its id here, then touch .spec-complete again"
	if !strings.Contains(progress, wantLine) {
		t.Fatalf("PROGRESS.md missing the hand-back line:\n%s", progress)
	}

	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if r.scaffoldExists(r.reviewPath()) {
		t.Fatalf("REVIEW.md should have been deleted by finish")
	}

	body := r.resultBody(finalStatus)
	if !strings.Contains(body, "round 1: reject — ") || !strings.Contains(body, "round 2: approve — ") {
		t.Fatalf("result body missing both rounds:\n%s", body)
	}
}

// TestFixRoundsExhausted: review_rounds: 1 with two rejects in a row must
// still end the run `complete` (never a new status), carrying this round's
// reject verdict so the notification is urgent, as nightshift-review
// already defines for a rejecting automatic review.
func TestFixRoundsExhausted(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		switch iter {
		case 2:
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		case 3:
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	rejectText := "VERDICT: reject\nSUMMARY: nope.\n\n## Findings\n- [high] still broken\n"
	rv := &seqReviewer{texts: []string{rejectText, rejectText}}

	nr := &recordingNotifierForTest{}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: nr, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\nreview_rounds: 1\n", opts, sb)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want complete (rounds exhausted, never a new status)", status)
	}
	if len(r.reviewRounds) != 2 {
		t.Fatalf("reviewRounds = %d, want 2", len(r.reviewRounds))
	}
	if r.reviewRounds[1].verdict != "reject" {
		t.Fatalf("round 2 verdict = %q, want reject", r.reviewRounds[1].verdict)
	}

	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if !nr.urgent {
		t.Fatalf("notification should be urgent on an exhausted-rounds reject")
	}
}

// TestFixRoundRefCleanedUp: refs/nightshift/review/<slug> exists in the
// source repo only for the duration of one round — both on a normal
// approve and on a reviewer error.
func TestFixRoundRefCleanedUp(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	var sawRef bool
	rv := &seqReviewer{
		texts:  []string{"VERDICT: approve\nSUMMARY: fine.\n"},
		onCall: func(ws string) { sawRef = refExists(repo, reviewRoundRef("demo")) },
	}

	opts := Options{Stdout: &bytes.Buffer{}, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\nreview_rounds: 1\n", opts, sb)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want complete", status)
	}
	if !sawRef {
		t.Fatalf("review ref did not exist in the source repo during the round")
	}
	if refExists(repo, reviewRoundRef("demo")) {
		t.Fatalf("review ref still exists after an approving round")
	}

	repo2 := newSourceRepo(t)
	sb2 := newStubSandbox()
	sb2.agent = completingAgent(t)
	sb2.verify = alwaysGreenVerify
	rv2 := &seqReviewer{errs: []error{errors.New("boom")}}
	opts2 := Options{Stdout: &bytes.Buffer{}, Reviewer: rv2}
	r2 := newTestRun(t, repo2, "review: true\nreview_rounds: 1\n", opts2, sb2)

	status2 := r2.loop()
	if status2 != StatusComplete {
		t.Fatalf("status = %v, want complete (an erroring round must not change status)", status2)
	}
	if refExists(repo2, reviewRoundRef("demo")) {
		t.Fatalf("review ref still exists after an erroring round")
	}
}

// commitFileWithBin is commitFile but execs git by absolute path instead
// of relying on PATH. The real agent would commit inside its own untrusted
// sandbox, never via host git; commitFile's usual shortcut of faking that
// with host git on PATH would otherwise show up in — and defeat —
// TestFixRoundNoHostGitOnClone's PATH-wrapper log.
func commitFileWithBin(t *testing.T, bin, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	if out, err := exec.Command(bin, "-C", dir, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command(bin, "-C", dir, "commit", "-q", "-m", msg).CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
}

// TestFixRoundNoHostGitOnClone puts a logging wrapper first on PATH after
// setup (which legitimately uses host git on the fresh clone) so any host
// git the fix-round machinery runs is caught running with the agent's
// clone as its cwd — the one trust boundary this spec must never cross
// (see CLAUDE.md's Trust model). stubSandbox.gitBin points its own Git
// calls (simulating the -ctl sandbox, which legitimately uses the clone as
// cwd) at the real git directly, bypassing the wrapper.
func TestFixRoundNoHostGitOnClone(t *testing.T) {
	repo := newSourceRepo(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("lookpath git: %v", err)
	}

	sb := newStubSandbox()
	sb.gitBin = realGit
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFileWithBin(t, realGit, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	rv := &seqReviewer{texts: []string{"VERDICT: approve\nSUMMARY: fine.\n"}}
	opts := Options{Stdout: &bytes.Buffer{}, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\nreview_rounds: 1\n", opts, sb)

	logPath := filepath.Join(t.TempDir(), "git-cwd.log")
	wrapperDir := t.TempDir()
	script := "#!/bin/sh\npwd -P >> '" + logPath + "'\nexec '" + realGit + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapperDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatalf("write git wrapper: %v", err)
	}
	t.Setenv("PATH", wrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want complete", status)
	}

	cloneReal, err := filepath.EvalSymlinks(r.clone)
	if err != nil {
		t.Fatalf("EvalSymlinks(clone): %v", err)
	}
	data, _ := os.ReadFile(logPath)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		if line == cloneReal || strings.HasPrefix(line, cloneReal+string(filepath.Separator)) {
			t.Fatalf("host git ran with cwd under the clone: %s", line)
		}
	}
}

// TestFixRoundResume: a run that hands back round 1 and then caps before
// round 2 can run must, on resume, pick up at round 2 — not restart
// numbering from 1.
func TestFixRoundResume(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 2\nreview: true\nreview_rounds: 2\n", repo)

	sb1 := newStubSandbox()
	sb1.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb1.verify = alwaysGreenVerify

	rv1 := &seqReviewer{texts: []string{"VERDICT: reject\nSUMMARY: not yet.\n\n## Findings\n- [high] fix this\n"}}

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1, Reviewer: rv1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}

	st := loadPriorState(Options{StateRoot: home.RunsDir(homeDir)}, "demo")
	if st == nil || st.ReviewRound != 1 || st.ReviewHandbacks != 1 {
		t.Fatalf("state after run1 = %+v, want review_round=1 review_handbacks=1", st)
	}

	writeReadySpec(t, homeDir, "demo", "max_iter: 3\nreview: true\nreview_rounds: 2\n", repo)
	sb2 := newStubSandbox()
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			touchComplete(t, dir)
		}
		return nil
	}
	sb2.verify = alwaysGreenVerify

	var lastSummary string
	rv2 := &seqReviewer{
		texts: []string{"VERDICT: approve\nSUMMARY: fixed.\n"},
		onCall: func(ws string) {
			data, _ := os.ReadFile(filepath.Join(ws, "inputs", "summary.md"))
			lastSummary = string(data)
		},
	}

	opts2 := opts
	opts2.Sandbox = sb2
	opts2.Reviewer = rv2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("resume: status=%v err=%v, want Complete/nil", status2, err2)
	}
	if !strings.Contains(lastSummary, "review round 2 of up to 3") {
		t.Fatalf("round 2 summary.md = %q, want mention of round 2 of up to 3", lastSummary)
	}
}

// TestRoundsZeroUnchanged pins that with review_rounds: 0 (the default),
// the fix-round machinery never runs at all: no REVIEW.md, no entry in
// reviewRounds, and finish's single automatic review behaves exactly as
// nightshift-review defined it.
func TestRoundsZeroUnchanged(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	rv := stubReviewer{text: "VERDICT: approve\nSUMMARY: fine.\n"}
	nr := &recordingNotifierForTest{}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: nr, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\n", opts, sb) // review_rounds defaults to 0

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want complete", status)
	}
	if len(r.reviewRounds) != 0 {
		t.Fatalf("reviewRounds = %+v, want none (rounds off)", r.reviewRounds)
	}
	if r.scaffoldExists(r.reviewPath()) {
		t.Fatalf("REVIEW.md should never be created with review_rounds: 0")
	}

	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if r.reviewVerdict != "approve" {
		t.Fatalf("reviewVerdict = %q, want approve via the unchanged automatic review", r.reviewVerdict)
	}
	body := r.resultBody(finalStatus)
	if !strings.Contains(body, "## Review\n\napprove — ") {
		t.Fatalf("result body missing the old single-line ## Review format:\n%s", body)
	}
	if !strings.HasSuffix(nr.msg, " · review: approve") {
		t.Fatalf("notification missing review suffix: %q", nr.msg)
	}
}
