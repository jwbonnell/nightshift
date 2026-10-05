// Command nightshift hands ONE ready design spec to a Claude Code agent and
// drives it through a Ralph loop inside a Docker Sandboxes (sbx) container.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/runner"
)

// version is set via -ldflags "-X main.version=..."; see the Makefile.
var version = "dev"

const usage = `nightshift — run design specs through a sandboxed Claude Code loop

usage:
  nightshift init                       create the nightshift home (~/.nightshift or $NIGHTSHIFT_HOME)
  nightshift new <slug> [--repo PATH] [--verify CMD]
                                        start a draft spec from the template
  nightshift ready <slug>               move a draft spec to specs/ready/
  nightshift done <slug>                move a ready spec to specs/done/
  nightshift run <spec> [flags]         run one ready spec (nightshift run -h for flags)
  nightshift run --next [flags]         run the oldest eligible ready spec
  nightshift status [<slug>]            list runs, or show one run in detail
  nightshift version                    print the version
`

func main() {
	os.Exit(dispatch(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// dispatch runs the top-level command named by args[0] and returns the
// process exit code.
func dispatch(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}

	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintf(stdout, "nightshift %s\n", version)
		return 0
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "new":
		return cmdNew(args[1:], stdout, stderr)
	case "ready":
		return cmdReady(args[1:], stdout, stderr)
	case "done":
		return cmdDone(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr, stdin)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprint(stderr, usage)
		return 2
	}
}

// resolveHome resolves $NIGHTSHIFT_HOME/~/.nightshift, printing the
// standard error line itself on failure.
func resolveHome(stderr io.Writer) (string, bool) {
	h, err := home.Home()
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return "", false
	}
	return h, true
}

// resolveSbxBin applies D3's env > config > default precedence for the
// one bin init's "is it on PATH" check needs.
func resolveSbxBin(h string) string {
	if v := os.Getenv("NIGHTSHIFT_SBX_BIN"); v != "" {
		return v
	}
	cfg, _ := home.ParseConfig(home.ConfigPath(h))
	if v := cfg["NIGHTSHIFT_SBX_BIN"]; v != "" {
		return v
	}
	return "sbx"
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: nightshift init")
		return 2
	}
	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	if err := home.Init(h, resolveSbxBin(h), stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}

func cmdNew(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightshift new", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoFlag := fs.String("repo", "", "")
	verifyFlag := fs.String("verify", "", "")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift new <slug> [--repo PATH] [--verify CMD]")
	}

	// flag.Parse stops at the first non-flag argument, but D4's usage
	// puts the slug before the flags; interleave like "run" does.
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return 2
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	if len(positional) != 1 {
		fs.Usage()
		return 2
	}

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	if err := home.New(h, positional[0], *repoFlag, *verifyFlag, time.Now(), stdout); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}

func cmdReady(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: nightshift ready <slug>")
		return 2
	}
	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	if err := home.Ready(h, args[0], stdout); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}

func cmdDone(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: nightshift done <slug>")
		return 2
	}
	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	if err := home.Done(h, args[0], stdout); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	stateRoot := home.RunsDir(h)

	if len(args) == 0 {
		if err := runner.PrintStatusTable(stdout, h, stateRoot); err != nil {
			fmt.Fprintf(stderr, "nightshift: %v\n", err)
			return 1
		}
		return 0
	}
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: nightshift status [<slug>]")
		return 2
	}
	if err := runner.PrintStatusDetail(stdout, h, stateRoot, args[0]); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}

// isTTY reports whether stdin is a terminal.
func isTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// cmdRun implements `nightshift run`, exactly the reference CLI (D4) under
// this subcommand.
func cmdRun(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	fs := flag.NewFlagSet("nightshift run", flag.ExitOnError)
	fs.SetOutput(stderr)

	next := fs.Bool("next", false, "run the oldest eligible spec in specs/ready/")
	fresh := fs.Bool("fresh", false, "discard prior state for this spec and start over")
	yes := fs.Bool("yes", false, "assume yes to the --fresh confirmation")
	maxIter := fs.Int("max-iter", 0, "hard cap on iterations (default: spec/env/10)")
	until := fs.String("until", "", "local HH:MM deadline; the run stops (resumably) at it")
	dryRun := fs.Bool("dry-run", false, "resolve, validate, and print the plan; run nothing")
	base := fs.String("base", "", "branch/ref/sha the run starts from (default: spec base:/NIGHTSHIFT_BASE/origin HEAD)")
	maxConcurrent := fs.String("max-concurrent", "", "max simultaneous runs (default: NIGHTSHIFT_MAX_CONCURRENT/2)")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift run <spec-name|path> [--fresh [--yes]] [--max-iter N] [--until HH:MM] [--base REF] [--max-concurrent N] [--dry-run]")
		fmt.Fprintln(stderr, "       nightshift run --next [--until HH:MM] [--max-concurrent N] [--dry-run]")
		fs.PrintDefaults()
	}

	// Accept flags before or after the spec name, per the reference's
	// interleaved positional-parsing loop.
	var positional []string
	rest := args
	for {
		_ = fs.Parse(rest) // flag.ExitOnError exits the process itself on a real parse error
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	if *next {
		if len(positional) != 0 {
			fs.Usage()
			return 1
		}
	} else if len(positional) != 1 {
		fs.Usage()
		return 1
	}

	// D11.4: an unparsable --until is a usage error, exit 1, nothing
	// created (the reference silently ignored it).
	if *until != "" {
		if _, err := time.Parse("15:04", *until); err != nil {
			fmt.Fprintf(stderr, "nightshift: --until %q: want HH:MM (24h, local time)\n", *until)
			return 1
		}
	}

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	ctx, stop := runner.NewInterruptContext()
	defer stop()

	opts := runner.Options{
		Next:          *next,
		Fresh:         *fresh,
		Yes:           *yes,
		MaxIter:       *maxIter,
		Until:         *until,
		DryRun:        *dryRun,
		Base:          *base,
		MaxConcurrent: *maxConcurrent,
		Home:          h,
		Stdout:        stdout,
		Stderr:        stderr,
		Stdin:         stdin,
		IsTTY:         isTTY(),
		Ctx:           ctx,
	}
	if !*next {
		opts.SpecArg = positional[0]
	}

	status, err := runner.Run(opts)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return runner.ExitCode(runner.StatusError)
	}
	return runner.ExitCode(status)
}
