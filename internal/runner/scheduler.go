package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"text/tabwriter"
)

// Waiter is the subset of *exec.Cmd the --jobs scheduler needs from an
// already-started child process, abstracted so tests can script a child's
// exit code without a real exec. Cmd.Start (or the test equivalent) has
// already happened by the time a Spawner returns one. Signal forwards a
// signal to the child (D-j: SIGINT/SIGTERM on the scheduler's first signal,
// SIGKILL on its second).
type Waiter interface {
	Wait() error
	Pid() int
	Signal(sig os.Signal) error
}

// Spawner starts one --jobs child ("nightshift run <path> ..."), with
// stdout/stderr already wired to where the scheduler wants its (prefixed)
// output copied. A nil Options.Spawn means defaultSpawner, a real
// exec.CommandContext against os.Executable().
type Spawner func(ctx context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error)

// defaultSpawner is the real, production Spawner.
func defaultSpawner(ctx context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execWaiter{cmd}, nil
}

// execWaiter adapts *exec.Cmd to Waiter (exec.Cmd has no Pid method of its
// own — the pid lives on cmd.Process).
type execWaiter struct{ cmd *exec.Cmd }

func (w *execWaiter) Wait() error                { return w.cmd.Wait() }
func (w *execWaiter) Pid() int                   { return w.cmd.Process.Pid }
func (w *execWaiter) Signal(sig os.Signal) error { return w.cmd.Process.Signal(sig) }

// linePrefixWriter prefixes each complete line written to it with prefix
// before handing it to write, guarded by mu so concurrent children sharing
// one underlying writer (opts.Stdout/opts.Stderr) don't interleave
// mid-line. A final line with no trailing newline is flushed (prefixed,
// newline added) by flush, called once the child it belongs to has exited.
type linePrefixWriter struct {
	mu     *sync.Mutex
	write  func(p []byte) (int, error)
	prefix string
	buf    []byte
}

func newLinePrefixWriter(mu *sync.Mutex, write func(p []byte) (int, error), prefix string) *linePrefixWriter {
	return &linePrefixWriter{mu: mu, write: write, prefix: prefix}
}

func (w *linePrefixWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := w.buf[:i]
		w.buf = w.buf[i+1:]
		w.writeLine(line)
	}
	return len(p), nil
}

// flush emits any trailing, newline-less partial line left in the buffer.
func (w *linePrefixWriter) flush() {
	if len(w.buf) == 0 {
		return
	}
	line := w.buf
	w.buf = nil
	w.writeLine(line)
}

func (w *linePrefixWriter) writeLine(line []byte) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.write([]byte(w.prefix))
	_, _ = w.write(line)
	_, _ = w.write([]byte("\n"))
}

// jobsResult is one child's recorded outcome, kept in start order.
type jobsResult struct {
	slug   string
	exit   int    // the child process's exit code; -1 if it never started
	status string // run.json status after the child exited, or "busy"/"" (see runJobs)
}

// schedulerInterruptMessageFmt is the --jobs scheduler's own first-signal
// message, printed once, with the number of then-running children — distinct
// from interruptMessage (signal.go), which is the single-run child's own
// message and gets printed by the child process, not the scheduler.
const schedulerInterruptMessageFmt = "nightshift: interrupt received — stopping %d run(s) (press Ctrl+C again to force quit)\n"

