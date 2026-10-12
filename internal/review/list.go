package review

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/home"
)

// runList is --list's entry point (P4/P8.1): read-only forge GETs only, no
// clone/sandbox/lock/state-dir/writes of any kind.
func runList(ctx context.Context, opts *Options) int {
	if opts.Pending {
		return runListPending(ctx, opts)
	}
	return runListTargets(ctx, opts)
}

// listTally accumulates --list's would-review/queued/skipped counts.
type listTally struct {
	wouldReview, queued, skipped int
}

// classify decides one change's --list line, per the reference's
// listDecision: a non-empty skip reason always wins; otherwise a run under
// --limit gets queued once wouldReview has already reached the limit.
func (t *listTally) classify(reason string, limit int) string {
	if reason != "" {
		t.skipped++
		return fmt.Sprintf("skip (%s)", reason)
	}
	if limit > 0 && t.wouldReview >= limit {
		t.queued++
		return "queued (over --limit)"
	}
	t.wouldReview++
	return "would review"
}

// printListFooter prints --list's trailing note and tally line, in that
// order, for both the explicit-target and --pending paths.
func printListFooter(w io.Writer, maxLines int, t listTally) {
	fmt.Fprintf(w, "note: changes over the %s-line size cap can't be detected without cloning; a real run writes them as SKIPPED\n", formatThousands(maxLines))
	fmt.Fprintf(w, "would review: %d · queued: %d · skipped: %d\n", t.wouldReview, t.queued, t.skipped)
}

// runListTargets lists every explicit target, resolving each through its
// client exactly as a real run would (reviewTarget), but never cloning,
// starting a sandbox, or writing anything.
func runListTargets(ctx context.Context, opts *Options) int {
	var tally listTally

	for _, t := range opts.Targets {
		client := opts.Clients[t.Forge]
		typedLabel := client.Label(t.Repo, t.Number)

		change, err := client.Get(ctx, t.Repo, t.Number)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%s  FAILED: get %s: %v\n", typedLabel, t.Forge.Noun(), err)
			fmt.Fprintf(opts.Stderr, "nightshift pr: list: get %s: %v — check the token/VPN\n", t.Forge.Noun(), err)
			return 1
		}
		repo, err := client.GetRepo(ctx, t.Repo)
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%s  FAILED: get %s: %v\n", typedLabel, t.Forge.RepoNoun(), err)
			fmt.Fprintf(opts.Stderr, "nightshift pr: list: get %s: %v — check the token/VPN\n", t.Forge.RepoNoun(), err)
			return 1
		}

		canonical := repo.Path
		if canonical == "" {
			canonical = t.Repo
		}
		label := client.Label(canonical, t.Number)

		_, _, reason, err := preCloneSkip(opts.Settings.Home, false, opts.Force, opts.IncludeDrafts, preClone{
			Kind: t.Forge, Repo: canonical, RepoID: repo.ID, Number: t.Number,
			Draft: change.Draft, State: change.State, SHA: change.HeadSHA, Suffix: client.SlugSuffix(),
		})
		if err != nil {
			fmt.Fprintf(opts.Stdout, "%s  FAILED: %v\n", label, err)
			return 1
		}

		decision := tally.classify(reason, opts.Limit)
		fmt.Fprintf(opts.Stdout, "%s  %s  %s\n", label, decision, change.Title)
	}

	printListFooter(opts.Stdout, opts.Settings.MaxLines, tally)
	return 0
}

// runListPending lists every change awaiting the user's review, across
// every configured forge, per P4/P8.1: GetRepo is cached per distinct
// RepoRef (not once per listed change), and a GitHub listing with no
// HeadSHA gets one Get call so the already-reviewed check is exact.
func runListPending(ctx context.Context, opts *Options) int {
	var tally listTally
	repoCache := make(map[string]forge.Repo)

	for _, kind := range []forge.Kind{forge.KindGitLab, forge.KindGitHub} {
		if !opts.Settings.Configured(kind) {
			continue
		}
		client := opts.Clients[kind]

		user, err := client.CurrentUser(ctx)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "nightshift pr: %s: resolve current user: %v — check the token/VPN\n", kind, err)
			return 1
		}
		listed, err := client.ListReviewRequests(ctx, user)
		if err != nil {
			fmt.Fprintf(opts.Stderr, "nightshift pr: %s: list pending: %v — check the token/VPN\n", kind, err)
			return 1
		}

		for _, l := range listed {
			typedLabel := client.Label(l.Label, l.Number)

			cacheKey := string(kind) + ":" + l.RepoRef
			repo, ok := repoCache[cacheKey]
			if !ok {
				repo, err = client.GetRepo(ctx, l.RepoRef)
				if err != nil {
					fmt.Fprintf(opts.Stdout, "%s  FAILED: get %s: %v\n", typedLabel, kind.RepoNoun(), err)
					fmt.Fprintf(opts.Stderr, "nightshift pr: list: get %s: %v — check the token/VPN\n", kind.RepoNoun(), err)
					return 1
				}
				repoCache[cacheKey] = repo
			}

			canonical := repo.Path
			if canonical == "" {
				canonical = l.RepoRef
			}
			label := client.Label(canonical, l.Number)

			sha := l.HeadSHA
			title := l.Title
			if sha == "" {
				change, err := client.Get(ctx, l.RepoRef, l.Number)
				if err != nil {
					fmt.Fprintf(opts.Stdout, "%s  FAILED: get %s: %v\n", label, kind.Noun(), err)
					fmt.Fprintf(opts.Stderr, "nightshift pr: list: get %s: %v — check the token/VPN\n", kind.Noun(), err)
					return 1
				}
				sha = change.HeadSHA
				title = change.Title
			}

			_, _, reason, err := preCloneSkip(opts.Settings.Home, true, opts.Force, opts.IncludeDrafts, preClone{
				Kind: kind, Repo: canonical, RepoID: repo.ID, Number: l.Number,
				Draft: l.Draft, State: l.State, SHA: sha, Suffix: client.SlugSuffix(),
			})
			if err != nil {
				fmt.Fprintf(opts.Stdout, "%s  FAILED: %v\n", label, err)
				return 1
			}

			decision := tally.classify(reason, opts.Limit)
			fmt.Fprintf(opts.Stdout, "%s  %s  %s\n", label, decision, title)
		}
	}

	printListFooter(opts.Stdout, opts.Settings.MaxLines, tally)
	return 0
}

