package runner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

// syncBuffer is a mutex-guarded buffer: NewInterruptContext's goroutine
// writes the interrupt message concurrently with the test's own reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestNewInterruptContextFirstSignal exercises only the graceful path (ctx
// cancelled on the first signal). The force-exit-on-a-second-signal path
// calls os.Exit and cannot be exercised in-process without killing the
// test binary; it's reviewed by inspection instead.
func TestNewInterruptContextFirstSignal(t *testing.T) {
	old := interruptOut
	interruptOut = io.Discard
	t.Cleanup(func() { interruptOut = old })

	ctx, stop := NewInterruptContext()
	defer stop()

	if ctx.Err() != nil {
		t.Fatalf("ctx already done before any signal")
	}

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("self-signal: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("ctx was not cancelled within 2s of SIGINT")
	}
}

func TestInterruptBeforeLoop(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before Run even starts

	sb := newStubSandbox()
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Ctx: ctx,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
	}
	status, err := Run(opts)
	if status != StatusInterrupted || err != nil {
		t.Fatalf("status=%v err=%v, want Interrupted/nil", status, err)
	}

	stateRoot := home.RunsDir(homeDir)
	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("no state dir should have been created")
	}
	if sb.agentN != 0 {
		t.Fatalf("agent should never have run")
	}
	entries, _ := os.ReadDir(home.ResultsDir(homeDir))
	if len(entries) != 0 {
		t.Fatalf("no result note should have been written")
	}
}

func TestInterruptIsResumable(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)
	stateDir := filepath.Join(stateRoot, "demo")

	ctx, cancel := context.WithCancel(context.Background())
	sb := newStubSandbox()
	sb.agent = func(c context.Context, iter int, dir string) error {
		if iter == 2 {
			writeUncommitted(t, dir, "scratch.txt", "leftover\n")
			cancel()
			<-c.Done() // the derived iterCtx is a child of opts.Ctx; wait for it to observe cancellation
			return c.Err()
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Ctx: ctx,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if status != StatusInterrupted || err != nil {
		t.Fatalf("status=%v err=%v, want Interrupted/nil", status, err)
	}

	if !fileExistsT(filepath.Join(stateDir, "attempts", "iter-2.dirty.patch")) {
		t.Fatalf("iter-2.dirty.patch missing — uncommitted work was not saved")
	}
	if sb.stopN != sb.agentN {
		t.Fatalf("StopAgent not called before giving up: stopN=%d agentN=%d", sb.stopN, sb.agentN)
	}
	if !sb.removed {
		t.Fatalf("the deferred sandbox Remove should still run on interrupt")
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil {
		t.Fatalf("readdir results: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 result note, got %d", len(entries))
	}
	noteData, err := os.ReadFile(filepath.Join(home.ResultsDir(homeDir), entries[0].Name()))
	if err != nil {
		t.Fatalf("read result note: %v", err)
	}
	if !strings.Contains(string(noteData), "The run was interrupted. Uncommitted work was saved as a patch; re-run to continue.") {
		t.Fatalf("result note missing the interrupted summary line:\n%s", noteData)
	}

	// A re-run resumes the same clone rather than starting over.
	sb2 := newStubSandbox()
	sb2.agent = completingAgent(t)
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	opts2.Ctx = context.Background()
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("resumed run: status=%v err=%v, want Complete/nil", status2, err2)
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var final struct{ Iterations int }
	if err := json.Unmarshal(data, &final); err != nil {
		t.Fatalf("parse run.json: %v", err)
	}
	if final.Iterations <= 2 {
		t.Fatalf("Iterations = %d, want > 2 (continuing from the interrupted run, not restarting)", final.Iterations)
	}
}

func TestInterruptMessage(t *testing.T) {
	var buf syncBuffer
	old := interruptOut
	interruptOut = &buf
	t.Cleanup(func() { interruptOut = old })

	ctx, stop := NewInterruptContext()

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("self-signal: %v", err)
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("ctx was not cancelled within 2s of SIGINT")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(buf.String(), interruptMessage) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()

	if got := strings.Count(buf.String(), interruptMessage); got != 1 {
		t.Fatalf("interrupt message printed %d times, want 1 (buf=%q)", got, buf.String())
	}
}

// TestInterruptContextStopIsSilent is a regression test for the bug where
// a normal exit (stop() called with no signal ever received) could still
// print interruptMessage: signal.NotifyContext's stop() cancels ctx, which
// is the same event the goroutine watches for an actual signal, and
// select picks at random when multiple cases are ready. Looping catches
// scheduling-dependent failures that a single run would miss.
func TestInterruptContextStopIsSilent(t *testing.T) {
	old := interruptOut
	t.Cleanup(func() { interruptOut = old })

	for i := 0; i < 200; i++ {
		var buf syncBuffer
		interruptOut = &buf

		ctx, stop, exited := newInterruptContext()
		stop()

		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d: goroutine did not exit within 2s of stop()", i)
		}

		if ctx.Err() == nil {
			t.Fatalf("iteration %d: ctx not done after stop()", i)
		}
		if got := buf.String(); got != "" {
			t.Fatalf("iteration %d: interruptOut not empty after a normal stop(): %q", i, got)
		}
	}
}

// TestInterruptContextStopTwiceSafe checks the stop function returned by
// newInterruptContext can be called more than once without panicking.
func TestInterruptContextStopTwiceSafe(t *testing.T) {
	old := interruptOut
	t.Cleanup(func() { interruptOut = old })

	var buf syncBuffer
	interruptOut = &buf

	ctx, stop, exited := newInterruptContext()
	stop()
	stop()

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatalf("goroutine did not exit within 2s of stop()")
	}

	if ctx.Err() == nil {
		t.Fatalf("ctx not done after stop()")
	}
	if got := buf.String(); got != "" {
		t.Fatalf("interruptOut not empty after a normal stop(): %q", got)
	}
}

// TestInterruptedRunFinishesClean cancels during iteration 1 (the planning
// iteration, before any verify) and checks the whole interrupt path runs
// to completion: StopAgent and Remove are both called, a result note is
// written, and run.json records the interrupted status.
func TestInterruptedRunFinishesClean(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateDir := filepath.Join(home.RunsDir(homeDir), "demo")

	ctx, cancel := context.WithCancel(context.Background())
	sb := newStubSandbox()
	sb.agent = func(c context.Context, iter int, dir string) error {
		if iter == 1 {
			cancel()
			<-c.Done()
			return c.Err()
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Ctx: ctx,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if status != StatusInterrupted || err != nil {
		t.Fatalf("status=%v err=%v, want Interrupted/nil", status, err)
	}

	if sb.stopN < 1 || sb.stopN != sb.agentN {
		t.Fatalf("StopAgent not called before giving up: stopN=%d agentN=%d", sb.stopN, sb.agentN)
	}
	if !sb.removed {
		t.Fatalf("the deferred sandbox Remove should still run on interrupt")
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil {
		t.Fatalf("readdir results: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 result note, got %d", len(entries))
	}

	data, err := os.ReadFile(filepath.Join(stateDir, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var st struct {
		Status  string   `json:"status"`
		History []string `json:"history"`
	}
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse run.json: %v", err)
	}
	if st.Status != "interrupted" {
		t.Fatalf("run.json status = %q, want interrupted", st.Status)
	}
	if len(st.History) == 0 || !strings.Contains(st.History[len(st.History)-1], " interrupted") {
		t.Fatalf("run.json history last entry = %q, want to contain \" interrupted\"", st.History)
	}
}
