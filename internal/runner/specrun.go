package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
	"github.com/jwbonnell/nightshift/internal/notify"
)

// Status is a run's terminal (or dry-run/busy) outcome.
type Status string

const (
	StatusComplete     Status = "complete"
	StatusError        Status = "error"
	StatusBlocked      Status = "blocked"
	StatusRed          Status = "red"
	StatusStalled      Status = "stalled"
	StatusCapped       Status = "capped"
	StatusWindowClosed Status = "window-closed"
	StatusBusy         Status = "busy"
	StatusInterrupted  Status = "interrupted"      // D8
	StatusRateLimited  Status = "rate-limited"     // Task 1: agent hit the Claude usage limit
	statusDryRun       Status = "dry-run"          // internal
	statusNothing      Status = "nothing-eligible" // internal: --next found nothing

	// Task 5: --jobs scheduler terminal statuses. Distinct from the
	// single-spec statuses above because a scheduler invocation's outcome
	// isn't any one spec's outcome — see runJobs's exit-code mapping in
	// scheduler.go.
	statusJobsNothingStarted Status = "jobs-nothing-started" // internal: started no child the whole run
	statusJobsChildFailed    Status = "jobs-child-failed"    // internal: a child exited 1
	statusJobsDone           Status = "jobs-done"            // internal: scheduler finished, nothing failed
)

const defaultMaxConcurrent = 2

// ExitCode maps a Status to the process exit code documented in D4/the
// reference's exit-code table. Any unrecognized status (including
// StatusError) is 1.
func ExitCode(s Status) int {
	switch s {
	case StatusComplete, statusDryRun, statusNothing, statusJobsDone:
		return 0
	case StatusBlocked:
		return 2
	case StatusRed:
		return 3
	case StatusStalled:
		return 4
	case StatusCapped:
		return 5
	case StatusWindowClosed:
		return 6
	case StatusBusy, statusJobsNothingStarted:
		return 7
	case StatusInterrupted:
		return 8
	case StatusRateLimited:
		return 9
	default:
		return 1
	}
}

// Options configures a nightshift run. Fields were added incrementally
// task by task — see PROGRESS.md for which task introduced which.
type Options struct {
	SpecArg       string // spec name or path (ignored when Next is set)
	Next          bool
	IgnoreDeps    bool // skip dependency satisfaction and the integrated base
	Fresh         bool
	Yes           bool
	MaxIter       int    // 0 = unset
	Until         string // "HH:MM" local deadline, or ""
	DryRun        bool
	Base          string  // "" = fall through to frontmatter/env/config/default
	MaxConcurrent string  // raw flag value ("" = unset)
	Jobs          int     // --jobs flag value, 0 = unset; only valid with Next (resolveJobsCount)
	Spawn         Spawner // nil = defaultSpawner (a real exec.CommandContext); --jobs child processes
	// RegisterForceKill, if set, is called once by runJobs when it starts,
	// installing its SIGKILL-the-children callback for a second interrupt
	// signal (wired to NewSchedulerInterruptContext's register return value
	// by cmd/nightshift). nil is safe: it just means a second signal exits
	// immediately with no extra cleanup, same as a plain (non-jobs) run.
	RegisterForceKill func(func())
	Home              string
	StateRoot         string    // default: <Home>/runs
	Now               time.Time // stamps run.json "started" and PROGRESS.md lines; zero = time.Now()
	Sandbox           Sandbox   // nil = real sbx sandbox
	SbxBin            string    // default "sbx"
	ClaudeBin         string    // default "claude"
	OpencodeBin       string    // default "opencode"
	Stdout            io.Writer
	Stderr            io.Writer
	Stdin             io.Reader // not in the reference's Options (which reads os.Stdin directly); added for testability
	IsTTY             bool
	Notifier          notify.Notifier                                  // nil = resolve from NIGHTSHIFT_NOTIFY/config
	Reviewer          Reviewer                                         // nil = sbxReviewer
	Ctx               context.Context                                  // D8: cancellation source; nil = context.Background()
	Version           string                                           // the running binary's version; "" is recorded as "dev"
	Sleep             func(ctx context.Context, d time.Duration) error // injectable waiter for Task 3's usage-limit wait; nil = a real timer that returns ctx.Err() early on cancellation
	clock             func() time.Time                                 // unexported: wall clock for --until and log stamps; nil = time.Now
	limits            limitConfig                                      // unexported: resolved by Run() via resolveLimitConfig; zero fields mean "use limitCfg()'s defaults"
}

