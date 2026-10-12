package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

// testExitError is a fake Waiter.Wait error implementing exitCoder, the
// same shape *exec.ExitError has, so exitCodeOf extracts the scripted code
// without any real process ever running.
type testExitError struct{ code int }

func (e *testExitError) Error() string { return fmt.Sprintf("exit status %d", e.code) }
func (e *testExitError) ExitCode() int { return e.code }

// fakeWaiter is a scripted Waiter: Wait optionally blocks on block (if
// non-nil) until the test closes it, then returns the scripted exit code.
type fakeWaiter struct {
	exit  int
	block chan struct{}
}

func (w *fakeWaiter) Wait() error {
	if w.block != nil {
		<-w.block
	}
	if w.exit == 0 {
		return nil
	}
	return &testExitError{w.exit}
}
func (w *fakeWaiter) Pid() int                   { return 0 }
func (w *fakeWaiter) Signal(sig os.Signal) error { return nil }

// childScript describes one slug's scripted child: its exit code, the
// run.json status it writes (as if it were the real child process) right
// before "exiting" ("" writes nothing, simulating a child that never got
// as far as creating state — e.g. a busy exit), an optional block gate
// for concurrency tests, and whether it writes the usage-limit marker
// before exiting (simulating a real child that hit the Claude limit).
type childScript struct {
	exit        int
	runStatus   string
	block       chan struct{}
	writeMarker bool
	afterWait   func() // called after block releases but before Wait returns; tests use it to flip shared test state deterministically
}

// slugFromRunArgs extracts the spec slug from a scheduler-built child argv
// ("run", "<path-to-slug.md>", ...).
func slugFromRunArgs(args []string) string {
	return slugFromFilename(filepath.Base(args[1]))
}

// fakeSchedulerSpawner builds a Spawner that looks up each call's script by
// slug (defaulting to an immediate, successful, stateless exit), appends
// the slug to calls (guarded by callsMu, since multiple children can spawn
// concurrently), and writes the scripted run.json only when the fake
// Waiter's Wait returns — mirroring a real child, whose state is only
// final once it has exited.
func fakeSchedulerSpawner(t *testing.T, stateRoot string, scripts map[string]childScript, calls *[]string, callsMu *sync.Mutex) Spawner {
	t.Helper()
	return func(_ context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
		slug := slugFromRunArgs(args)
		callsMu.Lock()
		*calls = append(*calls, slug)
		callsMu.Unlock()

		sc, ok := scripts[slug]
		if !ok {
			sc = childScript{exit: 0, runStatus: "complete"}
		}
		stdout.Write([]byte("working\n"))
		return &scriptedChildWaiter{t: t, stateRoot: stateRoot, slug: slug, sc: sc}, nil
	}
}

// scriptedChildWaiter writes its scripted run.json status on Wait, right
// before returning — after whatever blocking the script calls for.
type scriptedChildWaiter struct {
	t         *testing.T
	stateRoot string
	slug      string
	sc        childScript
}

func (w *scriptedChildWaiter) Wait() error {
	if w.sc.block != nil {
		<-w.sc.block
	}
	if w.sc.writeMarker {
		hit := limitHit{ResetAt: time.Now().Add(time.Hour)}
		if err := writeLimitMarker(w.stateRoot, hit, time.Now(), w.slug, time.Hour); err != nil {
			w.t.Fatalf("write limit marker: %v", err)
		}
	}
	if w.sc.runStatus != "" {
		dir := filepath.Join(w.stateRoot, w.slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			w.t.Fatalf("mkdir state dir: %v", err)
		}
		runJSON := fmt.Sprintf(`{"slug":%q,"status":%q}`, w.slug, w.sc.runStatus)
		if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(runJSON), 0o644); err != nil {
			w.t.Fatalf("write run.json: %v", err)
		}
	}
	if w.sc.afterWait != nil {
		w.sc.afterWait()
	}
	if w.sc.exit == 0 {
		return nil
	}
	return &testExitError{w.sc.exit}
}
func (w *scriptedChildWaiter) Pid() int                   { return 0 }
func (w *scriptedChildWaiter) Signal(sig os.Signal) error { return nil }

