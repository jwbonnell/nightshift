package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
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

	t.Run("active marker", func(t *testing.T) {
		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)
		clockFn, sleepFn, slept := newFakeClock(base)
		sb := newStubSandbox()
		opts := planOnlyOpts(homeDir, sb)
		opts.Sleep = sleepFn
		opts.clock = clockFn
		if err := writeLimitMarker(opts.StateRoot, limitHit{ResetAt: base.Add(10 * time.Minute)}, base, "other", time.Hour); err != nil {
			t.Fatal(err)
		}
		status, err := Run(opts)
		if err != nil || status != StatusRateLimited {
			t.Fatalf("status=%v err=%v, want rate-limited/nil", status, err)
		}
		if len(*slept) != 0 {
			t.Fatalf("Sleep called %v", *slept)
		}
		if sb.createN != 0 {
			t.Fatalf("createN = %d, want 0", sb.createN)
		}
	})
}

func TestPlanOnlyBlockedRerunUnchanged(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		return os.WriteFile(filepath.Join(dir, "QUESTION.md"), []byte("Which design doc?\n"), 0o644)
	}
	opts := planOnlyOpts(homeDir, sb)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}

	sb2 := newStubSandbox()
	var out strings.Builder
	opts2 := planOnlyOpts(homeDir, sb2)
	opts2.Stdout = &out
	status, err := Run(opts2)
	if err != nil || status != StatusBlocked {
		t.Fatalf("second: status=%v err=%v, want blocked/nil", status, err)
	}
	if ExitCode(status) != 2 {
		t.Fatalf("exit = %d, want 2", ExitCode(status))
	}
	if !strings.Contains(out.String(), "spec demo is still blocked — answer the question in ") {
		t.Fatalf("missing still-blocked line:\n%s", out.String())
	}
	if sb2.createN != 0 {
		t.Fatalf("createN = %d, want 0", sb2.createN)
	}
	if _, err := os.Stat(filepath.Join(CloneDir(opts.StateRoot, "demo"), "QUESTION.md")); err != nil {
		t.Fatalf("QUESTION.md gone: %v", err)
	}
}

func TestPlanOnlyAlreadyPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	opts := planOnlyOpts(homeDir, newStubSandbox())
	if status, err := Run(opts); err != nil || status != StatusPlanned {
		t.Fatalf("first: status=%v err=%v", status, err)
	}
	before, _ := os.ReadFile(filepath.Join(opts.StateRoot, "demo", "run.json"))

	sb2 := newStubSandbox()
	var out strings.Builder
	opts2 := planOnlyOpts(homeDir, sb2)
	opts2.Stdout = &out
	status, err := Run(opts2)
	if err != nil || status != StatusPlanned {
		t.Fatalf("second: status=%v err=%v", status, err)
	}
	if !strings.Contains(out.String(), "spec demo is already planned — its plan is in ") {
		t.Fatalf("missing already-planned line:\n%s", out.String())
	}
	if sb2.createN != 0 || sb2.agentN != 0 {
		t.Fatalf("createN=%d agentN=%d, want 0/0", sb2.createN, sb2.agentN)
	}
	after, _ := os.ReadFile(filepath.Join(opts.StateRoot, "demo", "run.json"))
	if string(before) != string(after) {
		t.Fatalf("run.json changed by the shortcut")
	}
}

func TestPlanOnlyReplansAfterSpecEdit(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		return os.WriteFile(filepath.Join(dir, "QUESTION.md"), []byte("Which design doc?\n"), 0o644)
	}
	if status, err := Run(planOnlyOpts(homeDir, sb)); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}
	hashBefore := loadPriorState(planOnlyOpts(homeDir, sb), "demo").SpecHash

	sbSame := newStubSandbox()
	if status, err := Run(planOnlyOpts(homeDir, sbSame)); err != nil || status != StatusBlocked || sbSame.createN != 0 {
		t.Fatalf("unedited rerun: status=%v err=%v createN=%d, want blocked via shortcut", status, err, sbSame.createN)
	}

	writeReadySpec(t, homeDir, "demo", "model: edited\n", repo)
	sb2 := newStubSandbox()
	opts := planOnlyOpts(homeDir, sb2)
	status, err := Run(opts)
	if err != nil || status != StatusPlanned {
		t.Fatalf("second: status=%v err=%v, want planned", status, err)
	}
	if len(sb2.prompts) != 1 || !strings.HasPrefix(sb2.prompts[0], planPreamble) {
		t.Fatalf("second run did not plan: %d prompts", len(sb2.prompts))
	}
	if hashAfter := loadPriorState(opts, "demo").SpecHash; hashAfter == hashBefore || hashAfter == "" {
		t.Fatalf("spec_hash not updated by the edit: before=%q after=%q", hashBefore, hashAfter)
	}

	sb3 := newStubSandbox()
	if status, err := Run(planOnlyOpts(homeDir, sb3)); err != nil || status != StatusPlanned || sb3.createN != 0 {
		t.Fatalf("third: status=%v err=%v createN=%d, want planned via shortcut", status, err, sb3.createN)
	}
}