// clockFn returns o.clock if set, else time.Now. D11.9: PROGRESS.md and
// history entries are stamped with this live clock, not the run's frozen
// start time (opts.Now) — unlike the reference, which used opts.Now for
// those and only the live clock for --until / result-note "ended".
func (o Options) clockFn() func() time.Time {
	if o.clock != nil {
		return o.clock
	}
	return time.Now
}

// fill defaults every unset field, reading NIGHTSHIFT_SBX_BIN/
// NIGHTSHIFT_CLAUDE_BIN (env > config > default) for the two bins that
// have no CLI flag of their own.
func (o *Options) fill() {
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.StateRoot == "" {
		o.StateRoot = home.RunsDir(o.Home)
	}

	cfg, _ := home.ParseConfig(home.ConfigPath(o.Home))
	if o.SbxBin == "" {
		o.SbxBin = envConfigDefault("NIGHTSHIFT_SBX_BIN", cfg, "sbx")
	}
	if o.ClaudeBin == "" {
		o.ClaudeBin = envConfigDefault("NIGHTSHIFT_CLAUDE_BIN", cfg, "claude")
	}
	if o.OpencodeBin == "" {
		o.OpencodeBin = envConfigDefault("NIGHTSHIFT_OPENCODE_BIN", cfg, "opencode")
	}

	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Stdin == nil {
		o.Stdin = os.Stdin
	}
}

func envConfigDefault(key string, cfg map[string]string, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	if v := cfg[key]; v != "" {
		return v
	}
	return def
}

// resolveCap resolves max_concurrent per D3: flag > env > config > 2.
// Frontmatter is never consulted.
func resolveCap(opts Options) (int, string, error) {
	parse := func(raw, errFmt string) (int, error) {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, fmt.Errorf(errFmt, raw)
		}
		return n, nil
	}

	if opts.MaxConcurrent != "" {
		n, err := parse(opts.MaxConcurrent, "--max-concurrent %q: must be an integer >= 1")
		if err != nil {
			return 0, "", err
		}
		return n, "flag", nil
	}
	if env := os.Getenv("NIGHTSHIFT_MAX_CONCURRENT"); env != "" {
		n, err := parse(env, "NIGHTSHIFT_MAX_CONCURRENT=%q: must be an integer >= 1")
		if err != nil {
			return 0, "", err
		}
		return n, "env", nil
	}
	cfg, err := home.ParseConfig(home.ConfigPath(opts.Home))
	if err != nil {
		return 0, "", err
	}
	if val := cfg["NIGHTSHIFT_MAX_CONCURRENT"]; val != "" {
		n, err := parse(val, "NIGHTSHIFT_MAX_CONCURRENT=%q: must be an integer >= 1")
		if err != nil {
			return 0, "", err
		}
		return n, "config", nil
	}
	return defaultMaxConcurrent, "default", nil
}

// Run resolves and runs (or dry-runs, or selects via --next) one spec per
// opts, returning its terminal Status. See reference §3.5.1.
func Run(opts Options) (Status, error) {
	opts.fill()

	limit, capSrc, err := resolveCap(opts)
	if err != nil {
		return StatusError, err
	}

	opts.limits, err = resolveLimitConfig(opts)
	if err != nil {
		return StatusError, err
	}

	if opts.Next && !opts.DryRun {
		jobs, _, err := resolveJobsCount(opts, limit)
		if err != nil {
			return StatusError, err
		}
		return runJobs(opts, limit, capSrc, jobs)
	}
	if opts.Next && opts.DryRun {
		jobs, _, err := resolveJobsCount(opts, limit)
		if err != nil {
			return StatusError, err
		}
		cands, waits, err := selectNextCandidates(opts)
		if err != nil {
			return StatusError, err
		}
		for _, w := range waits {
			fmt.Fprintf(opts.Stdout, "skip  %s: waits on %s (%s)\n", w.slug, w.dep, w.state)
		}
		if err := printJobsDryRun(opts, jobs, limit); err != nil {
			return StatusError, err
		}
		if jobs > 1 {
			return statusDryRun, nil
		}
		if len(cands) == 0 {
			fmt.Fprint(opts.Stdout, "no eligible spec in specs/ready/ — nothing to do\n")
			return statusNothing, nil
		}
		opts.SpecArg = cands[0].path
	}

	sp, err := resolveSpec(opts)
	if err != nil {
		return StatusError, err
	}

	if err := checkDeps(opts, sp); err != nil {
		return StatusError, err
	}

	if opts.DryRun {
		sha, err := resolveBase(sp.Repo, sp.Base)
		if err != nil {
			return StatusError, err
		}
		if err := validSHA("resolved base", sha); err != nil {
			return StatusError, err
		}
		sp.BaseSHA = sha
		printPlan(opts.Stdout, opts, sp, limit, capSrc)
		return statusDryRun, nil
	}

	specLock, err := lock.TryAcquire(specLockPath(opts.StateRoot, sp.Slug), sp.Slug)
	if err != nil {
		var held *lock.HeldError
		if errors.As(err, &held) {
			fmt.Fprintf(opts.Stderr, "spec %s is already running (pid %d, since %s). Wait for it to finish. (%s)\n",
				sp.Slug, held.Holder.PID, held.Holder.Start.Format("15:04"), held.Path)
			return StatusBusy, nil
		}
		return StatusError, err
	}

	return runLocked(opts, sp, limit, capSrc, specLock)
}