// runJobs is the `run --next` scheduler (D-j): every non-dry-run --next
// invocation routes here, at any job count. It starts up to jobs
// eligible specs at once as child "nightshift run <path>" processes,
// starting dependents as their dependencies finish, never starting the
// same spec twice in this invocation, and stops starting new work once
// nothing is eligible, the --until window has ended, the usage-limit
// marker is active, or the context is cancelled (D8) — it still waits
// for whatever is already running.
//
// Signal handling: the first SIGINT/SIGTERM cancels opts.Ctx as usual
// (stopping new starts, same as any other D8 interrupt) and, here, also
// forwards SIGINT to every currently-running child so each takes its own
// graceful interrupted path. A second signal must SIGKILL the children
// before the process exits — but by the time a second real signal arrives,
// the generic interrupt goroutine in signal.go is about to call os.Exit
// directly, with no further chance for this function's own goroutines to
// run. So rather than racing that exit with a second, independent signal
// registration here, runJobs installs its SIGKILL callback via
// opts.RegisterForceKill (wired to NewSchedulerInterruptContext in
// cmd/nightshift): that callback runs synchronously, in the same
// goroutine, strictly before the os.Exit call — no race at all.
func runJobs(opts Options, limit int, capSrc string, jobs int) (Status, error) {
	_ = capSrc
	spawn := opts.Spawn
	if spawn == nil {
		spawn = defaultSpawner
	}

	var fw *follower
	if opts.Follow {
		fw = newFollower(opts.Stdout, opts.StateRoot)
		defer fw.close()
	}

	var outMu, errMu sync.Mutex
	stdoutWrite := func(p []byte) (int, error) { return opts.Stdout.Write(p) }
	stderrWrite := func(p []byte) (int, error) { return opts.Stderr.Write(p) }

	started := map[string]bool{}
	var order []string
	resultOf := map[string]jobsResult{}
	completions := make(chan jobsResult)
	running := 0

	var runningMu sync.Mutex
	runningWaiters := map[string]Waiter{}

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	done := make(chan struct{})
	defer close(done)

	if opts.RegisterForceKill != nil {
		opts.RegisterForceKill(func() {
			runningMu.Lock()
			defer runningMu.Unlock()
			for _, w := range runningWaiters {
				_ = w.Signal(syscall.SIGKILL)
			}
		})
	}

	// forwardFirstSignal reacts to ctx's first cancellation by printing the
	// scheduler's own message and forwarding SIGINT to every child running
	// at that moment. It exits without doing anything if runJobs itself
	// returns first (done closes before ctx would ever be cancelled by a
	// normal, non-interrupted exit — see newInterruptContextWithHook's own
	// identical done-vs-ctx.Done() race note in signal.go).
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			return
		}
		select {
		case <-done:
			return
		default:
		}

		runningMu.Lock()
		defer runningMu.Unlock()
		fmt.Fprintf(opts.Stderr, schedulerInterruptMessageFmt, len(runningWaiters))
		for _, w := range runningWaiters {
			_ = w.Signal(os.Interrupt)
		}
	}()

	// deadline is resolved once, like loop()'s own --until deadline:
	// parseUntil anchors "next occurrence of HH:MM" to ref, so calling it
	// again with a ref already past the first deadline would roll forward
	// to the next day instead of reporting the window closed.
	deadline := parseUntil(opts.Until, opts.clockFn()())

	// blocked records that a candidate existed but was held back by the
	// --until deadline or the usage-limit marker, not that none was ever
	// eligible — it distinguishes the two empty outcomes below. lastWaits
	// is the most recent selection's wait list, printed as "skip" lines
	// only in the "never eligible" outcome.
	blocked := false
	var lastWaits []waitSkip

	startEligible := func() {
		for running < jobs && !opts.interrupted() {
			cands, waits, err := selectNextCandidates(opts)
			if err != nil {
				return
			}
			lastWaits = waits
			var next *nextCandidate
			for i := range cands {
				if started[cands[i].slug] {
					continue
				}
				// A dependency this scheduler started and hasn't reaped may
				// be mid-write of run.json; its on-disk state is not yet
				// trustworthy, so hold the dependent until it exits.
				if dependsOnRunning(cands[i].deps, started, resultOf) {
					continue
				}
				next = &cands[i]
				break
			}
			if next == nil {
				return
			}
			now := opts.clockFn()()
			if !deadline.IsZero() && !now.Before(deadline) {
				blocked = true
				return
			}
			if readActiveLimit(opts.StateRoot, now, true) != nil {
				blocked = true
				return
			}
			slug := next.slug
			started[slug] = true
			order = append(order, slug)
			running++

			args := []string{"run", next.path}
			if opts.Until != "" {
				args = append(args, "--until", opts.Until)
			}
			args = append(args, "--max-concurrent", strconv.Itoa(limit))

			prefix := fmt.Sprintf("[%s] ", slug)
			stdout := newLinePrefixWriter(&outMu, stdoutWrite, prefix)
			stderr := newLinePrefixWriter(&errMu, stderrWrite, prefix)

			if fw != nil {
				fw.add(slug, prefix)
			}
			w, serr := spawn(ctx, args, stdout, stderr)
			if serr != nil {
				if fw != nil {
					fw.drain(slug)
				}
				completions <- jobsResult{slug: slug, exit: -1, status: "error"}
				continue
			}
			runningMu.Lock()
			runningWaiters[slug] = w
			runningMu.Unlock()
			go func() {
				waitErr := w.Wait()
				runningMu.Lock()
				delete(runningWaiters, slug)
				runningMu.Unlock()
				if fw != nil {
					fw.drain(slug)
				}
				stdout.flush()
				stderr.flush()
				completions <- jobsResult{slug: slug, exit: exitCodeOf(waitErr), status: ""}
			}()
		}
	}

	startEligible()
	for running > 0 {
		r := <-completions
		running--
		if r.status == "" {
			if r.exit == 7 {
				r.status = "busy"
			} else if st := loadPriorState(opts, r.slug); st != nil {
				r.status = st.Status
			}
		}
		resultOf[r.slug] = r
		startEligible()
	}

	if len(order) == 0 && !blocked && !opts.interrupted() {
		for _, w := range lastWaits {
			fmt.Fprintf(opts.Stdout, "skip  %s: waits on %s (%s)\n", w.slug, w.dep, w.state)
		}
		fmt.Fprint(opts.Stdout, "no eligible spec in specs/ready/ — nothing to do\n")
		return statusNothing, nil
	}

	if fw != nil {
		fw.close()
	}
	printJobsTable(opts, order, resultOf)

	switch {
	case len(order) == 0:
		return statusJobsNothingStarted, nil
	case anyChildExited1(order, resultOf):
		return statusJobsChildFailed, nil
	case opts.interrupted():
		return StatusInterrupted, nil
	default:
		return statusJobsDone, nil
	}
}

