package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

// newFakeClock returns a clock pinned at start that only advances when the
// paired sleep func is invoked (which also records every requested
// duration), so loop()'s usage-limit wait/resume logic is fully
// deterministic regardless of wall-clock time.
func newFakeClock(start time.Time) (clockFn func() time.Time, sleepFn func(context.Context, time.Duration) error, slept *[]time.Duration) {
	now := start
	var durs []time.Duration
	clockFn = func() time.Time { return now }
	sleepFn = func(ctx context.Context, d time.Duration) error {
		durs = append(durs, d)
		now = now.Add(d)
		return nil
	}
	return clockFn, sleepFn, &durs
}

// epochLimitMsg builds a rate-limited result message using the epoch form
// ("Claude AI usage limit reached|<unix>"), which parseLimit resolves to
// an exact ResetAt.
func epochLimitMsg(resetAt time.Time) string {
	return fmt.Sprintf("Claude AI usage limit reached|%d", resetAt.Unix())
}

func epochLimitLine(resetAt time.Time) string {
	return fmt.Sprintf(`{"type":"result","is_error":true,"api_error_status":429,"result":%q}`, epochLimitMsg(resetAt))
}

// TestRateLimitWaitsAndResumes: iteration 2 (worker) hits a limit resetting
// 10m ahead. The run waits (12m = 10m + the 2m grace) and resumes inside the
// same loop() call; iteration 2 must not count toward max_iter.
func TestRateLimitWaitsAndResumes(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(10 * time.Minute)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 3 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 2 {
			return epochLimitLine(resetAt)
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{Sleep: sleepFn}
	opts.clock = clockFn

	// max_iter: 2 — without the exclusion, iteration 2 (the limit) would
	// consume the cap and iteration 3 would never run.
	r := newTestRun(t, repo, "max_iter: 2\n", opts, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if got := *slept; len(got) != 1 || got[0] != 12*time.Minute {
		t.Fatalf("slept = %v, want [12m]", got)
	}
	if sb.agentN != 3 {
		t.Fatalf("agentN = %d, want 3 (plan, rate-limited worker, retried worker)", sb.agentN)
	}
	found := false
	for _, h := range r.st.History {
		if strings.Contains(h, "rate-limited limit_reset=") {
			found = true
		}
	}
	if !found {
		t.Fatalf("history missing a rate-limited entry: %v", r.st.History)
	}
	if r.st.WaitingUntil != "" {
		t.Fatalf("WaitingUntil = %q, want cleared after resuming", r.st.WaitingUntil)
	}
}

// TestRateLimitBeyondWindowStops: the reset falls outside the --until
// window, so the run ends rate-limited (exit 9) with the new summary
// wording and a limit_reset frontmatter line in the result note.
func TestRateLimitBeyondWindowStops(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(10 * time.Minute) // wake = 10:12, after the 10:05 deadline
	clockFn, sleepFn, slept := newFakeClock(base)
	wantMsg := epochLimitMsg(resetAt)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 2 {
			return epochLimitLine(resetAt)
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Until: "10:05",
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Sleep: sleepFn,
	}
	opts.clock = clockFn

	status, err := Run(opts)
	if err != nil || status != StatusRateLimited {
		t.Fatalf("status=%v err=%v, want RateLimited/nil", status, err)
	}
	if got := ExitCode(status); got != 9 {
		t.Fatalf("ExitCode = %d, want 9", got)
	}
	if len(*slept) != 0 {
		t.Fatalf("slept = %v, want none — the reset is outside the window", *slept)
	}

	st := readRunState(t, home.RunsDir(homeDir), "demo")
	if st.StopReason != wantMsg {
		t.Fatalf("StopReason = %q, want %q", st.StopReason, wantMsg)
	}
	// Derive the expected reset via parseLimit itself rather than assuming a
	// zone: time.Unix (what the epoch form resolves through) carries the
	// host's Local zone, so an independently-formatted RFC3339 string could
	// render a different (but equal-instant) offset.
	wantReset := parseLimit(wantMsg, base).ResetAt.Format(time.RFC3339)
	if st.LimitReset != wantReset {
		t.Fatalf("LimitReset = %q, want %q", st.LimitReset, wantReset)
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly 1 result note, err=%v entries=%v", err, entries)
	}
	noteData, err := os.ReadFile(home.ResultsDir(homeDir) + "/" + entries[0].Name())
	if err != nil {
		t.Fatalf("read result note: %v", err)
	}
	wantLine := fmt.Sprintf("Hit the Claude usage limit (%s); it resets %s, outside this run's window. Nothing was lost; re-run after it resets.",
		wantMsg, parseLimit(wantMsg, base).ResetAt.In(time.Local).Format("Mon 15:04"))
	if !strings.Contains(string(noteData), wantLine) {
		t.Fatalf("result note missing the rate-limited summary line:\n%s", noteData)
	}
	if !strings.Contains(string(noteData), "limit_reset: "+wantReset) {
		t.Fatalf("result note missing limit_reset frontmatter:\n%s", noteData)
	}
}

// TestRateLimitUnknownResetUsesFallback: a 429 with no "resets" phrase has
// no usable reset time, so the wait uses NIGHTSHIFT_LIMIT_FALLBACK.
func TestRateLimitUnknownResetUsesFallback(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 3 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 2 {
			return `{"type":"result","is_error":true,"api_error_status":429,"result":"no reset info here"}`
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{Sleep: sleepFn, limits: limitConfig{Fallback: 30 * time.Minute}}
	opts.clock = clockFn

	r := newTestRun(t, repo, "max_iter: 5\n", opts, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if got := *slept; len(got) != 1 || got[0] != 30*time.Minute {
		t.Fatalf("slept = %v, want [30m] (the configured fallback)", got)
	}
}

// TestRateLimitMaxHits: three consecutive limits with no green iteration in
// between stop the run, even though each individual wake fits the window.
func TestRateLimitMaxHits(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.output = func(iter int) string {
		if iter >= 2 {
			return `{"type":"result","is_error":true,"api_error_status":429,"result":"hit your limit again"}`
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{Sleep: sleepFn, limits: limitConfig{Fallback: time.Minute}}
	opts.clock = clockFn

	r := newTestRun(t, repo, "max_iter: 10\n", opts, sb)
	status := r.loop()
	if status != StatusRateLimited {
		t.Fatalf("status = %v, want RateLimited", status)
	}
	if got := *slept; len(got) != 2 {
		t.Fatalf("slept = %v, want 2 waits before the 3rd (max-hits) limit stops the run", got)
	}
	if sb.agentN != 4 {
		t.Fatalf("agentN = %d, want 4 (plan, then 3 rate-limited worker attempts)", sb.agentN)
	}
}

// TestRateLimitPlannerWaitsThenPlans: a rate-limited planner waits, then
// plans again (still the planner model) rather than ending the run.
func TestRateLimitPlannerWaitsThenPlans(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(time.Minute)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.output = func(iter int) string {
		if iter == 1 {
			return epochLimitLine(resetAt)
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{Sleep: sleepFn}
	opts.clock = clockFn

	r := newTestRun(t, repo, "max_iter: 2\n", opts, sb)
	status := r.loop()
	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if !r.st.Planned {
		t.Fatalf("Planned = false, want true once the retried planner succeeded")
	}
	if got := *slept; len(got) != 1 || got[0] != 3*time.Minute {
		t.Fatalf("slept = %v, want [3m] (1m reset + 2m grace)", got)
	}
	if len(sb.models) < 2 || sb.models[0] != r.spec.Planner || sb.models[1] != r.spec.Planner {
		t.Fatalf("models = %v, want the first two entries to be the planner %q", sb.models, r.spec.Planner)
	}
}

// TestInterruptWhileWaiting: a cancellation during the wait ends the run
// interrupted, with a result note and Remove still called — exactly as an
// interrupt during an agent call behaves.
func TestInterruptWhileWaiting(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(10 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	sb := newStubSandbox()
	sb.agent = func(c context.Context, iter int, dir string) error {
		if iter == 2 {
			return fmt.Errorf("exit status 1")
		}
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 2 {
			return epochLimitLine(resetAt)
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Ctx: ctx,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Sleep: func(sleepCtx context.Context, d time.Duration) error {
			cancel()
			return sleepCtx.Err()
		},
	}
	opts.clock = func() time.Time { return base }

	status, err := Run(opts)
	if status != StatusInterrupted || err != nil {
		t.Fatalf("status=%v err=%v, want Interrupted/nil", status, err)
	}
	if !sb.removed {
		t.Fatalf("the deferred sandbox Remove should still run on interrupt")
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly 1 result note, err=%v entries=%v", err, entries)
	}

	st := readRunState(t, home.RunsDir(homeDir), "demo")
	if st.WaitingUntil != "" {
		t.Fatalf("WaitingUntil = %q, want cleared on interrupt", st.WaitingUntil)
	}
	if st.Status != "interrupted" {
		t.Fatalf("Status = %q, want interrupted", st.Status)
	}
}
