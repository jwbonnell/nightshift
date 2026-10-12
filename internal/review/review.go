package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/usagelimit"
)

// Outcome statuses a single change's review can land in.
const (
	statusReviewed    = "reviewed"
	statusSizecap     = "sizecap"
	statusSkipped     = "skipped"
	statusInterrupted = "interrupted"
	statusFailed      = "failed"
	statusRateLimited = "rate-limited"
	// statusFatal marks a run-ending failure (P12.3's egress policy
	// error) rather than a per-change one: the caller must not print
	// out.line() for it and must abort the whole run.
	statusFatal = "fatal"
)

// reviewOutcome is the result of reviewing (or skipping, or failing to
// review) one change.
type reviewOutcome struct {
	label       string
	status      string
	verdict     string
	cost        float64
	costKnown   bool
	reason      string
	until       string // statusRateLimited: "Mon 15:04", local time
	err         error
	execStarted bool // Sandbox.Exec was actually called (for --limit's counting, P12.4)
}

func (o reviewOutcome) failed() bool      { return o.status == statusFailed }
func (o reviewOutcome) interrupted() bool { return o.status == statusInterrupted }
func (o reviewOutcome) rateLimited() bool { return o.status == statusRateLimited }
func (o reviewOutcome) isFatal() bool     { return o.status == statusFatal }

// line formats o for the per-change stdout line a real run prints.
func (o reviewOutcome) line() string {
	switch o.status {
	case statusReviewed:
		if o.costKnown {
			return fmt.Sprintf("%s  %s ($%.2f)", o.label, o.verdict, o.cost)
		}
		return fmt.Sprintf("%s  %s (cost n/a)", o.label, o.verdict)
	case statusSizecap:
		return fmt.Sprintf("%s  SKIPPED (size)", o.label)
	case statusSkipped:
		return fmt.Sprintf("%s  skipped (%s)", o.label, o.reason)
	case statusInterrupted:
		return fmt.Sprintf("%s  interrupted", o.label)
	case statusRateLimited:
		return fmt.Sprintf("%s  rate-limited (until %s)", o.label, o.until)
	case statusFailed:
		return fmt.Sprintf("%s  FAILED: %v", o.label, o.err)
	default:
		return o.label
	}
}

// egressError marks ensureSandbox's AllowNetwork failure (P12.3): fatal to
// the whole run, not just the change being reviewed.
type egressError struct {
	host string
	err  error
}

func (e *egressError) Error() string { return fmt.Sprintf("allow network %s: %v", e.host, e.err) }
func (e *egressError) Unwrap() error { return e.err }

