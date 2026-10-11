package runner

import (
	"context"
	"testing"
	"time"
)

const overloadedLine = `{"type":"result","is_error":true,"api_error_status":529,"result":"Overloaded"}`

// TestTransientRetriesThenSucceeds: the worker iteration's first two agent
// calls come back 529 (overloaded); the third succeeds. The retries reuse
// the same iteration/session (not charged twice toward max_iter), verify
// runs exactly once, and the sleeper sees the 1m/5m backoff.
func TestTransientRetriesThenSucceeds(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 4 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 2 || iter == 3 {
			return overloadedLine
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{Sleep: sleepFn}
	opts.clock = clockFn

	r := newTestRun(t, repo, "max_iter: 5\n", opts, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if got := *slept; len(got) != 2 || got[0] != time.Minute || got[1] != 5*time.Minute {
		t.Fatalf("slept = %v, want [1m 5m]", got)
	}
	if sb.agentN != 4 {
		t.Fatalf("agentN = %d, want 4 (plan, then 2 transient worker attempts, then success)", sb.agentN)
	}
	if sb.verifyN != 1 {
		t.Fatalf("verifyN = %d, want 1", sb.verifyN)
	}
	if r.st.Iterations != 2 {
		t.Fatalf("st.Iterations = %d, want 2 (plan + worker, retries uncharged)", r.st.Iterations)
	}
}

// TestTransientGivesUpAfterThree: a 4th consecutive transient error falls
// through to today's failed-agent handling (verify skipped, no-progress
// counted) instead of retrying forever.
func TestTransientGivesUpAfterThree(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.output = func(iter int) string {
		if iter >= 2 {
			return overloadedLine
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
	if got := *slept; len(got) != 3 || got[0] != time.Minute || got[1] != 5*time.Minute || got[2] != 15*time.Minute {
		t.Fatalf("slept = %v, want [1m 5m 15m]", got)
	}
	if sb.agentN != 5 {
		t.Fatalf("agentN = %d, want 5 (plan, then 4 transient worker attempts)", sb.agentN)
	}
	if sb.verifyN != 0 {
		t.Fatalf("verifyN = %d, want 0 (verify skipped — agent failed, no changes)", sb.verifyN)
	}
	if r.st.Iterations != 2 {
		t.Fatalf("st.Iterations = %d, want 2 (plan + worker, retries uncharged)", r.st.Iterations)
	}
}

// TestTransientRespectsWindow: a retry that would land at or after the
// --until deadline takes the window-closed path instead of sleeping.
func TestTransientRespectsWindow(t *testing.T) {
	repo := newSourceRepo(t)
	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	clockFn, sleepFn, slept := newFakeClock(base)

	sb := newStubSandbox()
	sb.output = func(iter int) string {
		if iter == 2 {
			return overloadedLine
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{Sleep: sleepFn, Until: "10:01"}
	opts.clock = clockFn

	r := newTestRun(t, repo, "max_iter: 5\n", opts, sb)
	status := r.loop()
	if status != StatusWindowClosed {
		t.Fatalf("status = %v, want WindowClosed", status)
	}
	if got := *slept; len(got) != 0 {
		t.Fatalf("slept = %v, want none — the 1m retry would land at/after the deadline", got)
	}
	if sb.agentN != 2 {
		t.Fatalf("agentN = %d, want 2 (plan, then one transient worker attempt)", sb.agentN)
	}
}
