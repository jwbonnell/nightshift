package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/notify"
)

// finalizeSync retries a still-owed copy-back once more before the run
// ends, downgrading status to StatusError (and recording a recovery
// message in r.err) if it fails again.
func (r *run) finalizeSync(status Status) Status {
	if !r.st.SyncOwed {
		return status
	}
	if err := r.syncBranchBack(); err != nil {
		r.logf("final sync to %s failed: %v", r.spec.Repo, err)
		r.err = fmt.Errorf("sync to %s failed after a retry: %w (work is safe in the clone at %s; recover with: git -C %s fetch %s %s:%s; a resume will retry automatically)",
			r.spec.Repo, err, r.clone, r.spec.Repo, r.clone, r.st.Branch, r.pendingSyncRef())
		return StatusError
	}
	r.st.SyncOwed = false
	return status
}

// printSummary writes the run's one-line user-facing summary (and, for a
// non-complete status, the review command) to Options.Stdout.
func (r *run) printSummary(status Status) {
	st := r.st
	fmt.Fprintf(r.opts.Stdout, "\nspec %s: %s after %d iteration(s), %d commit(s) on %s\n",
		r.spec.Slug, status, st.Iterations, st.Commits, st.Branch)
	fmt.Fprintf(r.opts.Stdout, "post-run checks: %d failed, %d warnings\n", r.prChecks.fails, r.prChecks.warns)
	if status != StatusComplete {
		fmt.Fprintf(r.opts.Stdout, "review: git -C %s log --oneline %s..%s%s\n",
			r.spec.Repo, short(st.Base), r.reviewRef(), baseSuffix(st))
	}
}

// baseSuffix annotates a review command with the requested base name,
// when known.
func baseSuffix(st *runState) string {
	if st.BaseRef == "" {
		return ""
	}
	return fmt.Sprintf("   # base %s @ %s", st.BaseRef, short(st.Base))
}

// summaryLine is the one-sentence, human-facing explanation of status.
func summaryLine(status Status, st *runState) string {
	switch status {
	case StatusComplete:
		return "All acceptance criteria met and verify is green."
	case StatusBlocked:
		return "The agent hit a decision only a human can make. Answer it by editing the spec, then re-run."
	case StatusRed:
		return "Two consecutive iterations left verify red; the branch is back at the last green commit."
	case StatusStalled:
		return "Two iterations made no progress (no new green commit, no PROGRESS.md change)."
	case StatusCapped:
		return fmt.Sprintf("Hit the iteration cap (%d) before completing. Re-run to continue.", st.Iterations)
	case StatusWindowClosed:
		return "The time window closed mid-run. Uncommitted work was saved as a patch; re-run to continue."
	case StatusInterrupted:
		return "The run was interrupted. Uncommitted work was saved as a patch; re-run to continue."
	case StatusRateLimited:
		return fmt.Sprintf("Hit the Claude usage limit (%s). Nothing was lost; re-run after it resets.", st.StopReason)
	case StatusError:
		return "The runner hit an error and stopped; nothing was recorded from the failing step."
	default:
		return "Run ended."
	}
}

// resultBody assembles the result note's full Markdown body per reference
// §3.9.2, with D1's frontmatter type rename and D11.7's leading-space-
// preserving diffstat.
func (r *run) resultBody(status Status) string {
	sp := r.spec
	st := r.st
	var b strings.Builder

	ended := r.opts.clockFn()().Format("2006-01-02T15:04:05Z07:00")
	fmt.Fprintf(&b, "---\ntype: nightshift-result\nspec: %s\nstatus: %s\nstarted: %s\nended: %s\niterations: %d\ncommits: %d\nbranch: %s\nrepo: %s\nbase_ref: %s\nbase: %s\nnightshift: %s\n---\n\n",
		sp.Slug, status, st.Started, ended, st.Iterations, st.Commits, st.Branch, sp.Repo, st.BaseRef, short(st.Base), r.version)

	switch {
	case st.SyncOwed:
		fmt.Fprintf(&b, "> [!error] Sync to %s failed, even after a retry. The work is safe in the clone at %s.\n> Recover with: `git -C %s fetch %s %s:%s`\n> A resume of this spec will retry the sync automatically.\n\n",
			sp.Repo, r.clone, sp.Repo, r.clone, st.Branch, r.pendingSyncRef())
	case r.syncPending():
		fmt.Fprintf(&b, "> [!warning] Sync pending: %s was checked out in %s, so the work landed in %s instead.\n> After switching away from the branch, run:\n> `git fetch . %s:refs/heads/%s`\n\n",
			st.Branch, sp.Repo, r.pendingSyncRef(), r.pendingSyncRef(), st.Branch)
	}

	fmt.Fprintf(&b, "# Spec run: %s — %s\n\n", sp.Slug, status)

	if status == StatusBlocked {
		if q := strings.TrimSpace(r.scaffoldRead(r.questionPath())); q != "" {
			fmt.Fprintf(&b, "## Question (agent is blocked)\n\n%s\n\n", q)
		}
	}

	b.WriteString(summaryLine(status, st) + "\n\n")

	if r.err != nil {
		fmt.Fprintf(&b, "## Error\n\n%s\n\n", r.err.Error())
	}

	if diffstat, _ := r.git("diff", "--stat", st.Base+".."+st.Branch); strings.TrimSpace(diffstat) != "" {
		fmt.Fprintf(&b, "## Changes\n\n```\n%s\n```\n\n", strings.TrimRight(diffstat, "\n"))
	}

	if progress := r.scaffoldRead(r.progressPath()); strings.TrimSpace(progress) != "" {
		fmt.Fprintf(&b, "## PROGRESS.md (tail)\n\n```\n%s\n```\n\n", strings.TrimSpace(tailLines(progress, 40)))
	}

	if len(r.prChecks.lines) > 0 {
		fmt.Fprintf(&b, "## Post-run checks\n\n```\n")
		for _, c := range r.prChecks.lines {
			fmt.Fprintf(&b, "%s\n", c.String())
		}
		b.WriteString("```\n\n")
	}

	fmt.Fprintf(&b, "## Review\n\n")
	fmt.Fprintf(&b, "- `git -C %s log --oneline %s..%s`%s\n", sp.Repo, short(st.Base), r.reviewRef(), baseSuffix(st))
	fmt.Fprintf(&b, "- `git -C %s diff %s..%s`%s\n", sp.Repo, short(st.Base), r.reviewRef(), baseSuffix(st))
	fmt.Fprintf(&b, "- runner log: `%s`\n", filepath.Join(r.dir, "runner.log"))
	if status == StatusRed || status == StatusStalled || status == StatusWindowClosed {
		fmt.Fprintf(&b, "- failed attempts: `%s`\n", r.attemptsDir())
	}

	return b.String()
}

