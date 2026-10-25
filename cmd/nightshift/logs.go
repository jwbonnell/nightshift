package main

import (
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/runner"
)

// logsOptsHook lets tests inject Active, Sleep, Poll and Loc.
var logsOptsHook func(*runner.LogsOptions)

const logsUsage = "usage: nightshift logs [<slug>] [-f] [--agent [--raw] [--iter N]] [--verify N] [-n N]"

func cmdLogs(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightshift logs", flag.ContinueOnError)
	fs.SetOutput(stderr)
	follow := fs.Bool("f", false, "follow the log until the run ends")
	fs.BoolVar(follow, "follow", false, "follow the log until the run ends")
	agentLog := fs.Bool("agent", false, "show the agent's activity (agent.log) instead of runner.log")
	raw := fs.Bool("raw", false, "with --agent: show the raw stream")
	iter := fs.Int("iter", 0, "with --agent: only iteration `N`")
	verify := fs.Int("verify", 0, "print the verify output of red iteration `N`")
	n := fs.Int("n", 40, "show the last `N` lines (0 = all)")
	fs.Usage = func() { fmt.Fprintln(stderr, logsUsage) }

	positional, err := parseLintFlags(fs, args)
	if err != nil {
		return 2
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	bad := len(positional) > 1 || *n < 0 ||
		(given["iter"] && *iter < 1) || (given["verify"] && *verify < 1) ||
		((*raw || given["iter"]) && !*agentLog) ||
		(given["verify"] && (*agentLog || *follow))
	if bad {
		fs.Usage()
		return 2
	}

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	ctx, stop := newInterruptContext()
	defer stop()

	opts := runner.LogsOptions{
		StateRoot: home.RunsDir(h),
		Follow:    *follow,
		Agent:     *agentLog,
		Raw:       *raw,
		Iter:      *iter,
		Verify:    *verify,
		N:         *n,
		Stdout:    stdout,
		Ctx:       ctx,
		Poll:      500 * time.Millisecond,
	}
	if len(positional) == 1 {
		opts.Slug = positional[0]
	}
	if logsOptsHook != nil {
		logsOptsHook(&opts)
	}
	if err := runner.Logs(opts); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}