// runLocked runs sp, which already holds its spec lock (released here).
// It refuses a direct re-run of an unchanged completed spec, claims a
// concurrency slot, resolves the sandbox backend, and resolves the base
// sha (a best-effort host "fetch origin" happens first, outside any lock).
func runLocked(opts Options, sp *Spec, limit int, capSrc string, specLock *lock.Lock) (Status, error) {
	defer specLock.Release()

	if !opts.Fresh {
		prev := loadPriorState(opts, sp.Slug)
		if prev != nil && prev.Status == "complete" && prev.SpecHash == sp.ContentHash {
			return StatusError, fmt.Errorf("spec %s already completed on branch %s — move it to specs/done/ or re-run with --fresh", sp.Slug, prev.Branch)
		}
	}

	slotLock, err := acquireSlot(opts.StateRoot, limit, sp.Slug)
	if err != nil {
		return StatusError, err
	}
	if slotLock == nil {
		fmt.Fprintf(opts.Stderr, "no free slot (max_concurrent=%d, %s): %s running\n", limit, capSrc, joinOrNone(runningSlots(opts.StateRoot, limit)))
		return StatusBusy, nil
	}
	defer slotLock.Release()

	if status, handled, err := checkActiveLimit(opts); handled {
		return status, err
	}

	sb := opts.Sandbox
	if sb == nil {
		settings := agent.Settings{ClaudeBin: opts.ClaudeBin, OpencodeBin: opts.OpencodeBin, OpencodeConfig: sp.OpencodeCfg}
		codeAg, err := agent.New(sp.ModelSel, settings)
		if err != nil {
			return StatusError, err
		}
		planAg, err := agent.New(sp.PlannerSel, settings)
		if err != nil {
			return StatusError, err
		}
		real := newSbxSandboxAgents(opts.SbxBin, sp.Slug, codeAg, planAg)
		real.image = sp.Image
		sb = real
	}
	if !sb.Available() {
		return StatusError, fmt.Errorf("Docker Sandboxes (%s) is not available — install it and run `sbx setup`; the agent is never run on the host", opts.SbxBin)
	}

	_, _ = gitx.Run(sp.Repo, "fetch", "origin")

	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		return StatusError, err
	}
	if err := validSHA("resolved base", sha); err != nil {
		return StatusError, err
	}
	sp.BaseSHA = sha

	if len(sp.DependsOn) > 0 && !opts.IgnoreDeps {
		sp.depTips = resolveDepTips(sp)
	}

	return runSpec(opts, sp, sb)
}

// runSpec sets up the run, provisions the sandbox, drives the loop, and
// hands the result back via finish.
func runSpec(opts Options, sp *Spec, sb Sandbox) (Status, error) {
	// D8: cancellation before setup creates no state dir, sandbox, or
	// result note. Setup itself is never aborted midway — if cancellation
	// arrives during setup, loop()'s first check catches it instead, and
	// the run finishes interrupted with its result note.
	if opts.interrupted() {
		return StatusInterrupted, nil
	}

	r, err := setup(opts, sp)
	if err != nil {
		return StatusError, err
	}
	defer func() {
		if r.logFile != nil {
			r.logFile.Close()
		}
		if r.agentLog != nil {
			r.agentLog.Close()
		}
	}()

	if err := sb.Create(r.clone, sp.Egress); err != nil {
		return StatusError, fmt.Errorf("create sandbox: %w", err)
	}
	r.sb = sb
	sbRemoved := false
	defer func() {
		if !sbRemoved {
			_ = sb.Remove()
		}
	}()

	var status Status
	if r.needIntegrate {
		status = r.integrateDeps()
	}
	if status == "" {
		status = r.loop()
	}
	r.removeErr = sb.Remove()
	sbRemoved = true
	if r.removeErr != nil {
		r.logf("remove sandboxes: %v", r.removeErr)
	}

	status, err = r.finish(status)
	if err != nil {
		r.logf("hand-back error: %v", err)
	}
	return status, r.err
}
