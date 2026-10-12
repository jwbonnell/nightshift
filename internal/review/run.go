package review

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/forge/github"
	"github.com/jwbonnell/nightshift/internal/forge/gitlab"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/notify"
	"github.com/jwbonnell/nightshift/internal/usagelimit"
)

// Options is everything Run needs for one invocation of nightshift pr,
// explicit targets or --pending. A nil field in most cases means "use the
// real default" (see fill()); AllowFileURL and gitRecord exist for tests
// only.
type Options struct {
	Settings Settings
	Targets  []Target

	Pending, Force, IncludeDrafts bool
	DryRun, List                  bool
	Limit                         int

	RunID string
	Ctx   context.Context

	Sandbox  Sandbox
	Clients  map[forge.Kind]forge.Client
	Agent    agent.Agent
	Notifier notify.Notifier

	AllowFileURL bool
	Now          func() time.Time
	Stdout       io.Writer
	Stderr       io.Writer
	Sleep        func(ctx context.Context, d time.Duration) error

	gitRecord func(argv []string)
}

// fill applies Options' defaults, in the same order the fields are
// declared.
func (o *Options) fill() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
	if o.Ctx == nil {
		o.Ctx = context.Background()
	}
	if o.RunID == "" {
		o.RunID = strconv.FormatInt(o.Now().Unix(), 10)
	}
	if o.Sandbox == nil {
		o.Sandbox = newSbxSandbox(o.Settings.SbxBin, o.RunID)
	}
	if o.Clients == nil {
		o.Clients = map[forge.Kind]forge.Client{
			forge.KindGitLab: gitlab.New(o.Settings.GitLabURL, o.Settings.GitLabToken),
			forge.KindGitHub: github.New(o.Settings.GitHubURL, o.Settings.GitHubToken),
		}
	}
	if o.Agent == nil {
		if ag, err := agent.New(o.Settings.Selector, agent.Settings{
			ClaudeBin: o.Settings.ClaudeBin, OpencodeBin: o.Settings.OpencodeBin, OpencodeConfig: o.Settings.OpencodeConfig,
		}); err == nil {
			o.Agent = ag
		}
	}
	if o.Notifier == nil {
		if o.Settings.Notify == "off" {
			o.Notifier = notify.Noop{}
		} else {
			o.Notifier = notify.Desktop{}
		}
	}
	if o.Sleep == nil {
		o.Sleep = defaultSleep
	}
}

// defaultSleep is Options.Sleep's real implementation: wait d, but return
// ctx.Err() early if ctx is cancelled first.
func defaultSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run executes one nightshift pr invocation and returns its exit code,
// per the Run-order in SPEC.md's P12 ("Options / Run").
func Run(opts Options) int {
	opts.fill()

	if opts.List && opts.DryRun {
		fmt.Fprintln(opts.Stderr, "nightshift pr: use --list or --dry-run, not both (--dry-run makes no API calls; --list makes read-only forge requests)")
		return 2
	}
	if opts.Limit < 0 {
		fmt.Fprintln(opts.Stderr, "nightshift pr: --limit must be 0 (no limit) or a positive number of reviews")
		return 2
	}
	if opts.Limit > 0 && !opts.Pending {
		fmt.Fprintln(opts.Stderr, "nightshift pr: --limit only applies to --pending")
		return 2
	}

	if !opts.Settings.Configured(forge.KindGitLab) && !opts.Settings.Configured(forge.KindGitHub) {
		if opts.Settings.GitLabTokenErr != nil {
			fmt.Fprintf(opts.Stderr, "nightshift pr: %v\n", opts.Settings.GitLabTokenErr)
		}
		if opts.Settings.GitHubTokenErr != nil {
			fmt.Fprintf(opts.Stderr, "nightshift pr: %v\n", opts.Settings.GitHubTokenErr)
		}
		fmt.Fprintln(opts.Stderr, "nightshift pr: no forge configured — set NIGHTSHIFT_GITLAB_URL + NIGHTSHIFT_GITLAB_TOKEN_FILE and/or NIGHTSHIFT_GITHUB_TOKEN_FILE")
		return 2
	}

	if opts.DryRun {
		printPlan(opts.Stdout, &opts)
		return 0
	}
	if opts.List {
		return runList(opts.Ctx, &opts)
	}

	// P12.4: the usage-limit marker is claude-only — an opencode reviewer
	// never checks it.
	if opts.Settings.Selector.Agent == "claude" {
		now := opts.Now()
		if active := usagelimit.ReadActive(home.RunsDir(opts.Settings.Home), now, true); active != nil {
			resetAt, _ := time.Parse(time.RFC3339, active.ResetAt)
			fmt.Fprintf(opts.Stderr, "nightshift pr: usage limit active until %s — skipping\n", resetAt.Local().Format("Mon 15:04"))
			return 0
		}
	}

	if !opts.Sandbox.Available() {
		fmt.Fprintln(opts.Stderr, "nightshift pr: Docker Sandboxes (sbx) is not available — install it and run `sbx setup`; nightshift pr never runs the reviewer on the host")
		return 2
	}

	if err := ensureState(opts.Settings.Home); err != nil {
		fmt.Fprintf(opts.Stderr, "nightshift pr: %v\n", err)
		return 2
	}

	lk, err := acquirePRLock(opts.Settings.Home)
	if err != nil {
		fmt.Fprintf(opts.Stderr, "nightshift pr: %v\n", err)
		return 2
	}
	defer lk.Release()

	if err := clearWork(opts.Settings.Home); err != nil {
		fmt.Fprintf(opts.Stderr, "nightshift pr: %v\n", err)
		return 2
	}

	r := newRunner(&opts)
	defer func() {
		env := scrubEnv(os.Environ(), opts.Settings.GitLabToken, opts.Settings.GitHubToken)
		if err := opts.Sandbox.Remove(env); err != nil {
			fmt.Fprintf(opts.Stderr, "nightshift pr: sandbox remove: %v\n", err)
		}
	}()

	if opts.Pending {
		return runPendingTop(r, &opts)
	}
	return runTargetsTop(r, &opts)
}

