package runner

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

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

	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}

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
