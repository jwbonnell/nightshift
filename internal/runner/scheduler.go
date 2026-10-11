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
	"text/tabwriter"
)

// Waiter is the subset of *exec.Cmd the --jobs scheduler needs from an
// already-started child process, abstracted so tests can script a child's
// exit code without a real exec. Cmd.Start (or the test equivalent) has
// already happened by the time a Spawner returns one.
type Waiter interface {
	Wait() error
	Pid() int
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

func (w *execWaiter) Wait() error { return w.cmd.Wait() }
func (w *execWaiter) Pid() int    { return w.cmd.Process.Pid }

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

// runJobs is the `run --next --jobs N` scheduler (D-j), for N already
// resolved to > 1 (N<=1 is runNext, unchanged). It starts up to jobs
// eligible specs at once as child "nightshift run <path>" processes,
// starting dependents as their dependencies finish, never starting the
// same spec twice in this invocation, and stops starting new work once
// nothing is eligible or the context is cancelled (D8) — it still waits
// for whatever is already running. --until/the usage-limit-marker stop
// conditions and signal forwarding to children are not implemented yet;
// see PROGRESS.md.
func runJobs(opts Options, limit int, capSrc string, jobs int) (Status, error) {
	_ = capSrc
	spawn := opts.Spawn
	if spawn == nil {
		spawn = defaultSpawner
	}

	var outMu, errMu sync.Mutex
	stdoutWrite := func(p []byte) (int, error) { return opts.Stdout.Write(p) }
	stderrWrite := func(p []byte) (int, error) { return opts.Stderr.Write(p) }

	started := map[string]bool{}
	var order []string
	resultOf := map[string]jobsResult{}
	completions := make(chan jobsResult)
	running := 0

	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}

	startEligible := func() {
		for running < jobs && !opts.interrupted() {
			cands, _, err := selectNextCandidates(opts)
			if err != nil {
				return
			}
			var next *nextCandidate
			for i := range cands {
				if !started[cands[i].slug] {
					next = &cands[i]
					break
				}
			}
			if next == nil {
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

			w, serr := spawn(ctx, args, stdout, stderr)
			if serr != nil {
				completions <- jobsResult{slug: slug, exit: -1, status: "error"}
				continue
			}
			go func() {
				waitErr := w.Wait()
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