// runTargetsTop drives the explicit-target loop (no summary line is ever
// printed for this path — only --pending gets one).
func runTargetsTop(r *runner, opts *Options) int {
	if len(opts.Targets) > 0 {
		if err := r.ensureEgressUpFront(opts.Ctx); err != nil {
			fmt.Fprintf(opts.Stderr, "nightshift pr: %v\n", err)
			return 1
		}
	}

	failed := 0
	notReviewed := 0
	interrupted := false

	for i, t := range opts.Targets {
		if opts.Ctx.Err() != nil {
			interrupted = true
			notReviewed = len(opts.Targets) - i
			break
		}

		out := r.reviewTarget(opts.Ctx, t)
		if out.isFatal() {
			return 1
		}
		fmt.Fprintln(opts.Stdout, out.line())

		if out.interrupted() {
			interrupted = true
			notReviewed = len(opts.Targets) - i
			break
		}
		if out.rateLimited() {
			break
		}
		if out.failed() {
			failed++
		}
	}

	if interrupted {
		fmt.Fprintf(opts.Stderr, "nightshift pr: interrupted — %d change(s) not reviewed (re-run to review them)\n", notReviewed)
		return 130
	}
	if failed > 0 {
		return 1
	}
	return 0
}

// runTally accumulates a --pending run's outcome counts, per P8.3/P12.4.
type runTally struct {
	reviewed, skipped, failed, blocking int
	interrupted                         bool
	notReviewed                         int
	limitReached                        bool
	left                                int
	rateLimitedUntil                    string
	fatal                               bool
}

// runPendingTop runs runPending and maps its tally onto an exit code and
// the P7 notification.
func runPendingTop(r *runner, opts *Options) int {
	tally := r.runPending(opts.Ctx)
	if tally.fatal {
		return 1
	}

	if !tally.interrupted && tally.reviewed+tally.failed > 0 {
		r.notifyPending(tally)
	}

	if tally.interrupted {
		fmt.Fprintf(opts.Stderr, "nightshift pr: interrupted — %d change(s) not reviewed (re-run to review them)\n", tally.notReviewed)
		return 130
	}
	if tally.failed > 0 {
		return 1
	}
	return 0
}

// pendingItem is one entry in --pending's combined, cross-forge queue.
type pendingItem struct {
	client forge.Client
	listed forge.Listed
}

// pendingResult tells runPending's loop what to do after processing one
// item.
type pendingResult int

const (
	pendingContinue pendingResult = iota
	pendingStop
	pendingFatal
)

// runPending reviews every open change where the user is a requested
// reviewer, across every configured forge (GitLab first, then GitHub,
// each in its own oldest-first API order), per P4/P8.3/P8.4/P12.4.
func (r *runner) runPending(ctx context.Context) runTally {
	var tally runTally
	var queue []pendingItem
	anyListOK := false

	for _, kind := range []forge.Kind{forge.KindGitLab, forge.KindGitHub} {
		if !r.opts.Settings.Configured(kind) {
			continue
		}
		client := r.opts.Clients[kind]

		user, err := client.CurrentUser(ctx)
		if err != nil {
			if ctx.Err() != nil {
				tally.interrupted = true
				r.printSummary(tally)
				return tally
			}
			fmt.Fprintf(r.opts.Stderr, "nightshift pr: %s: resolve current user: %v — check the token/VPN\n", kind, err)
			tally.failed++
			continue
		}
		listed, err := client.ListReviewRequests(ctx, user)
		if err != nil {
			if ctx.Err() != nil {
				tally.interrupted = true
				r.printSummary(tally)
				return tally
			}
			fmt.Fprintf(r.opts.Stderr, "nightshift pr: %s: list pending: %v — check the token/VPN\n", kind, err)
			tally.failed++
			continue
		}
		anyListOK = true
		for _, l := range listed {
			queue = append(queue, pendingItem{client: client, listed: l})
		}
	}

	if !anyListOK {
		return tally
	}

	if len(queue) > 0 {
		if err := r.ensureEgressUpFront(ctx); err != nil {
			fmt.Fprintf(r.opts.Stderr, "nightshift pr: %v\n", err)
			tally.fatal = true
			return tally
		}
	}

	started := 0
queueLoop:
	for i, q := range queue {
		if ctx.Err() != nil {
			tally.interrupted = true
			tally.notReviewed = len(queue) - i
			break
		}

		if r.opts.Limit > 0 && started >= r.opts.Limit {
			left := 0
			for _, rest := range queue[i:] {
				if !(rest.listed.Draft && !r.opts.IncludeDrafts) {
					left++
				}
			}
			fmt.Fprintf(r.opts.Stdout, "limit reached: %d change(s) left for the next run\n", left)
			tally.limitReached = true
			tally.left = left
			break
		}

		switch r.processPendingItem(ctx, q, len(queue)-i, &tally, &started) {
		case pendingFatal:
			return tally
		case pendingStop:
			break queueLoop
		}
	}

	r.printSummary(tally)
	return tally
}

