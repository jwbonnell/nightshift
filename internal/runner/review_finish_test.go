package runner

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingNotifierForTest captures the last Notify call so tests can
// assert on the message/urgency the review changes produce.
type recordingNotifierForTest struct {
	title, msg string
	urgent     bool
}

func (n *recordingNotifierForTest) Notify(title, msg string, urgent bool) {
	n.title, n.msg, n.urgent = title, msg, urgent
}

func TestAutoReviewOnComplete(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	var calls int
	rv := stubReviewer{text: "VERDICT: approve\nSUMMARY: looks fine.\n", duringReview: func(ws string) {
		calls++
		headInWS := mustGit(t, filepath.Join(ws, "repo"), "rev-parse", "HEAD")
		headInSource := mustGit(t, repo, "rev-parse", "refs/heads/spec/demo")
		if headInWS != headInSource {
			t.Errorf("review ran before sync-back: ws head %s != source spec/demo %s", headInWS, headInSource)
		}
	}}

	nr := &recordingNotifierForTest{}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: nr, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\n", opts, sb)

	status := r.loop()
	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if calls != 1 {
		t.Fatalf("reviewer calls = %d, want 1", calls)
	}
	if r.reviewVerdict != "approve" {
		t.Fatalf("reviewVerdict = %q, want approve", r.reviewVerdict)
	}

	body := r.resultBody(finalStatus)
	if !strings.Contains(body, "\nreview: approve\n") {
		t.Fatalf("result body missing frontmatter review line:\n%s", body)
	}
	if !strings.Contains(body, "## Review\n\napprove — ") {
		t.Fatalf("result body missing ## Review section:\n%s", body)
	}
	if !strings.Contains(body, "## Review commands\n") {
		t.Fatalf("result body missing renamed ## Review commands section:\n%s", body)
	}

	if !strings.HasSuffix(nr.msg, " · review: approve") {
		t.Fatalf("notification message missing review suffix: %q", nr.msg)
	}
}

func TestNoAutoReviewWhenDisabled(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	var calls int
	rv := stubReviewer{text: "VERDICT: approve\n", duringReview: func(ws string) { calls++ }}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: noopNotifierForTest{}, Reviewer: rv}
	r := newTestRun(t, repo, "", opts, sb) // no review: true

	status := r.loop()
	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if calls != 0 {
		t.Fatalf("reviewer should not have run, calls = %d", calls)
	}
	if r.reviewVerdict != "" {
		t.Fatalf("reviewVerdict = %q, want empty", r.reviewVerdict)
	}
}

func TestNoAutoReviewWhenNotComplete(t *testing.T) {
	repo := newSourceRepo(t)
	cases := []struct {
		name   string
		extra  string
		agent  func(ctx context.Context, iter int, dir string) error
		verify func(iter int, dir string) error
		want   Status
	}{
		{"blocked", "review: true\n", func(ctx context.Context, iter int, dir string) error {
			if iter == 2 {
				writeQuestion(t, dir, "q?")
			}
			return nil
		}, alwaysGreenVerify, StatusBlocked},
		{"red", "review: true\n", func(ctx context.Context, iter int, dir string) error {
			if iter == 2 || iter == 3 {
				commitFile(t, dir, "work.go", "bad\n", "bad")
			}
			return nil
		}, func(iter int, dir string) error {
			if iter == 2 || iter == 3 {
				return errMockVerifyFail
			}
			return nil
		}, StatusRed},
		{"capped", "review: true\nmax_iter: 1\n", func(ctx context.Context, iter int, dir string) error {
			return nil
		}, alwaysGreenVerify, StatusCapped},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := newStubSandbox()
			sb.agent = tc.agent
			sb.verify = tc.verify

			var calls int
			rv := stubReviewer{text: "VERDICT: approve\n", duringReview: func(ws string) { calls++ }}
			opts := Options{Stdout: &bytes.Buffer{}, Notifier: noopNotifierForTest{}, Reviewer: rv}
			r := newTestRun(t, repo, tc.extra, opts, sb)

			status := r.loop()
			if status != tc.want {
				t.Fatalf("loop status = %v, want %v", status, tc.want)
			}
			if _, err := r.finish(status); err != nil {
				t.Fatalf("finish: %v", err)
			}
			if calls != 0 {
				t.Fatalf("%s: reviewer should not have run, calls = %d", tc.name, calls)
			}
			if r.reviewVerdict != "" {
				t.Fatalf("%s: reviewVerdict = %q, want empty", tc.name, r.reviewVerdict)
			}
		})
	}
}

