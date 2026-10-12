package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// interruptOut is where NewInterruptContext writes the first-signal
// message. A package var so tests can swap it for a buffer.
var interruptOut io.Writer = os.Stderr

// interruptMessage is printed once, on the first SIGINT/SIGTERM, so a
// terminal Ctrl+C gives visible feedback instead of inviting a second one.
const interruptMessage = "nightshift: interrupt received — stopping the agent and saving state (press Ctrl+C again to force quit)"

// interrupted reports whether o.Ctx has been cancelled (D8). A nil Ctx is
// never interrupted.
func (o Options) interrupted() bool {
	return o.Ctx != nil && o.Ctx.Err() != nil
}

func (r *run) interrupted() bool {
	return r.opts.interrupted()
}

// NewInterruptContext returns a context cancelled on the first SIGINT or
// SIGTERM, for cmd/nightshift to pass as Options.Ctx (D8). Callers must
// call the returned stop function (typically via defer) once the run is
// done, to restore default signal handling. The stop function is safe to
// call more than once.
//
// A second signal, received after the first but before stop is called,
// exits the process immediately with code 130, without cleanup — the
// lock files held at that point are reclaimed as stale on the next run.
func NewInterruptContext() (context.Context, func()) {
	ctx, stop, _ := newInterruptContextWithHook(nil)
	return ctx, stop
}

// NewSchedulerInterruptContext is NewInterruptContext, plus a third return
// value, register: the --jobs scheduler calls it once runJobs starts,
// installing a callback that runs synchronously on a second SIGINT/SIGTERM,
// right before the process exits with code 130 — its one chance to SIGKILL
// already-started children before the process itself goes away mid-exit.
// Until register is called (e.g. the resolved --jobs count turns out to be
// 1, so the scheduler never runs), a second signal behaves exactly like
// NewInterruptContext's: an immediate, uncoordinated exit. cmd/nightshift
// can therefore use this unconditionally for the run command, with no
// behavior change for plain `run`/`run --next`.
func NewSchedulerInterruptContext() (context.Context, func(), func(func())) {
	var mu sync.Mutex
	var hook func()
	register := func(fn func()) {
		mu.Lock()
		defer mu.Unlock()
		hook = fn
	}
	callHook := func() {
		mu.Lock()
		fn := hook
		mu.Unlock()
		if fn != nil {
			fn()
		}
	}
	ctx, stop, _ := newInterruptContextWithHook(callHook)
	return ctx, stop, register
}

// newInterruptContext is newInterruptContextWithHook with no hook — used
// directly by tests that don't care about the scheduler's force-exit hook.
func newInterruptContext() (context.Context, func(), <-chan struct{}) {
	return newInterruptContextWithHook(nil)
}

// newInterruptContextWithHook is NewInterruptContext's implementation.
// forceHook, if non-nil, is called synchronously right before os.Exit(130)
// on a second signal — see NewSchedulerInterruptContext. The third return
// value, exited, is closed when the goroutine below returns — tests use it
// to know the goroutine has finished (and so can no longer write to
// interruptOut) rather than racily polling an output buffer.
func newInterruptContextWithHook(forceHook func()) (context.Context, func(), <-chan struct{}) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	exited := make(chan struct{})
	out := interruptOut // captured once, so a test restoring the var later doesn't race this goroutine

	go func() {
		defer close(exited)

		select {
		case <-ctx.Done():
		case <-done:
			return
		}

		// ctx.Done() can also fire because stop() was called on a normal
		// exit (it cancels ctx too). Closing done happens before stop() in
		// the returned stop func below, so if this was a normal exit, done
		// is already closed by the time we get here — check it instead of
		// trusting which case the select above happened to pick.
		select {
		case <-done:
			return
		default:
		}

		fmt.Fprintln(out, interruptMessage)

		// The first signal already cancelled ctx above. Arm a second,
		// independent registration so a further signal force-exits
		// instead of waiting for cleanup.
		forceExit := make(chan os.Signal, 1)
		signal.Notify(forceExit, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(forceExit)

		select {
		case <-forceExit:
			if forceHook != nil {
				forceHook()
			}
			os.Exit(130)
		case <-done:
		}
	}()

	var once sync.Once
	stopFn := func() {
		once.Do(func() {
			close(done) // first, so the goroutine sees it when ctx.Done fires
			stop()      // signal.NotifyContext's stop also cancels ctx
		})
	}

	return ctx, stopFn, exited
}
