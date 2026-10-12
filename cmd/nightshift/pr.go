package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/review"
)

// prRunFn is a test seam over review.Run: tests overwrite this to capture
// the Options a run was invoked with without driving a real sandboxed
// review.
var prRunFn = review.Run

// cmdPR implements `nightshift pr`: a sandboxed, read-only pre-review of
// one GitHub pull request or GitLab merge request, or every open one
// where the user is a requested reviewer (SPEC.md P4).
func cmdPR(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("nightshift pr", flag.ContinueOnError)
	fs.SetOutput(stderr)

	pending := fs.Bool("pending", false, "review every open PR/MR where you're a requested reviewer, on every configured forge")
	force := fs.Bool("force", false, "re-review even if this head sha was reviewed / over the size cap / not open")
	includeDrafts := fs.Bool("include-drafts", false, "with --pending, don't skip drafts")
	dryRun := fs.Bool("dry-run", false, "print resolved config and the commands that would run; no clone, no sandbox, no writes")
	list := fs.Bool("list", false, "show what --pending or a target would do, one line per PR/MR; read-only API GETs only, no clone/sandbox/writes")
	limit := fs.Int("limit", 0, "with --pending, stop after N reviews have been sent to the reviewer (0 = no limit)")
	model := fs.String("model", "", "override NIGHTSHIFT_PR_MODEL (an agent selector, e.g. sonnet or opencode/<provider>/<model>)")
	maxTurns := fs.Int("max-turns", 0, "override NIGHTSHIFT_PR_MAX_TURNS")
	timeout := fs.String("timeout", "", "override NIGHTSHIFT_PR_TIMEOUT (Go duration, e.g. 15m)")
	maxLines := fs.Int("max-lines", 0, "override NIGHTSHIFT_PR_MAX_LINES")
	forgeFlag := fs.String("forge", "", "gitlab or github; only needed for <repo> <n> when both are configured")

	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: nightshift pr <pr-or-mr-url> [--force] [--list]")
		fmt.Fprintln(stderr, "       nightshift pr <owner/repo#n | group/project!n> [--force] [--list]")
		fmt.Fprintln(stderr, "       nightshift pr <repo> <n> [--forge gitlab|github] [--force] [--list]")
		fmt.Fprintln(stderr, "       nightshift pr --pending [--force] [--include-drafts] [--list] [--limit N]")
		fmt.Fprintln(stderr, "common flags: --dry-run --model M --max-turns N --timeout D --max-lines N")
		fmt.Fprintln(stderr, "--list makes read-only API GETs only (no clone/sandbox/writes); can't combine with --dry-run")
		fmt.Fprintln(stderr, "--limit N only applies to --pending; stops after N reviews reach Claude (0 = no limit)")
		fs.PrintDefaults()
	}

	// Flags may come before or after positionals, same interleaved-parse
	// loop as "new"/"run"/"lint".
	var positional []string
	rest := args
	for {
		if err := fs.Parse(rest); err != nil {
			if err == flag.ErrHelp {
				return 0
			}
			return 2
		}
		rest = fs.Args()
		if len(rest) == 0 {
			break
		}
		positional = append(positional, rest[0])
		rest = rest[1:]
	}

	h, ok := resolveHome(stderr)
	if !ok {
		return 1
	}

	settings, err := review.ResolveSettings(review.FlagValues{
		Model: *model, MaxTurns: *maxTurns, Timeout: *timeout, MaxLines: *maxLines,
	}, h, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift pr: %v\n", err)
		return 2
	}

	for _, w := range settings.Warnings {
		fmt.Fprintln(stderr, w)
	}

	if !settings.Configured(forge.KindGitLab) && !settings.Configured(forge.KindGitHub) {
		if settings.GitLabTokenErr != nil {
			fmt.Fprintf(stderr, "nightshift pr: %v\n", settings.GitLabTokenErr)
		}
		if settings.GitHubTokenErr != nil {
			fmt.Fprintf(stderr, "nightshift pr: %v\n", settings.GitHubTokenErr)
		}
		fmt.Fprintln(stderr, "nightshift pr: no forge configured — set NIGHTSHIFT_GITLAB_URL + NIGHTSHIFT_GITLAB_TOKEN_FILE and/or NIGHTSHIFT_GITHUB_TOKEN_FILE")
		return 2
	}

	opts := review.Options{
		Settings:      settings,
		Pending:       *pending,
		Force:         *force,
		IncludeDrafts: *includeDrafts,
		DryRun:        *dryRun,
		List:          *list,
		Limit:         *limit,
		Stdout:        stdout,
		Stderr:        stderr,
	}

	if *pending {
		if len(positional) != 0 {
			fs.Usage()
			return 2
		}
		if *forgeFlag != "" {
			fmt.Fprintln(stderr, "nightshift pr: --forge only applies to <repo> <n>")
			return 2
		}
	} else {
		if *includeDrafts {
			fmt.Fprintln(stderr, "nightshift pr: --include-drafts only applies to --pending")
			return 2
		}
		target, err := review.ParseArgs(positional, *forgeFlag, settings)
		if err != nil {
			fmt.Fprintf(stderr, "nightshift pr: %v\n", err)
			fs.Usage()
			return 2
		}
		opts.Targets = []review.Target{target}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	opts.Ctx = ctx

	return prRunFn(opts)
}