// TestJobsOneIsNext: with the effective jobs count at its default of 1,
// `run --next` must behave exactly like it did before --jobs existed —
// never touching Options.Spawn at all.
func TestJobsOneIsNext(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "solo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	spawnCalled := false
	opts := Options{
		Next: true, Home: homeDir, StateRoot: stateRoot, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Spawn: func(ctx context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
			spawnCalled = true
			return nil, fmt.Errorf("Spawn must not be called when the effective --jobs count is 1")
		},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}
	if spawnCalled {
		t.Fatal("jobs<=1 must behave exactly like plain --next")
	}
}

// TestJobsStartsDependents: a contract spec with two dependents (api, web)
// that in turn gate a fourth (integrate) — contract must finish before
// api/web start, api and web must start in the same round (together), and
// integrate only after both finish. The [slug] output prefix is checked
// too.
func TestJobsStartsDependents(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "contract", "", repo)
	writeReadySpec(t, homeDir, "api", "depends_on: contract\n", repo)
	writeReadySpec(t, homeDir, "web", "depends_on: contract\n", repo)
	writeReadySpec(t, homeDir, "integrate", "depends_on: api, web\n", repo)
	stateRoot := home.RunsDir(homeDir)

	var calls []string
	var callsMu sync.Mutex
	spawn := fakeSchedulerSpawner(t, stateRoot, nil, &calls, &callsMu)

	var stdout strings.Builder
	opts := Options{
		Next: true, Jobs: 2, Home: homeDir, StateRoot: stateRoot,
		Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}
	status, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != statusJobsDone {
		t.Fatalf("status = %v, want jobs-done", status)
	}

	want := []string{"contract", "api", "web", "integrate"}
	if len(calls) != len(want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
	for i, slug := range want {
		if calls[i] != slug {
			t.Fatalf("calls[%d] = %q, want %q (full: %v)", i, calls[i], slug, calls)
		}
	}

	for _, slug := range want {
		if !strings.Contains(stdout.String(), "["+slug+"] working\n") {
			t.Fatalf("stdout missing prefixed line for %s:\n%s", slug, stdout.String())
		}
	}
}

// TestJobsCappedByMaxConcurrent: three independent, eligible specs with
// --jobs 3 but max_concurrent 2 must never run more than 2 at once.
func TestJobsCappedByMaxConcurrent(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	writeReadySpec(t, homeDir, "b", "", repo)
	writeReadySpec(t, homeDir, "c", "", repo)
	stateRoot := home.RunsDir(homeDir)

	blockA := make(chan struct{})
	blockB := make(chan struct{})
	blockC := make(chan struct{})
	scripts := map[string]childScript{
		"a": {exit: 0, runStatus: "complete", block: blockA},
		"b": {exit: 0, runStatus: "complete", block: blockB},
		"c": {exit: 0, runStatus: "complete", block: blockC},
	}

	var calls []string
	var callsMu sync.Mutex
	spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)

	opts := Options{
		Next: true, Jobs: 3, MaxConcurrent: "2", Home: homeDir, StateRoot: stateRoot,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if status, err := Run(opts); err != nil || status != statusJobsDone {
			t.Errorf("Run: status=%v err=%v, want jobs-done/nil", status, err)
		}
	}()

	waitForCalls := func(n int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			callsMu.Lock()
			got := len(calls)
			callsMu.Unlock()
			if got >= n {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %d spawn calls", n)
	}

	waitForCalls(2)
	time.Sleep(50 * time.Millisecond) // give a would-be 3rd start a moment to (wrongly) happen
	callsMu.Lock()
	gotBeforeRelease := len(calls)
	callsMu.Unlock()
	if gotBeforeRelease != 2 {
		t.Fatalf("spawn calls = %d before any released, want exactly 2 (max_concurrent=2)", gotBeforeRelease)
	}

	close(blockA)
	waitForCalls(3)
	close(blockB)
	close(blockC)
	<-done
}