// uniqueResultPath returns dir/name if free, else dir/<base>-2<ext>,
// dir/<base>-3<ext>, etc.
func uniqueResultPath(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 1; ; n++ {
		candidate := name
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		path := filepath.Join(dir, candidate)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return path
		}
	}
}

// writeResultNote writes the result note to <Home>/results/ and, only
// when Home is itself a git repo (D7 — the owner may choose to git-track
// ~/.nightshift), commits it there. A commit failure is logged and never
// changes the run's status.
func (r *run) writeResultNote(status Status) (string, error) {
	dir := home.ResultsDir(r.opts.Home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := r.spec.Slug + "-" + r.opts.Now.Format("2006-01-02") + ".md"
	path := uniqueResultPath(dir, name)

	if err := os.WriteFile(path, []byte(r.resultBody(status)), 0o644); err != nil {
		return "", err
	}

	if gitx.IsRepo(r.opts.Home) {
		rel, err := filepath.Rel(r.opts.Home, path)
		if err != nil {
			rel = path
		}
		if err := gitx.Commit(r.opts.Home, fmt.Sprintf("nightshift: result %s (%s)", r.spec.Slug, status), rel); err != nil {
			r.logf("commit result note failed: %v", err)
		}
	}

	return path, nil
}

// notifyText builds the notification's title and message: the title
// names the slug and status, with a sync-failed/sync-pending suffix when
// applicable.
func (r *run) notifyText(status Status) (title, msg string) {
	title = fmt.Sprintf("Spec %s: %s", r.spec.Slug, status)
	switch {
	case r.st.SyncOwed:
		title += " (sync failed)"
	case r.syncPending():
		title += " (sync pending)"
	}
	if r.prChecks.fails > 0 {
		title += " (checks failed)"
	}
	return title, summaryLine(status, r.st)
}

// resolveNotifier honors Options.Notifier if injected, else resolves
// NIGHTSHIFT_NOTIFY (env > config > "on", D6) to a Desktop or Noop
// Notifier.
func resolveNotifier(opts Options) (notify.Notifier, error) {
	if opts.Notifier != nil {
		return opts.Notifier, nil
	}
	cfg, err := home.ParseConfig(home.ConfigPath(opts.Home))
	if err != nil {
		return nil, err
	}
	val := os.Getenv("NIGHTSHIFT_NOTIFY")
	if val == "" {
		val = cfg["NIGHTSHIFT_NOTIFY"]
	}
	if val == "" {
		val = "on"
	}
	switch val {
	case "on":
		return notify.Desktop{}, nil
	case "off":
		return notify.Noop{}, nil
	default:
		return nil, fmt.Errorf("NIGHTSHIFT_NOTIFY=%q: want on or off", val)
	}
}

// notify sends the run's finished notification, silently doing nothing if
// the notifier cannot be resolved (resolveNotifier's error is meant to be
// surfaced earlier, at options-validation time — see Task 11/13).
func (r *run) notify(status Status) {
	n, err := resolveNotifier(r.opts)
	if err != nil {
		return
	}
	title, msg := r.notifyText(status)
	urgent := status == StatusError || status == StatusRed || status == StatusStalled || r.prChecks.fails > 0
	n.Notify(title, msg, urgent)
}

// finish finalizes a terminal status: retries an owed sync once more,
// records status and a history entry, writes the result note, and
// notifies. Its own error return is about finishing itself (e.g. a failed
// write); the run's substantive failure (if any) stays in r.err.
func (r *run) finish(status Status) (Status, error) {
	status = r.finalizeSync(status)
	r.st.Status = string(status)
	r.st.History = append(r.st.History, r.opts.clockFn()().Format("2006-01-02 15:04")+" "+string(status))
	_ = r.saveState()

	r.prChecks = r.postRunChecks()

	r.printSummary(status)

	notePath, err := r.writeResultNote(status)
	if err != nil {
		return status, err
	}
	r.notify(status)
	fmt.Fprintf(r.opts.Stdout, "result note: %s\n", notePath)
	return status, nil
}
