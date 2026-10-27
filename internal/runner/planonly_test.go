package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func planOnlyOpts(homeDir string, sb *stubSandbox) Options {
	return Options{
		SpecArg: "demo", Home: homeDir, StateRoot: filepath.Join(homeDir, "state"), Sandbox: sb, PlanOnly: true,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
}

func TestPlanOnlyStopsAfterPlanning(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	opts := planOnlyOpts(homeDir, sb)
	status, err := Run(opts)
	if err != nil || status != StatusPlanned {
		t.Fatalf("status=%v err=%v, want planned/nil", status, err)
	}
	if ExitCode(status) != 0 {
		t.Fatalf("exit = %d, want 0", ExitCode(status))
	}
	if sb.agentN != 1 || sb.verifyN != 0 {
		t.Fatalf("agentN=%d verifyN=%d, want 1/0", sb.agentN, sb.verifyN)
	}
	st := loadPriorState(opts, "demo")
	if st == nil || !st.Planned || st.Status != "planned" {
		t.Fatalf("state = %+v, want planned", st)
	}
}

func TestPlanOnlyThenResumeSkipsPlanning(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	opts := planOnlyOpts(homeDir, newStubSandbox())
	if status, err := Run(opts); err != nil || status != StatusPlanned {
		t.Fatalf("plan-only: status=%v err=%v", status, err)
	}

	sb2 := newStubSandbox()
	sb2.agent = completingAgent(t)
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.PlanOnly = false
	opts2.Sandbox = sb2
	status, err := Run(opts2)
	if err != nil || status != StatusComplete {
		t.Fatalf("resume: status=%v err=%v", status, err)
	}
	if len(sb2.prompts) == 0 || strings.HasPrefix(sb2.prompts[0], planPreamble) {
		t.Fatalf("first resumed iteration was a planning pass")
	}
}

func TestPlanOnlyBlocked(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		return os.WriteFile(filepath.Join(dir, "QUESTION.md"), []byte("Which design doc?\n"), 0o644)
	}
	status, err := Run(planOnlyOpts(homeDir, sb))
	if err != nil || status != StatusBlocked {
		t.Fatalf("status=%v err=%v, want blocked/nil", status, err)
	}
	if ExitCode(status) != 2 {
		t.Fatalf("exit = %d, want 2", ExitCode(status))
	}
	notes, _ := filepath.Glob(filepath.Join(homeDir, "results", "demo-*.md"))
	if len(notes) == 0 {
		t.Fatalf("no result note")
	}
	b, _ := os.ReadFile(notes[0])
	if !strings.Contains(string(b), "Which design doc?") {
		t.Fatalf("result note lacks the question:\n%s", b)
	}
}

func TestPlanOnlyNeverWaitsOnLimit(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	clockFn, sleepFn, slept := newFakeClock(base)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error { return os.ErrInvalid }
	sb.output = func(iter int) string { return epochLimitLine(base.Add(10 * time.Minute)) }

	opts := planOnlyOpts(homeDir, sb)
	opts.Sleep = sleepFn
	opts.clock = clockFn
	status, err := Run(opts)
	if err != nil || status != StatusRateLimited {
		t.Fatalf("status=%v err=%v, want rate-limited/nil", status, err)
	}
	if len(*slept) != 0 {
		t.Fatalf("Sleep called %v", *slept)
	}
}