// TestJobsNoRestartInSameInvocation: a spec that finishes non-complete
// (e.g. red) must never be re-started within the same --jobs invocation,
// even though it would otherwise still look eligible.
func TestJobsNoRestartInSameInvocation(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	stateRoot := home.RunsDir(homeDir)

	scripts := map[string]childScript{"a": {exit: 0, runStatus: "red"}}
	var calls []string
	var callsMu sync.Mutex
	spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)

	opts := Options{
		Next: true, Jobs: 2, Home: homeDir, StateRoot: stateRoot,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want exactly one start of a", calls)
	}
}

// TestJobsBusyRecorded: a child that exits 7 (busy, e.g. it lost a lock
// race) is recorded as busy and doesn't escalate the scheduler's exit
// status the way an exit-1 child would.
func TestJobsBusyRecorded(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	stateRoot := home.RunsDir(homeDir)

	scripts := map[string]childScript{"a": {exit: 7, runStatus: ""}}
	var calls []string
	var callsMu sync.Mutex
	spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)

	var stdout strings.Builder
	opts := Options{
		Next: true, Jobs: 2, Home: homeDir, StateRoot: stateRoot,
		Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}
	status, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != statusJobsDone {
		t.Fatalf("status = %v, want jobs-done (busy must not escalate)", status)
	}
	var busyLine string
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.HasPrefix(line, "a ") {
			busyLine = line
		}
	}
	if !strings.Contains(busyLine, "busy") || !strings.Contains(busyLine, "7") {
		t.Fatalf("stdout table missing the busy row:\n%s", stdout.String())
	}
}

// TestJobsExitCodes covers the scheduler's exit-code mapping: 7 when
// nothing was started, 1 when any child exited 1, else 0.
func TestJobsExitCodes(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("nothing eligible", func(t *testing.T) {
		homeDir := t.TempDir()
		stateRoot := home.RunsDir(homeDir)
		opts := Options{Home: homeDir, StateRoot: stateRoot, Stdout: io.Discard, Stderr: io.Discard}
		status, err := runJobs(opts, 2, "test", 2)
		if err != nil {
			t.Fatalf("runJobs: %v", err)
		}
		if ExitCode(status) != 7 {
			t.Fatalf("ExitCode(%v) = %d, want 7", status, ExitCode(status))
		}
	})

	t.Run("a child exits 1", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "a", "", repo)
		stateRoot := home.RunsDir(homeDir)
		scripts := map[string]childScript{"a": {exit: 1, runStatus: "error"}}
		var calls []string
		var callsMu sync.Mutex
		spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)
		opts := Options{Home: homeDir, StateRoot: stateRoot, Stdout: io.Discard, Stderr: io.Discard, Spawn: spawn}
		status, err := runJobs(opts, 2, "test", 2)
		if err != nil {
			t.Fatalf("runJobs: %v", err)
		}
		if ExitCode(status) != 1 {
			t.Fatalf("ExitCode(%v) = %d, want 1", status, ExitCode(status))
		}
	})

	t.Run("everything completes", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "a", "", repo)
		stateRoot := home.RunsDir(homeDir)
		scripts := map[string]childScript{"a": {exit: 0, runStatus: "complete"}}
		var calls []string
		var callsMu sync.Mutex
		spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)
		opts := Options{Home: homeDir, StateRoot: stateRoot, Stdout: io.Discard, Stderr: io.Discard, Spawn: spawn}
		status, err := runJobs(opts, 2, "test", 2)
		if err != nil {
			t.Fatalf("runJobs: %v", err)
		}
		if ExitCode(status) != 0 {
			t.Fatalf("ExitCode(%v) = %d, want 0", status, ExitCode(status))
		}
	})
}