func anyChildExited1(order []string, resultOf map[string]jobsResult) bool {
	for _, slug := range order {
		if resultOf[slug].exit == 1 {
			return true
		}
	}
	return false
}

// exitCoder is satisfied by *exec.ExitError (a clean nonzero exit) and by
// fake test Waiters that script one the same way, without tying exitCodeOf
// to the concrete exec type.
type exitCoder interface{ ExitCode() int }

// exitCodeOf extracts a process's exit code from a Waiter.Wait error (nil
// on success). Anything that isn't an exitCoder (a start failure, a signal
// kill) is reported as -1.
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var ec exitCoder
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return -1
}

// printJobsTable prints the `--jobs` scheduler's end-of-run table: SLUG,
// STATUS, EXIT in start order, then a started/complete/other summary line.
func printJobsTable(opts Options, order []string, resultOf map[string]jobsResult) {
	tw := tabwriter.NewWriter(opts.Stdout, 0, 8, 2, ' ', 0)
	fmt.Fprint(tw, "SLUG\tSTATUS\tEXIT\n")
	complete := 0
	for _, slug := range order {
		r := resultOf[slug]
		fmt.Fprintf(tw, "%s\t%s\t%d\n", slug, r.status, r.exit)
		if r.status == "complete" {
			complete++
		}
	}
	_ = tw.Flush()
	fmt.Fprintf(opts.Stdout, "started %d · complete %d · other %d\n", len(order), complete, len(order)-complete)
}

// dependsOnRunning reports whether any of deps was started by this
// scheduler but has not yet completed.
func dependsOnRunning(deps []string, started map[string]bool, resultOf map[string]jobsResult) bool {
	for _, d := range deps {
		if _, done := resultOf[d]; started[d] && !done {
			return true
		}
	}
	return false
}