func TestPlanOnlyWithNextIsUsageErrorLib(t *testing.T) {
	opts := planOnlyOpts(t.TempDir(), newStubSandbox())
	opts.Next = true
	if status, err := Run(opts); err == nil || status != StatusError {
		t.Fatalf("status=%v err=%v, want error", status, err)
	}
}

func TestNextPicksPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	opts := planOnlyOpts(homeDir, newStubSandbox())
	if status, err := Run(opts); err != nil || status != StatusPlanned {
		t.Fatalf("plan-only: status=%v err=%v", status, err)
	}
	path, _, err := selectNext(opts)
	if err != nil || !strings.HasSuffix(path, string(os.PathSeparator)+"demo.md") {
		t.Fatalf("selectNext = %q, %v; want demo.md", path, err)
	}

	// --next hands the planned spec to a child run, which starts on a worker.
	var calls []string
	var mu sync.Mutex
	next := Options{
		Next: true, Home: homeDir, StateRoot: opts.StateRoot, Stdout: io.Discard, Stderr: io.Discard,
		Stdin: strings.NewReader(""), Spawn: fakeSchedulerSpawner(t, opts.StateRoot, map[string]childScript{"demo": {}}, &calls, &mu),
	}
	if _, err := Run(next); err != nil {
		t.Fatalf("next: %v", err)
	}
	if len(calls) != 1 || calls[0] != "demo" {
		t.Fatalf("--next spawned %v, want [demo]", calls)
	}

	// This direct Run stands in for the spawned --next child: a real child
	// would exec a real nightshift process (fakeSchedulerSpawner only fakes it).
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	opts.PlanOnly, opts.Sandbox = false, sb
	if _, err := Run(opts); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(sb.prompts) == 0 || strings.HasPrefix(sb.prompts[0], planPreamble) {
		t.Fatalf("run did not start with a worker iteration: %v", sb.prompts)
	}
}

// realRmSandbox runs the real sbxSandbox's lifecycle (Create/ResetVerify/
// Remove against a fake sbx) and delegates the agent and git to a stub.
type realRmSandbox struct {
	*stubSandbox
	real *sbxSandbox
}

func (s realRmSandbox) Create(mountDir string, egress []string) error {
	if err := s.real.Create(mountDir, egress); err != nil {
		return err
	}
	return s.stubSandbox.Create(mountDir, egress)
}
func (s realRmSandbox) StopAgent() error   { return s.real.StopAgent() }
func (s realRmSandbox) ResetVerify() error { return s.real.ResetVerify() }
func (s realRmSandbox) RunVerify(ctx context.Context, c string) (string, error) {
	return s.real.RunVerify(ctx, c)
}
func (s realRmSandbox) Remove() error { return s.real.Remove() }

func TestPlanOnlyPassesSandboxesCheck(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_RM_STRICT", "1")
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	opts := planOnlyOpts(homeDir, newStubSandbox())
	opts.Sandbox = realRmSandbox{opts.Sandbox.(*stubSandbox), newSbxSandbox(bin, "claude", "demo")}
	if status, err := Run(opts); err != nil || status != StatusPlanned {
		t.Fatalf("status=%v err=%v, want planned/nil", status, err)
	}
	notes, _ := filepath.Glob(filepath.Join(homeDir, "results", "demo-*.md"))
	if len(notes) != 1 {
		t.Fatalf("result notes = %v", notes)
	}
	data, _ := os.ReadFile(notes[0])
	if !regexp.MustCompile(`(?m)^\s*ok\s+sandboxes$`).Match(data) {
		t.Fatalf("note lacks ok sandboxes:\n%s", data)
	}
}

func TestPlanOnlyStillBlockedNeedsQuestion(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	sb := newStubSandbox()
	sb.agent = askQuestion
	opts := planOnlyOpts(homeDir, sb)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}
	if err := os.Remove(filepath.Join(CloneDir(opts.StateRoot, "demo"), "QUESTION.md")); err != nil {
		t.Fatal(err)
	}

	sb2 := newStubSandbox()
	var out strings.Builder
	opts2 := planOnlyOpts(homeDir, sb2)
	opts2.Stdout = &out
	status, err := Run(opts2)
	if err != nil || status != StatusPlanned {
		t.Fatalf("second: status=%v err=%v, want planned", status, err)
	}
	if sb2.createN < 1 {
		t.Fatalf("createN = %d, want a planning pass", sb2.createN)
	}
	if strings.Contains(out.String(), "still blocked") {
		t.Fatalf("unexpected still-blocked line:\n%s", out.String())
	}
}
