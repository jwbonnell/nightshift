// Command nightshift hands ONE ready design spec to a Claude Code agent and
// drives it through a Ralph loop inside a Docker Sandboxes (sbx) container.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/interview"
	"github.com/jwbonnell/nightshift/internal/runner"
)

// version is set via -ldflags "-X main.version=..."; see the Makefile.
var version = "dev"

// newBaseVerifier is a test seam: when set, lint uses its return value as
// the BaseVerifier instead of letting runner.Lint construct the real
// sbxBaseVerifier itself. Production leaves it nil.
var newBaseVerifier func(bin string) runner.BaseVerifier

// runFn is a test seam over runner.Run: tests overwrite this to capture the
// Options a run was invoked with (e.g. Version) without driving a real run.
var runFn = runner.Run

// execFn replaces the current process with argv0, the way a successful
// interview launch hands off to claude. A test seam: tests overwrite this
// so nothing ever execs a real binary.
var execFn = syscall.Exec

// newInterruptContext is a test seam over runner.NewInterruptContext, so
// tests can assert lint and ready install an interrupt-aware context
// without sending a real signal.
var newInterruptContext = runner.NewInterruptContext

// isTerminal reports whether both stdin and stdout are a TTY, the gate
// `new`/`refine` use to decide whether an interactive interview is
// possible at all. A test seam, forced false in TestMain.
var isTerminal = func() bool {
	return isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
}

func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