// formatThousands renders n with a comma every three digits from the
// right (4812 -> "4,812"), preserving a leading "-".
func formatThousands(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

// runner bundles the mutable state shared across every change reviewed in
// one Run: the single sandbox (recreated only after a timed-out Exec) and
// the per-forge gitEnv built once from Options.
type runner struct {
	opts *Options

	gitEnvs map[forge.Kind]gitEnv

	sandboxCreated bool
	egressAllowed  bool
}

func newRunner(opts *Options) *runner {
	envs := make(map[forge.Kind]gitEnv, 2)
	if c, ok := opts.Clients[forge.KindGitLab]; ok {
		envs[forge.KindGitLab] = buildGitEnv(c, opts.Settings.GitLabURL, opts.Settings.GitLabToken, opts.AllowFileURL, opts.gitRecord)
	}
	if c, ok := opts.Clients[forge.KindGitHub]; ok {
		envs[forge.KindGitHub] = buildGitEnv(c, opts.Settings.GitHubURL, opts.Settings.GitHubToken, opts.AllowFileURL, opts.gitRecord)
	}
	return &runner{opts: opts, gitEnvs: envs}
}

// buildGitEnv derives one forge's gitEnv from its client and configured
// base URL: the URL's scheme (falling back to https), the client's own
// web host/auth-user/slug conventions, and the resolved token.
func buildGitEnv(client forge.Client, rawURL, token string, allowFileURL bool, record func([]string)) gitEnv {
	scheme := "https"
	if i := strings.Index(rawURL, "://"); i >= 0 {
		scheme = rawURL[:i]
	}
	return gitEnv{
		scheme:       scheme,
		webHost:      client.WebHost(),
		urlSetting:   client.Kind().URLSetting(),
		authUser:     client.GitAuthUser(),
		token:        token,
		allowFileURL: allowFileURL,
		record:       record,
	}
}

// failOutcome turns err into either an interrupted outcome (ctx already
// cancelled — nothing logged, so the change is retried next run) or a
// failed one (logged to failures.log).
func (r *runner) failOutcome(ctx context.Context, label, slug string, err error) reviewOutcome {
	if ctx.Err() != nil {
		return reviewOutcome{label: label, status: statusInterrupted}
	}
	appendFailure(r.opts.Settings.Home, r.opts.Now(), slug, err, r.opts.Stderr)
	return reviewOutcome{label: label, status: statusFailed, err: err}
}

// finishNote writes d's note and, best-effort, commits/mirrors it (P6).
func (r *runner) finishNote(d noteData) error {
	notePath, content, err := writeNote(r.opts.Settings.Home, d, r.opts.Now())
	if err != nil {
		return err
	}
	commitNote(r.opts.Settings.Home, notePath, d.Label, d.Verdict, r.opts.Stderr)
	mirrorNote(r.opts.Settings.Mirror, d.Slug, content, d.Label, d.Verdict, r.opts.Stderr)
	return nil
}

// ensureSandbox creates the shared review sandbox on first need (or again
// after a timeout-Stop) and allows every configured egress host, also on
// first need. An AllowNetwork failure is wrapped in *egressError, which
// callers must treat as fatal to the whole run (P12.3), not just this
// change.
func (r *runner) ensureSandbox(ctx context.Context) error {
	env := scrubEnv(os.Environ(), r.opts.Settings.GitLabToken, r.opts.Settings.GitHubToken)

	if !r.sandboxCreated {
		workDir := filepath.Join(home.PRDir(r.opts.Settings.Home), "work")
		if err := os.MkdirAll(workDir, 0o700); err != nil {
			return fmt.Errorf("create work dir: %w", err)
		}
		if err := r.opts.Sandbox.Create(ctx, workDir, r.opts.Agent.SbxAgent(), env); err != nil {
			return fmt.Errorf("sandbox create: %w", err)
		}
		r.sandboxCreated = true
		r.egressAllowed = false
	}

	if !r.egressAllowed {
		for _, hostEntry := range r.opts.Settings.Egress {
			if err := r.opts.Sandbox.AllowNetwork(ctx, hostEntry, env); err != nil {
				return &egressError{host: hostEntry, err: err}
			}
		}
		r.egressAllowed = true
	}
	return nil
}

// rateLimitedOutcome records res's rate-limit (P12.4) in the shared usage-
// limit marker and reports the reset time a caller should show.
func (r *runner) rateLimitedOutcome(label string, res agent.Result) reviewOutcome {
	now := r.opts.Now()
	runsDir := home.RunsDir(r.opts.Settings.Home)
	hit := usagelimit.Parse(res.Message, now)
	_ = usagelimit.Write(runsDir, hit, now, "pr", r.opts.Settings.LimitFallback)

	until := now.Add(r.opts.Settings.LimitFallback)
	if active := usagelimit.ReadActive(runsDir, now, false); active != nil {
		if t, err := time.Parse(time.RFC3339, active.ResetAt); err == nil {
			until = t
		}
	}
	return reviewOutcome{label: label, status: statusRateLimited, until: until.Local().Format("Mon 15:04"), execStarted: true}
}

// reviewTarget resolves an explicit Target through its forge client (Get
// then GetRepo, on the typed repo string) and runs reviewOne on the
// canonical repo path, per the reference's reviewTarget (docs/reference
// §"Canonical project path").
func (r *runner) reviewTarget(ctx context.Context, t Target) reviewOutcome {
	client := r.opts.Clients[t.Forge]
	typedLabel := client.Label(t.Repo, t.Number)
	suffix := client.SlugSuffix()

	if ctx.Err() != nil {
		return reviewOutcome{label: typedLabel, status: statusInterrupted}
	}

	change, err := client.Get(ctx, t.Repo, t.Number)
	if err != nil {
		return r.failOutcome(ctx, typedLabel, Slug(t.Repo, t.Number, suffix), fmt.Errorf("get %s: %w", t.Forge.Noun(), err))
	}
	repo, err := client.GetRepo(ctx, t.Repo)
	if err != nil {
		return r.failOutcome(ctx, typedLabel, Slug(t.Repo, t.Number, suffix), fmt.Errorf("get %s: %w", t.Forge.RepoNoun(), err))
	}

	canonical := repo.Path
	if canonical == "" {
		canonical = t.Repo
	}
	return r.reviewOne(ctx, t.Forge, canonical, t.Number, change, repo, false)
}

// reviewOne reviews one already-resolved change end to end: skip rules,
// clone/fetch/export, the size cap, the sandboxed reviewer (with its
// rate-limit/transient/timeout handling, P12.4), and the note it writes.
// pending is true only when called from runPending (P1's draft-skip rule
// is pending-only).
func (r *runner) reviewOne(ctx context.Context, kind forge.Kind, repoPath string, number int, change forge.Change, repo forge.Repo, pending bool) reviewOutcome {
	client := r.opts.Clients[kind]
	label := client.Label(repoPath, number)
	suffix := client.SlugSuffix()

	if ctx.Err() != nil {
		return reviewOutcome{label: label, status: statusInterrupted}
	}

	homeDir := r.opts.Settings.Home

	slug, _, reason, err := preCloneSkip(homeDir, pending, r.opts.Force, r.opts.IncludeDrafts, preClone{
		Kind: kind, Repo: repoPath, RepoID: repo.ID, Number: number,
		Draft: change.Draft, State: change.State, SHA: change.HeadSHA, Suffix: suffix,
	})
	if err != nil {
		return r.failOutcome(ctx, label, Slug(repoPath, number, suffix), err)
	}
	if reason != "" {
		return reviewOutcome{label: label, status: statusSkipped, reason: reason}
	}

	workDir := filepath.Join(home.PRDir(homeDir), "work", slug)
	treeDir := filepath.Join(workDir, "tree")
	defer os.RemoveAll(workDir)

	g := r.gitEnvs[kind]
	reposDir := filepath.Join(home.PRDir(homeDir), "repos")

	repoClone, err := ensureClone(ctx, g, reposDir, kind, repo.ID, repo.CloneURL)
	if err != nil {
		return r.failOutcome(ctx, label, slug, fmt.Errorf("clone: %w", err))
	}
	if err := fetchChange(ctx, g, repoClone, client.HeadRefspec(number), change.TargetBranch, number); err != nil {
		return r.failOutcome(ctx, label, slug, fmt.Errorf("fetch: %w", err))
	}
	head, err := resolveHead(ctx, g, repoClone, change.HeadSHA, number)
	if err != nil {
		return r.failOutcome(ctx, label, slug, err)
	}
	base, err := resolveBase(ctx, g, repoClone, change.DiffBaseSHA, change.TargetBranch, head)
	if err != nil {
		return r.failOutcome(ctx, label, slug, err)
	}
	if err := exportTree(ctx, g, repoClone, head, treeDir); err != nil {
		return r.failOutcome(ctx, label, slug, fmt.Errorf("export tree: %w", err))
	}
	inputs, err := prepareReviewDir(ctx, g, repoClone, base, head, treeDir)
	if err != nil {
		return r.failOutcome(ctx, label, slug, fmt.Errorf("prepare review dir: %w", err))
	}

	if inputs.ChangedLines > r.opts.Settings.MaxLines && !r.opts.Force {
		body := fmt.Sprintf("Skipped: %s changed lines exceeds the %s-line cap (re-run with --force).",
			formatThousands(inputs.ChangedLines), formatThousands(r.opts.Settings.MaxLines))
		d := noteData{
			Kind: kind, Slug: slug, Label: label, Project: repoPath, Number: number,
			Title: change.Title, Author: change.Author, URL: change.WebURL,
			Head: head, Base: base, Verdict: "SKIPPED", Lines: inputs.ChangedLines,
			CostUSD: 0, CostKnown: true,
			Model: r.opts.Settings.Selector.Display(), Body: body,
		}
		if err := r.finishNote(d); err != nil {
			return r.failOutcome(ctx, label, slug, fmt.Errorf("write note: %w", err))
		}
		return reviewOutcome{label: label, status: statusSizecap}
	}

	if err := r.ensureSandbox(ctx); err != nil {
		var ee *egressError
		if errors.As(err, &ee) {
			fmt.Fprintf(r.opts.Stderr, "nightshift pr: %v\n", err)
			return reviewOutcome{label: label, status: statusFatal, err: err}
		}
		return r.failOutcome(ctx, label, slug, err)
	}

	promptText, _, err := LoadPrompt(r.opts.Settings.PromptFile, homeDir)
	if err != nil {
		return r.failOutcome(ctx, label, slug, err)
	}
	prompt := buildPrompt(promptText, kind, repoPath, label, change.Title, change.Author, change.TargetBranch, base, head, inputs, change.Description)

	logW := agentLogWriter(homeDir, label, r.opts.Now())
	defer logW.Close()

	env := scrubEnv(os.Environ(), r.opts.Settings.GitLabToken, r.opts.Settings.GitHubToken)
	ag := r.opts.Agent
	argv := ag.Argv(agent.Invocation{
		Role: agent.RoleReview, Model: r.opts.Settings.Selector.Model,
		ReadOnly: true, MaxTurns: r.opts.Settings.MaxTurns, Hardened: true,
	})

	retried := false
	var res agent.Result
	var execErr error
	for {
		execCtx, cancel := context.WithTimeout(ctx, r.opts.Settings.Timeout)
		var stdout string
		stdout, execErr = r.opts.Sandbox.Exec(execCtx, treeDir, argv, prompt, env, logW)
		cancel()

		if errors.Is(execErr, context.DeadlineExceeded) && ctx.Err() == nil {
			if serr := r.opts.Sandbox.Stop(env); serr != nil {
				fmt.Fprintf(r.opts.Stderr, "nightshift pr: stop timed-out sandbox: %v\n", serr)
			}
			r.sandboxCreated = false
			r.egressAllowed = false
			out := r.failOutcome(ctx, label, slug, fmt.Errorf("%s exec timed out after %s — sandbox stopped; see logs/agent.log", ag.Name(), r.opts.Settings.Timeout))
			out.execStarted = true
			return out
		}

		res = ag.Parse(stdout)

		if ctx.Err() != nil {
			return reviewOutcome{label: label, status: statusInterrupted, execStarted: true}
		}
		if res.Found && ag.RateLimited(res) {
			return r.rateLimitedOutcome(label, res)
		}
		if res.Found && ag.Transient(res) && !retried {
			retried = true
			if serr := r.opts.Sleep(ctx, time.Minute); serr != nil && ctx.Err() != nil {
				return reviewOutcome{label: label, status: statusInterrupted, execStarted: true}
			}
			continue
		}
		break
	}

	var finalErr error
	switch {
	case execErr != nil:
		finalErr = fmt.Errorf("%s exec: %w — see logs/agent.log", ag.Name(), execErr)
	case !res.Found:
		finalErr = fmt.Errorf("parse %s output: no result found — see logs/agent.log", ag.Name())
	case res.IsError:
		msg := fmt.Sprintf("%s reported an error: %s", ag.Name(), res.Message)
		if res.Subtype != "" {
			msg += fmt.Sprintf(" (subtype %q)", res.Subtype)
		}
		finalErr = fmt.Errorf("%s — see logs/agent.log", msg)
	case strings.TrimSpace(res.Text) == "":
		finalErr = fmt.Errorf("%s returned an empty result — see logs/agent.log", ag.Name())
	}
	if finalErr != nil {
		out := r.failOutcome(ctx, label, slug, finalErr)
		out.execStarted = true
		return out
	}

	body := stripCheckboxes(res.Text)
	verdict := parseVerdict(body)

	d := noteData{
		Kind: kind, Slug: slug, Label: label, Project: repoPath, Number: number,
		Title: change.Title, Author: change.Author, URL: change.WebURL,
		Head: head, Base: base, Verdict: verdict, Lines: inputs.ChangedLines,
		CostUSD: res.CostUSD, CostKnown: res.CostKnown,
		Model: r.opts.Settings.Selector.Display(), Body: body,
	}
	if err := r.finishNote(d); err != nil {
		out := r.failOutcome(ctx, label, slug, fmt.Errorf("write note: %w", err))
		out.execStarted = true
		return out
	}

	return reviewOutcome{label: label, status: statusReviewed, verdict: verdict, cost: res.CostUSD, costKnown: res.CostKnown, execStarted: true}
}
