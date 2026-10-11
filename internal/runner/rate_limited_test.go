package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

// readRunState reads and parses <stateRoot>/<slug>/run.json, for tests
// that need to inspect state fields after a full Run().
func readRunState(t *testing.T, stateRoot, slug string) runState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(stateRoot, slug, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var st runState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse run.json: %v", err)
	}
	return st
}

// TestRateLimitedStopsImmediately: iteration 1 plans normally, iteration 2
// (a worker iteration) comes back with the real 429 result line. The run
// must stop right there as rate-limited, never reaching verify.
// TestRateLimitedStopsImmediately: the reset (7am UTC) is always more than
// NIGHTSHIFT_LIMIT_MAX_WAIT away given a 1ns max wait, so this deterministically
// takes the stop path (Task 3) regardless of wall-clock time-of-day — the
// run must still stop right there as rate-limited, never reaching verify.
func TestRateLimitedStopsImmediately(t *testing.T) {
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_WAIT", "1ns")

	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			return errors.New("exit status 1")
		}
		return nil
	}
	sb.output = func(iter int) string {
		if iter == 2 {
			return rateLimitLine
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatalf("unexpected sleep %s — expected an immediate stop", d)
			return nil
		},
	}
	status, err := Run(opts)
	if err != nil || status != StatusRateLimited {
		t.Fatalf("status=%v err=%v, want RateLimited/nil", status, err)
	}
	if got := ExitCode(status); got != 9 {
		t.Fatalf("ExitCode = %d, want 9", got)
	}
	if sb.verifyN != 0 {
		t.Fatalf("verifyN = %d, want 0 — no verify after a rate-limited iteration", sb.verifyN)
	}

	st := readRunState(t, stateRoot, "demo")
	const wantMsg = "You've hit your session limit · resets 7am (UTC)"
	if st.StopReason != wantMsg {
		t.Fatalf("StopReason = %q, want %q", st.StopReason, wantMsg)
	}
	if st.LimitReset == "" {
		t.Fatalf("LimitReset not set")
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly 1 result note, err=%v entries=%v", err, entries)
	}
	noteData, err := os.ReadFile(filepath.Join(home.ResultsDir(homeDir), entries[0].Name()))
	if err != nil {
		t.Fatalf("read result note: %v", err)
	}
	resetAt, err := time.Parse(time.RFC3339, st.LimitReset)
	if err != nil {
		t.Fatalf("parse LimitReset: %v", err)
	}
	wantLine := fmt.Sprintf("Hit the Claude usage limit (%s); it resets %s, outside this run's window. Nothing was lost; re-run after it resets.",
		wantMsg, resetAt.In(time.Local).Format("Mon 15:04"))
	if !strings.Contains(string(noteData), wantLine) {
		t.Fatalf("result note missing the rate-limited summary line:\n%s", noteData)
	}
	if !strings.Contains(string(noteData), "limit_reset: "+st.LimitReset) {
		t.Fatalf("result note missing limit_reset frontmatter:\n%s", noteData)
	}
}

// TestRateLimitedPlannerNotPlanned: the planner itself is rate-limited on
// its very first attempt. Planned must stay false, and a resumed session
// plans again from scratch.
func TestRateLimitedPlannerNotPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.output = func(iter int) string {
		if iter == 1 {
			return rateLimitLine
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb.verify = alwaysGreenVerify

	opts := Options{
		limits: limitConfig{MaxWait: time.Nanosecond},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatalf("unexpected sleep %s — expected an immediate stop", d)
			return nil
		},
	}
	r := newTestRun(t, repo, "max_iter: 1\n", opts, sb)
	status := r.loop()
	if status != StatusRateLimited {
		t.Fatalf("status = %v, want RateLimited", status)
	}
	if r.st.Planned {
		t.Fatalf("Planned = true, want false after a rate-limited planning iteration")
	}
	if r.st.StopReason == "" {
		t.Fatalf("StopReason not set")
	}

	sb.output = nil // next session gets the stub's default, non-JSON output
	status = r.loop()
	if status != StatusCapped {
		t.Fatalf("second session status = %v, want Capped", status)
	}
	if !r.st.Planned {
		t.Fatalf("Planned = false, want true once the retried planner succeeded")
	}
	if len(sb.models) != 2 || sb.models[0] != r.spec.Planner || sb.models[1] != r.spec.Planner {
		t.Fatalf("models = %v, want both entries to be the planner %q", sb.models, r.spec.Planner)
	}
}

// TestRateLimitedResumeClearsReason: a resumed run that completes normally
// must not carry the previous run's StopReason forward.
func TestRateLimitedResumeClearsReason(t *testing.T) {
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_WAIT", "1ns")

	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	sb1 := newStubSandbox()
	sb1.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			return errors.New("exit status 1")
		}
		return nil
	}
	sb1.output = func(iter int) string {
		if iter == 2 {
			return rateLimitLine
		}
		return "AGENT RAW OUTPUT\n"
	}
	sb1.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatalf("unexpected sleep %s — expected an immediate stop", d)
			return nil
		},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusRateLimited {
		t.Fatalf("run1: status=%v err=%v, want RateLimited/nil", status1, err1)
	}
	if st1 := readRunState(t, stateRoot, "demo"); st1.StopReason == "" {
		t.Fatalf("StopReason not set after the rate-limited run")
	}

	sb2 := newStubSandbox()
	sb2.agent = completingAgent(t)
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("run2: status=%v err=%v, want Complete/nil", status2, err2)
	}

	st2 := readRunState(t, stateRoot, "demo")
	if st2.StopReason != "" {
		t.Fatalf("StopReason = %q, want cleared after a successful resume", st2.StopReason)
	}
}

func TestExitCodeRateLimited(t *testing.T) {
	if got := ExitCode(StatusRateLimited); got != 9 {
		t.Fatalf("ExitCode(StatusRateLimited) = %d, want 9", got)
	}
}