// processPendingItem reviews one queued item (or skips/fails it before
// ever reviewing), mutating tally and started as it goes. remaining is
// how many items (including this one) are left in the queue, for the
// interrupted-mid-loop notReviewed count.
func (r *runner) processPendingItem(ctx context.Context, q pendingItem, remaining int, tally *runTally, started *int) pendingResult {
	label := q.client.Label(q.listed.Label, q.listed.Number)

	if q.listed.Draft && !r.opts.IncludeDrafts {
		fmt.Fprintf(r.opts.Stdout, "%s  skipped (draft)\n", label)
		tally.skipped++
		return pendingContinue
	}

	slug := Slug(q.listed.Label, q.listed.Number, q.client.SlugSuffix())

	change, err := q.client.Get(ctx, q.listed.RepoRef, q.listed.Number)
	if err != nil {
		return r.pendingAPIFailure(ctx, label, slug, remaining, err, tally)
	}
	repo, err := q.client.GetRepo(ctx, q.listed.RepoRef)
	if err != nil {
		return r.pendingAPIFailure(ctx, label, slug, remaining, err, tally)
	}

	canonical := repo.Path
	if canonical == "" {
		canonical = q.listed.RepoRef
	}

	out := r.reviewOne(ctx, q.client.Kind(), canonical, q.listed.Number, change, repo, true)
	if out.isFatal() {
		tally.fatal = true
		return pendingFatal
	}
	fmt.Fprintln(r.opts.Stdout, out.line())
	if out.execStarted {
		*started++
	}

	switch {
	case out.interrupted():
		tally.interrupted = true
		tally.notReviewed = remaining
		return pendingStop
	case out.rateLimited():
		tally.rateLimitedUntil = out.until
		return pendingStop
	case out.failed():
		tally.failed++
	case out.status == statusReviewed:
		tally.reviewed++
		if out.verdict == "BLOCKING" {
			tally.blocking++
		}
	default: // sizecap, skipped
		tally.skipped++
	}
	return pendingContinue
}

// pendingAPIFailure handles a Get/GetRepo failure for a listed change
// (P8.4: unlike the reference, this is also logged to failures.log).
func (r *runner) pendingAPIFailure(ctx context.Context, label, slug string, remaining int, err error, tally *runTally) pendingResult {
	if ctx.Err() != nil {
		tally.interrupted = true
		tally.notReviewed = remaining
		return pendingStop
	}
	fmt.Fprintf(r.opts.Stdout, "%s  FAILED: %v\n", label, err)
	appendFailure(r.opts.Settings.Home, r.opts.Now(), slug, err, r.opts.Stderr)
	tally.failed++
	return pendingContinue
}

// summaryLine formats --pending's one-line-per-run summary.
func (r *runner) summaryLine(t runTally) string {
	s := fmt.Sprintf("reviewed: %d · skipped: %d · failed: %d", t.reviewed, t.skipped, t.failed)
	if t.limitReached {
		s += fmt.Sprintf(" · left: %d", t.left)
	}
	if t.interrupted {
		s += " · interrupted: 1"
	}
	if t.rateLimitedUntil != "" {
		s += fmt.Sprintf(" · limited: until %s", t.rateLimitedUntil)
	}
	return s
}

func (r *runner) printSummary(t runTally) {
	fmt.Fprintln(r.opts.Stdout, r.summaryLine(t))
}

// notifyPending sends the P7 desktop notification for a --pending run
// that reviewed or failed at least one change.
func (r *runner) notifyPending(t runTally) {
	msg := r.summaryLine(t)
	if t.blocking > 0 {
		msg += fmt.Sprintf(" · blocking: %d", t.blocking)
	}
	urgent := t.failed > 0 || t.blocking > 0
	r.opts.Notifier.Notify("nightshift pr", msg, urgent)
}