// waitForSchedulerCalls polls calls (guarded by callsMu) until it holds at
// least n entries, failing the test after 2s.
func waitForSchedulerCalls(t *testing.T, calls *[]string, callsMu *sync.Mutex, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		callsMu.Lock()
		got := len(*calls)
		callsMu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d spawn calls", n)
}

// TestJobsStopsOnLimitMarker: a child that hits the usage limit writes the
// marker itself (just as a real single run would) right before exiting
// rate-limited. Once that marker is on disk, the scheduler must not start
// a second, otherwise-eligible spec — even though nothing about that
// second spec's own eligibility changed.
func TestJobsStopsOnLimitMarker(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	stateRoot := home.RunsDir(homeDir)

	blockA := make(chan struct{})
	scripts := map[string]childScript{
		"a": {exit: 9, runStatus: "rate-limited", block: blockA, writeMarker: true},
	}
	var calls []string
	var callsMu sync.Mutex
	spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)

	opts := Options{
		Next: true, Jobs: 2, Home: homeDir, StateRoot: stateRoot,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}

	done := make(chan struct{})
	var status Status
	var runErr error
	go func() {
		defer close(done)
		status, runErr = Run(opts)
	}()

	waitForSchedulerCalls(t, &calls, &callsMu, 1)
	// Only now does the second, independent spec exist — so the scheduler
	// can only have skipped it below because of the marker, not because it
	// was never offered.
	writeReadySpec(t, homeDir, "b", "", repo)
	close(blockA)
	<-done

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if status != statusJobsDone {
		t.Fatalf("status = %v, want jobs-done", status)
	}
	if len(calls) != 1 || calls[0] != "a" {
		t.Fatalf("calls = %v, want exactly [a] — b must not start once the limit marker is active", calls)
	}
}

// TestJobsStopsAtUntil: once the clock reaches the --until deadline, the
// scheduler must stop starting new, otherwise-eligible specs — but it
// still waits for and records whatever already started.
func TestJobsStopsAtUntil(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	stateRoot := home.RunsDir(homeDir)

	ref := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	deadline := parseUntil("09:00", ref)
	var pastDeadline atomic.Bool
	clock := func() time.Time {
		if pastDeadline.Load() {
			return deadline.Add(time.Minute)
		}
		return ref
	}

	blockA := make(chan struct{})
	scripts := map[string]childScript{
		"a": {exit: 0, runStatus: "complete", block: blockA, afterWait: func() { pastDeadline.Store(true) }},
	}
	var calls []string
	var callsMu sync.Mutex
	spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)

	opts := Options{
		Next: true, Jobs: 2, Until: "09:00", Home: homeDir, StateRoot: stateRoot,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}
	opts.clock = clock

	done := make(chan struct{})
	var status Status
	var runErr error
	go func() {
		defer close(done)
		status, runErr = Run(opts)
	}()

	waitForSchedulerCalls(t, &calls, &callsMu, 1)
	writeReadySpec(t, homeDir, "b", "", repo)
	close(blockA)
	<-done

	if runErr != nil {
		t.Fatalf("Run: %v", runErr)
	}
	if status != statusJobsDone {
		t.Fatalf("status = %v, want jobs-done", status)
	}
	if len(calls) != 1 || calls[0] != "a" {
		t.Fatalf("calls = %v, want exactly [a] — b must not start once the --until deadline has passed", calls)
	}
}