func TestAutoReviewErrorDoesNotChangeStatus(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	rv := stubReviewer{err: errors.New("boom")}
	nr := &recordingNotifierForTest{}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: nr, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\n", opts, sb)

	status := r.loop()
	finalStatus, err := r.finish(status)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finalStatus != StatusComplete {
		t.Fatalf("finalStatus = %v, want Complete", finalStatus)
	}
	if ExitCode(finalStatus) != 0 {
		t.Fatalf("ExitCode = %d, want 0", ExitCode(finalStatus))
	}
	if r.st.Status != "complete" {
		t.Fatalf("run.json status = %q, want complete", r.st.Status)
	}
	if r.reviewVerdict != "error" {
		t.Fatalf("reviewVerdict = %q, want error", r.reviewVerdict)
	}

	body := r.resultBody(finalStatus)
	if !strings.Contains(body, "\nreview: error\n") {
		t.Fatalf("result body missing review: error:\n%s", body)
	}
	if !nr.urgent {
		t.Fatalf("notification should be urgent on a review error")
	}
}

func TestAutoReviewRejectIsUrgent(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	rv := stubReviewer{text: "VERDICT: reject\nSUMMARY: nope.\n"}
	nr := &recordingNotifierForTest{}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: nr, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\n", opts, sb)

	status := r.loop()
	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if r.reviewVerdict != "reject" {
		t.Fatalf("reviewVerdict = %q, want reject", r.reviewVerdict)
	}
	if !nr.urgent {
		t.Fatalf("notification should be urgent on reject")
	}
}

func TestAutoReviewSkippedWhenInterrupted(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	var calls int
	rv := stubReviewer{text: "VERDICT: approve\n", duringReview: func(ws string) { calls++ }}
	ctx, cancel := context.WithCancel(context.Background())
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: noopNotifierForTest{}, Reviewer: rv, Ctx: ctx}
	r := newTestRun(t, repo, "review: true\n", opts, sb)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}
	cancel() // interrupt arrives only after the run itself has completed

	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if calls != 0 {
		t.Fatalf("reviewer should not have run when interrupted, calls = %d", calls)
	}
	if r.reviewVerdict != "skipped" || r.reviewReason != "interrupted" {
		t.Fatalf("reviewVerdict=%q reviewReason=%q, want skipped/interrupted", r.reviewVerdict, r.reviewReason)
	}

	body := r.resultBody(finalStatus)
	if !strings.Contains(body, "## Review\n\nskipped (interrupted)\n") {
		t.Fatalf("result body missing skip reason:\n%s", body)
	}
}

func TestAutoReviewSkippedAfterDeadline(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	var calls int
	rv := stubReviewer{text: "VERDICT: approve\n", duringReview: func(ws string) { calls++ }}
	opts := Options{Stdout: &bytes.Buffer{}, Notifier: noopNotifierForTest{}, Reviewer: rv}
	r := newTestRun(t, repo, "review: true\n", opts, sb)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}
	r.deadline = time.Now().Add(-time.Hour) // the window closed after loop() finished

	finalStatus, err := r.finish(status)
	if err != nil || finalStatus != StatusComplete {
		t.Fatalf("finish: status=%v err=%v", finalStatus, err)
	}
	if calls != 0 {
		t.Fatalf("reviewer should not have run after the deadline, calls = %d", calls)
	}
	if r.reviewVerdict != "skipped" || r.reviewReason != "window closed" {
		t.Fatalf("reviewVerdict=%q reviewReason=%q, want skipped/window closed", r.reviewVerdict, r.reviewReason)
	}
}