const usage = `nightshift — run design specs through a sandboxed Claude Code loop

usage:
  nightshift init                       create the nightshift home (~/.nightshift or $NIGHTSHIFT_HOME)
  nightshift new <slug> [--repo PATH] [--verify CMD] [--no-interview]
                                        start a draft and interview you to write it
  nightshift refine <slug>              reopen the interview on a draft
  nightshift ready <slug> [--no-verify] [--no-lint]
                                        lint a draft, then move it to specs/ready/
  nightshift lint <spec> [--no-verify]  check a draft or ready spec (verify runs on the base commit in sbx)
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
	case "refine":
		return cmdRefine(args[1:], stdout, stderr)
	case "ready":
		return cmdReady(args[1:], stdout, stderr)
	case "lint":
		return cmdLint(args[1:], stdout, stderr)
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

// resolveSetting applies D3's env > config > default precedence for a
// single config key.
func resolveSetting(h, key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	cfg, _ := home.ParseConfig(home.ConfigPath(h))
	if v := cfg[key]; v != "" {
		return v
	}
	return def
}

// resolveSbxBin applies D3's env > config > default precedence for the
// one bin init's "is it on PATH" check needs.
func resolveSbxBin(h string) string {
	return resolveSetting(h, "NIGHTSHIFT_SBX_BIN", "sbx")
}

// expandHome expands a leading "~/" using the real home directory; any
// other path is returned unchanged.
func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	hd, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(hd, p[2:]), nil
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
	noInterview := fs.Bool("no-interview", false, "")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift new <slug> [--repo PATH] [--verify CMD] [--no-interview]")
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
	slug := positional[0]

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}
	if err := home.New(h, slug, *repoFlag, *verifyFlag, time.Now(), stdout); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}

	if *noInterview || !isTerminal() {
		fmt.Fprintf(stdout, "edit the draft, then: nightshift lint %s\n", slug)
		return 0
	}

	claudeBin := resolveSetting(h, "NIGHTSHIFT_HOST_CLAUDE_BIN", "claude")
	if _, err := exec.LookPath(claudeBin); err != nil {
		fmt.Fprintf(stdout, "claude not found on PATH — edit the draft by hand, then: nightshift lint %s\n", slug)
		return 0
	}

	if *repoFlag == "" {
		fmt.Fprintln(stdout, "pass --repo PATH to be interviewed, or edit the draft by hand")
		return 0
	}

	repoAbs, err := resolveRepoAbs(*repoFlag)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	if !gitx.IsRepo(repoAbs) {
		fmt.Fprintf(stdout, "--repo %s is not a git repository — fix it, or edit the draft by hand\n", *repoFlag)
		return 0
	}

	draftPath, err := filepath.Abs(filepath.Join(home.DraftDir(h), slug+".md"))
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return launchInterview(h, slug, draftPath, repoAbs, false, claudeBin, stderr)
}

// resolveRepoAbs expands a leading "~/" and resolves p to an absolute
// path, the way both `new`'s --repo flag and `refine`'s draft frontmatter
// `repo:` value need resolving before a gitx.IsRepo check.
func resolveRepoAbs(p string) (string, error) {
	expanded, err := expandHome(p)
	if err != nil {
		return "", err
	}
	return filepath.Abs(expanded)
}

// launchInterview prepares the interview files and execs the host claude
// in repoAbs, replacing the current process. It only returns on error (a
// successful exec never returns).
func launchInterview(h, slug, draftPath, repoAbs string, refine bool, claudeBin string, stderr io.Writer) int {
	homeAbs, err := filepath.Abs(h)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	model := resolveSetting(h, "NIGHTSHIFT_INTERVIEW_MODEL", "opus")
	argv, dir, err := interview.Prepare(interview.Options{
		Home:      homeAbs,
		Slug:      slug,
		DraftPath: draftPath,
		Repo:      repoAbs,
		Refine:    refine,
		ClaudeBin: claudeBin,
		Model:     model,
		Now:       time.Now(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}

	fmt.Fprintf(stderr, "nightshift: starting spec interview for %s (draft: %s) — exit Claude when done, then run: nightshift lint %s\n", slug, draftPath, slug)

	if err := os.Chdir(dir); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	if err := execFn(argv[0], argv, os.Environ()); err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return 0
}

// cmdRefine implements `nightshift refine <slug>`: reopens the interview
// on an existing draft, reading its repo: frontmatter rather than taking
// a --repo flag.
func cmdRefine(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightshift refine", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift refine <slug>")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fs.Usage()
		return 2
	}
	slug := rest[0]

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	draftPath := filepath.Join(home.DraftDir(h), slug+".md")
	data, err := os.ReadFile(draftPath)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: no draft named %s in specs/draft/ — 'refine' only works on drafts\n", slug)
		return 1
	}

	fm := home.ParseFrontmatter(data)
	repoRaw := home.Unquote(strings.TrimSpace(fm["repo"]))
	var repoAbs string
	if repoRaw != "" {
		repoAbs, err = resolveRepoAbs(repoRaw)
		if err != nil {
			fmt.Fprintf(stderr, "nightshift: %v\n", err)
			return 1
		}
	}
	if repoRaw == "" || !gitx.IsRepo(repoAbs) {
		fmt.Fprintln(stderr, "nightshift: set 'repo:' in the draft to a git checkout first")
		return 1
	}

	claudeBin := resolveSetting(h, "NIGHTSHIFT_HOST_CLAUDE_BIN", "claude")
	if !isTerminal() {
		fmt.Fprintln(stderr, "nightshift: refine needs an interactive terminal and claude on PATH")
		return 1
	}
	if _, err := exec.LookPath(claudeBin); err != nil {
		fmt.Fprintln(stderr, "nightshift: refine needs an interactive terminal and claude on PATH")
		return 1
	}

	draftAbs, err := filepath.Abs(draftPath)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	return launchInterview(h, slug, draftAbs, repoAbs, true, claudeBin, stderr)
}

// parseLintFlags interleaves --no-verify (and, for "ready", --no-lint)
// with the single positional slug/path, the same pattern "new" and "run"
// use so flags can come before or after the positional.
func parseLintFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			return nil, err
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}
	return positional, nil
}

// runLint resolves home's sbx bin and runs runner.Lint, writing its
// checks to stdout. newBaseVerifier, when set by a test, supplies the
// BaseVerifier instead of letting Lint build the real sbx one.
func runLint(ctx context.Context, h, specArg string, noVerify bool, stdout io.Writer) (runner.LintResult, error) {
	bin := resolveSbxBin(h)
	opts := runner.LintOptions{
		Home:     h,
		SpecArg:  specArg,
		NoVerify: noVerify,
		SbxBin:   bin,
		Stdout:   stdout,
		Ctx:      ctx,
	}
	if newBaseVerifier != nil {
		opts.Verifier = newBaseVerifier(bin)
	}
	return runner.Lint(opts)
}

func cmdLint(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightshift lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noVerify := fs.Bool("no-verify", false, "")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift lint <spec> [--no-verify]")
	}

	positional, err := parseLintFlags(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fs.Usage()
		return 2
	}

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	ctx, stop := newInterruptContext()
	defer stop()

	res, err := runLint(ctx, h, positional[0], *noVerify, stdout)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return 1
	}
	if res.Fails > 0 {
		return 1
	}
	return 0
}

func cmdReady(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightshift ready", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noVerify := fs.Bool("no-verify", false, "")
	noLint := fs.Bool("no-lint", false, "")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift ready <slug> [--no-verify] [--no-lint]")
	}

	positional, err := parseLintFlags(fs, args)
	if err != nil {
		return 2
	}
	if len(positional) != 1 {
		fs.Usage()
		return 2
	}
	slug := positional[0]

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	if !*noLint {
		ctx, stop := newInterruptContext()
		defer stop()

		res, err := runLint(ctx, h, slug, *noVerify, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "nightshift: %v\n", err)
			return 1
		}
		if res.Fails > 0 {
			fmt.Fprintf(stderr, "nightshift: %s failed lint — not moved to specs/ready/\n", slug)
			return 1
		}
	}

	if err := home.Ready(h, slug, stdout); err != nil {
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
		Version:       version,
	}
	if !*next {
		opts.SpecArg = positional[0]
	}

	status, err := runFn(opts)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift: %v\n", err)
		return runner.ExitCode(runner.StatusError)
	}
	return runner.ExitCode(status)
}