// TestJobsDryRun: `--next --jobs N --dry-run` prints the jobs plan —
// what would start now, and what starts in the round right after — and
// spawns or creates nothing.
func TestJobsDryRun(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	writeReadySpec(t, homeDir, "b", "", repo)
	writeReadySpec(t, homeDir, "c", "depends_on: a, b\n", repo)
	stateRoot := home.RunsDir(homeDir)

	spawnCalled := false
	var stdout strings.Builder
	opts := Options{
		Next: true, DryRun: true, Jobs: 2, Home: homeDir, StateRoot: stateRoot,
		Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader(""),
		Spawn: func(ctx context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
			spawnCalled = true
			return nil, fmt.Errorf("--dry-run must never spawn a child")
		},
	}
	status, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if status != statusDryRun {
		t.Fatalf("status = %v, want dry-run", status)
	}
	if spawnCalled {
		t.Fatal("--jobs --dry-run must not spawn")
	}
	if _, serr := os.Stat(filepath.Join(stateRoot, "a", "run.json")); !os.IsNotExist(serr) {
		t.Fatalf("--jobs --dry-run must not create state: %v", serr)
	}

	want := "jobs: 2 (max_concurrent 2)\nwould start: a, b\nthen: c (after a, b)\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

// signalRecordingWaiter blocks in Wait until block is closed, recording
// every Signal call (guarded by mu) for TestJobsSignalsForwarded.
type signalRecordingWaiter struct {
	mu      sync.Mutex
	signals []os.Signal
	block   chan struct{}
}

func (w *signalRecordingWaiter) Wait() error { <-w.block; return nil }
func (w *signalRecordingWaiter) Pid() int    { return 0 }
func (w *signalRecordingWaiter) Signal(sig os.Signal) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.signals = append(w.signals, sig)
	return nil
}

func (w *signalRecordingWaiter) signalCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.signals)
}

// TestJobsSignalsForwarded: the first simulated signal must print the
// scheduler's own message and forward SIGINT to every running child; a
// second must SIGKILL them. A real second signal ends in os.Exit(130)
// (see signal.go's own TestNewInterruptContextFirstSignal comment), so —
// exactly like that existing test — this one simulates signals by driving
// opts.Ctx's cancel func and the registered force-kill hook directly,
// rather than sending real OS signals and risking killing the test binary.
func TestJobsSignalsForwarded(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	stateRoot := home.RunsDir(homeDir)

	block := make(chan struct{})
	w := &signalRecordingWaiter{block: block}
	spawnedCh := make(chan struct{}, 1)
	spawn := func(_ context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
		spawnedCh <- struct{}{}
		return w, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	registeredFn := make(chan func(), 1)
	var stderrBuf syncBuffer

	opts := Options{
		Next: true, Jobs: 2, Home: homeDir, StateRoot: stateRoot,
		Stdout: io.Discard, Stderr: &stderrBuf, Stdin: strings.NewReader(""), Spawn: spawn,
		Ctx:               ctx,
		RegisterForceKill: func(fn func()) { registeredFn <- fn },
	}

	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		runJobs(opts, 2, "test", 2)
	}()

	killHook := <-registeredFn
	<-spawnedCh
	// Give the scheduler's own bookkeeping (recording "a" as a running
	// child) a moment to finish — it happens synchronously, a few
	// instructions after spawn returns, in the same goroutine.
	time.Sleep(50 * time.Millisecond)

	cancel() // simulate the first SIGINT/SIGTERM

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && w.signalCount() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := w.signalCount(); n != 1 || w.signals[0] != os.Interrupt {
		t.Fatalf("after the first signal, child signals = %v, want exactly [interrupt]", w.signals)
	}
	wantMsg := "nightshift: interrupt received — stopping 1 run(s) (press Ctrl+C again to force quit)"
	if !strings.Contains(stderrBuf.String(), wantMsg) {
		t.Fatalf("stderr missing the scheduler's own interrupt message:\n%s", stderrBuf.String())
	}

	killHook() // simulate the second SIGINT/SIGTERM

	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && w.signalCount() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := w.signalCount(); n != 2 || w.signals[1] != syscall.SIGKILL {
		t.Fatalf("after the second signal, child signals = %v, want [interrupt SIGKILL]", w.signals)
	}

	close(block) // let the fake child "exit" so runJobs can finish
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("runJobs did not return after the fake child exited")
	}
}