// tokenState renders a token's presence for printPlan without ever
// printing the token itself.
func tokenState(tok string) string {
	if tok == "" {
		return "unset"
	}
	return "set"
}

// printPlan is --dry-run's output (P4): resolved config, the targets that
// would be reviewed, and the exact sbx commands a real run would issue.
// It makes no API calls, clones nothing, and starts no sandbox.
func printPlan(w io.Writer, opts *Options) {
	s := opts.Settings

	fmt.Fprintln(w, "nightshift pr --dry-run: resolved config")
	fmt.Fprintf(w, "  %-14s%s\n", "home:", s.Home)
	fmt.Fprintf(w, "  %-14s%s\n", "reviews:", home.ReviewsDir(s.Home))

	mirror := s.Mirror
	if mirror == "" {
		mirror = "off"
	}
	fmt.Fprintf(w, "  %-14s%s\n", "mirror:", mirror)

	gitlabURL := s.GitLabURL
	if gitlabURL == "" {
		gitlabURL = "unset"
	}
	fmt.Fprintf(w, "  %-14s%s (token %s)\n", "gitlab:", gitlabURL, tokenState(s.GitLabToken))
	fmt.Fprintf(w, "  %-14s%s (token %s)\n", "github:", s.GitHubURL, tokenState(s.GitHubToken))
	fmt.Fprintf(w, "  %-14s%s\n", "state dir:", home.PRDir(s.Home))
	fmt.Fprintf(w, "  %-14s%s\n", "model:", s.Selector.String())

	egress := "none"
	if len(s.Egress) > 0 {
		egress = strings.Join(s.Egress, ", ")
	}
	fmt.Fprintf(w, "  %-14s%s\n", "egress:", egress)

	maxTurns := strconv.Itoa(s.MaxTurns)
	if s.Selector.Agent == "opencode" {
		maxTurns = "n/a (opencode)"
	}
	fmt.Fprintf(w, "  %-14s%s\n", "max turns:", maxTurns)
	fmt.Fprintf(w, "  %-14s%s\n", "timeout:", s.Timeout.String())
	fmt.Fprintf(w, "  %-14s%d\n", "max lines:", s.MaxLines)
	fmt.Fprintf(w, "  %-14s%s\n", "sbx bin:", s.SbxBin)

	_, source, err := LoadPrompt(s.PromptFile, s.Home)
	promptDisplay := source
	if err != nil {
		promptDisplay = fmt.Sprintf("error: %v", err)
	}
	fmt.Fprintf(w, "  %-14s%s\n", "prompt:", promptDisplay)

	fmt.Fprintln(w)
	if opts.Pending {
		fmt.Fprintln(w, "targets: --pending (every open PR/MR awaiting your review on every configured forge — not listed here; --dry-run makes no API calls)")
	} else {
		fmt.Fprintln(w, "targets:")
		for _, t := range opts.Targets {
			fmt.Fprintf(w, "  %s\n", t.Forge.Label(t.Repo, t.Number))
		}
	}

	sandboxName := "nightshift-pr-" + opts.RunID
	workDir := filepath.Join(home.PRDir(s.Home), "work")

	fmt.Fprintln(w)
	fmt.Fprintln(w, "commands that would run:")
	fmt.Fprintf(w, "  %s create --name %s %s %s\n", s.SbxBin, sandboxName, opts.Agent.SbxAgent(), workDir)
	for _, h := range s.Egress {
		fmt.Fprintf(w, "  %s policy allow network --sandbox %s %s\n", s.SbxBin, sandboxName, h)
	}

	slug := "<slug>"
	if !opts.Pending && len(opts.Targets) == 1 {
		t := opts.Targets[0]
		slug = Slug(t.Repo, t.Number, t.Forge.SlugSuffix())
	}
	treeDir := filepath.Join(workDir, slug, "tree")
	argv := opts.Agent.Argv(agent.Invocation{
		Role: agent.RoleReview, Model: s.Selector.Model, ReadOnly: true, MaxTurns: s.MaxTurns, Hardened: true,
	})
	fmt.Fprintf(w, "  %s exec -w %s %s %s\n", s.SbxBin, treeDir, sandboxName, strings.Join(argv, " "))
}
