package runner

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
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
// done, to restore default signal handling.
//
// A second signal, received after the first but before stop is called,
// exits the process immediately with code 130, without cleanup — the
// lock files held at that point are reclaimed as stale on the next run.
func NewInterruptContext() (context.Context, func()) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	out := interruptOut // captured once, so a test restoring the var later doesn't race this goroutine

	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
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
			os.Exit(130)
		case <-done:
		}
	}()

	return ctx, func() {
		stop()
		close(done)
	}
}
