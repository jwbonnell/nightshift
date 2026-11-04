package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jwbonnell/go-libs/pkg/logx"
	"github.com/jwbonnell/nightshift/internal/gitx"
)

// runState is the on-disk run.json. Field order is the JSON field order.
type runState struct {
	Slug      string `json:"slug"`
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`       // immutable base commit sha
	BaseRef   string `json:"base_ref"`   // requested base name at run start
	LastGreen string `json:"last_green"` // sha of the last verify-green HEAD

	DepTips      map[string]string `json:"dep_tips,omitempty"`      // Task 3: slug -> sha of each depends_on tip actually merged/fast-forwarded in
	BaseResolved string            `json:"base_resolved,omitempty"` // Task 3: the base sha before integrating depends_on, set once integration finishes

	SyncRef      string   `json:"sync_ref,omitempty"`  // refs/heads/<branch> or refs/nightshift/<slug>
	SyncOwed     bool     `json:"sync_owed,omitempty"` // a copy-back failed and hasn't succeeded since
	SpecHash     string   `json:"spec_hash"`           // spec content hash the last planning pass read
	Planned      bool     `json:"planned"`             // a planning iteration ran for SpecHash
	Iterations   int      `json:"iterations"`          // cumulative across resumes
	Commits      int      `json:"commits"`             // base..HEAD at last write
	Status       string   `json:"status"`
	StopReason   string   `json:"stop_reason,omitempty"`   // Task 1: the rate-limit message that ended the run, if any
	WaitingUntil string   `json:"waiting_until,omitempty"` // Task 3: RFC3339, set only while loop() is sleeping out a usage limit
	LimitReset   string   `json:"limit_reset,omitempty"`   // Task 3: RFC3339 reset time, recorded only when ending rate-limited with a known reset
	Started      string   `json:"started"`                 // RFC3339, first run
	History      []string `json:"history"`                 // human-readable status trail

	Agents *agentsRec `json:"agents,omitempty"` // the selectors' String() form, as of the last start/resume

	NightshiftVersion string `json:"nightshift_version"` // Task 4: the binary version that ran this invocation

	ReviewRound     int `json:"review_round,omitempty"`     // fix rounds: the last review round run (0 = none yet)
	ReviewHandbacks int `json:"review_handbacks,omitempty"` // fix rounds: how many times REVIEW.md has been handed back
	GateHandbacks   int `json:"gate_handbacks,omitempty"`   // acceptance gates: how many times a failing gate was handed back
}

// agentsRec is runState.Agents: a struct rather than a map so plan always
// marshals before code.
type agentsRec struct {
	Plan string `json:"plan"`
	Code string `json:"code"`
}

// run holds one spec's in-progress execution state.
type run struct {
	opts     Options
	spec     *Spec
	sb       Sandbox
	st       *runState
	dir      string // state dir
	clone    string // dir/repo
	logFile  *os.File
	logger   *logx.Logger
	agentLog *os.File
	version  string // Task 4: opts.Version, defaulted to "dev" if empty
	err      error  // set by fail()/finalizeSync; returned alongside StatusError

	removeErr error         // Task 5: the explicit, pre-finish sb.Remove() result
	prChecks  postRunReport // Task 5: computed once in finish(), reused by printSummary/resultBody/notify

	extended int // extra iterations granted past max_iter by this loop() call

	deadline time.Time // the --until deadline computed by loop(), zero if unset

	// needIntegrate is set by setup() when a fresh clone was just created
	// for a spec with depends_on (and !IgnoreDeps): runSpec must run
	// integrateDeps() before any agent iteration.
	needIntegrate bool

	// Automatic review results (Task: review), set by runAutoReview and
	// read by resultBody/notifyText. reviewVerdict == "" means no review
	// was attempted (review: false, or status != complete).
	reviewVerdict string
	gate          *gateOutcome // nil = no gates configured or completion never reached
	reviewReason  string       // set only when reviewVerdict == "skipped"
	reviewNote    string
	reviewNoteErr error // set only when the review note itself failed to write

	// reviewRounds records every fix round run during this invocation, oldest
	// first, for resultBody's "## Review" section (see review_rounds.go). It
	// lives only in memory: a resume after a hand-back starts a fresh slice,
	// so the result note only ever lists the rounds run since the last start
	// or resume, not the full history across resumes.
	reviewRounds []roundRec
}

// git runs args against the clone in the control sandbox, stripping any
// leading sbx banner lines from the output.
func (r *run) git(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := r.sb.Git(ctx, args...)
	return stripSbxBanner(out), err
}

func (r *run) attemptsDir() string { return filepath.Join(r.dir, "attempts") }

// CloneDir is the agent's working clone for a spec's run state.
func CloneDir(stateRoot, slug string) string {
	return filepath.Join(stateRoot, slug, "repo")
}

func (r *run) progressPath() string { return filepath.Join(r.clone, "PROGRESS.md") }
func (r *run) questionPath() string { return filepath.Join(r.clone, "QUESTION.md") }
func (r *run) completePath() string { return filepath.Join(r.clone, ".spec-complete") }
func (r *run) reviewPath() string   { return filepath.Join(r.clone, "REVIEW.md") }
func (r *run) statePath() string    { return filepath.Join(r.dir, "run.json") }

// short returns sha's first 8 characters, or sha unchanged if it is no
// longer than that.
func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// logf writes one plain-text line to runner.log via logx (D5). Level is
// chosen from the message's own text, matching the reference's logf call
// sites one-for-one: a "SECURITY:" prefix is Warn, an "error:" or
// "hand-back error:" prefix is Error, everything else is Info.
func (r *run) logf(format string, args ...any) {
	if r.logger == nil {
		return
	}
	msg := fmt.Sprintf(format, args...)
	ctx := context.Background()
	switch {
	case strings.HasPrefix(format, "SECURITY:"):
		r.logger.Warn(ctx, msg)
	case strings.HasPrefix(format, "error:"), strings.HasPrefix(format, "hand-back error:"):
		r.logger.Error(ctx, msg)
	default:
		r.logger.Info(ctx, msg)
	}
}

func (r *run) loadState() error {
	data, err := os.ReadFile(r.statePath())
	if err != nil {
		return fmt.Errorf("read run state: %w", err)
	}
	var st runState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("parse run state: %w", err)
	}
	r.st = &st
	return nil
}

func (r *run) saveState() error {
	data, err := json.MarshalIndent(r.st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(r.statePath(), data, 0o644)
}

// loadPriorState reads <StateRoot>/<slug>/run.json without constructing a
// run, returning nil on any read or parse error.
func loadPriorState(opts Options, slug string) *runState {
	data, err := os.ReadFile(filepath.Join(opts.StateRoot, slug, "run.json"))
	if err != nil {
		return nil
	}
	var st runState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil
	}
	return &st
}

// checkRepo refuses a resume whose spec now resolves to a different repo
// than the one this run started against (D11.2 — the reference never
// checked this).
func (r *run) checkRepo(sp *Spec) error {
	if r.st.Repo != sp.Repo {
		return fmt.Errorf("repo changed from %s to %s since this run started; re-run with --fresh", r.st.Repo, sp.Repo)
	}
	return nil
}

// checkBase adopts a pre-feature run.json's implicit base_ref, refuses a
// resume whose spec now requests a different base, and logs (without
// refusing) when the upstream ref has simply moved.
func (r *run) checkBase(sp *Spec) error {
	st := r.st
	if st.BaseRef == "" {
		st.BaseRef = sp.Base
		return r.saveState()
	}
	if st.BaseRef != sp.Base {
		return fmt.Errorf("base changed from %s to %s since this run started; re-run with --fresh", st.BaseRef, sp.Base)
	}
	if sp.BaseSHA != "" && sp.BaseSHA != st.Base {
		r.logf("upstream moved: base_ref=%s was %s, now %s; keeping recorded base %s", sp.Base, short(st.Base), short(sp.BaseSHA), short(st.Base))
	}
	return nil
}

// seedProgress writes PROGRESS.md's initial body, unless one already
// exists (e.g. a resume).
func (r *run) seedProgress() {
	if r.scaffoldExists(r.progressPath()) {
		return
	}
	body := fmt.Sprintf("# Progress — %s\n\nSpec: %s\nVerify: %s\n\n_This journal is your only memory across iterations. Append what you did, whether verify passed, and anything the next iteration would waste time rediscovering._\n\n",
		r.spec.Slug, r.spec.Path, r.spec.Verify)
	r.scaffoldWrite(r.progressPath(), body)
}

// appendLines appends each of lines to path (each followed by "\n") that
// isn't already present (TrimSpace-compared), creating path if needed.
// Matches the reference exactly, including not adding a leading separator
// if the file lacks a trailing newline.
func appendLines(path string, lines ...string) error {
	existing := map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			existing[strings.TrimSpace(l)] = true
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, l := range lines {
		if existing[l] {
			continue
		}
		if _, err := fmt.Fprintln(f, l); err != nil {
			return err
		}
	}
	return nil
}

// cloneRepo creates a fresh clone with no remotes, checked out onto
// spec/<slug> at the spec's resolved base, with scaffold files excluded
// and seeded. --no-local forces a real copy of the objects instead of
// git's local-clone hardlink optimization: the agent sandbox mounts this
// clone read-write with root inside it, and a hardlinked object file is
// the same inode as the source repo's — a hostile agent overwriting one
// in place would silently corrupt the source repo's history with no host
// git ever involved.
func (r *run) cloneRepo() error {
	sp := r.spec
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	if out, err := gitx.Run(r.dir, "clone", "--no-local", sp.Repo, "repo"); err != nil {
		return fmt.Errorf("clone %s: %v: %s", sp.Repo, err, out)
	}

	if _, err := gitx.Run(r.clone, "cat-file", "-e", sp.BaseSHA+"^{commit}"); err != nil {
		if out, ferr := gitx.Run(r.clone, "fetch", "origin", sp.BaseSHA); ferr != nil {
			return fmt.Errorf("fetch base %s from %s: %v: %s", sp.BaseSHA, sp.Repo, ferr, out)
		}
	}

	// Task 3: --no-local already clones every branch, including any
	// depends_on tips, so this fetch is only a fallback for an object the
	// initial clone somehow missed.
	for _, t := range sp.depTips {
		if t.sha == "" {
			continue
		}
		if _, err := gitx.Run(r.clone, "cat-file", "-e", t.sha+"^{commit}"); err != nil {
			if out, ferr := gitx.Run(r.clone, "fetch", "origin", t.sha); ferr != nil {
				return fmt.Errorf("fetch dep %s tip %s from %s: %v: %s", t.slug, t.sha, sp.Repo, ferr, out)
			}
		}
	}

	if out, err := gitx.Run(r.clone, "remote", "remove", "origin"); err != nil {
		return fmt.Errorf("remove remote: %v: %s", err, out)
	}
	_, _ = gitx.Run(r.clone, "config", "user.name", "nightshift")
	_, _ = gitx.Run(r.clone, "config", "user.email", "nightshift@localhost")

	branch := "spec/" + sp.Slug
	if out, err := gitx.Run(r.clone, "checkout", "-B", branch, sp.BaseSHA); err != nil {
		return fmt.Errorf("create branch %s off %s: %v: %s", branch, sp.BaseSHA, err, out)
	}

	if err := os.MkdirAll(filepath.Join(r.clone, ".git", "info"), 0o755); err != nil {
		return err
	}
	if err := appendLines(filepath.Join(r.clone, ".git", "info", "exclude"), "PROGRESS.md", "QUESTION.md", ".spec-complete", "REVIEW.md"); err != nil {
		return err
	}

	r.seedProgress()

	r.st = &runState{
		Slug:              sp.Slug,
		Repo:              sp.Repo,
		Branch:            branch,
		Base:              sp.BaseSHA,
		BaseRef:           sp.Base,
		LastGreen:         sp.BaseSHA,
		SpecHash:          sp.ContentHash,
		Started:           r.opts.Now.Format(time.RFC3339),
		Agents:            &agentsRec{Plan: sp.PlannerSel.String(), Code: sp.ModelSel.String()},
		NightshiftVersion: r.version,
	}
	return r.saveState()
}

// branchCheckedOut reports whether branch is checked out in a linked
// worktree of repo (host git, since this inspects the SOURCE repo).
func branchCheckedOut(repo, branch string) (bool, error) {
	out, err := gitx.Run(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("git worktree list: %v: %s", err, out)
	}
	want := "branch refs/heads/" + branch
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == want {
			return true, nil
		}
	}
	return false, nil
}

// pendingSyncRef is the runner-owned ref (D1 renames from the original tool):
// the runner-owned ref copy-back uses when spec/<slug> is checked out
// elsewhere in the source repo.
func (r *run) pendingSyncRef() string {
	return "refs/nightshift/" + r.spec.Slug
}

func (r *run) syncPending() bool {
	return r.st.SyncRef == r.pendingSyncRef()
}

// reviewRef is where a reviewer should look: the pending ref if the copy-
// back landed there, otherwise the real branch.
func (r *run) reviewRef() string {
	if r.syncPending() {
		return r.pendingSyncRef()
	}
	return r.st.Branch
}

// reviewRoundRef is where a fix round's reviewed commit is published in the
// source repo for the duration of one round, so a reviewer workspace can be
// built from the source repo without the host ever running git against the
// agent's clone directly.
func reviewRoundRef(slug string) string {
	return "refs/nightshift/review/" + slug
}

// publishReviewRef copies the clone's last_green commit to the source
// repo's reviewRoundRef, by fetching the sha out of the clone — never by
// running host git against the clone itself.
func (r *run) publishReviewRef() error {
	sp := r.spec
	if err := validSHA("last_green", r.st.LastGreen); err != nil {
		return err
	}
	ref := reviewRoundRef(sp.Slug)
	return withRepoLock(r.opts.StateRoot, sp.Repo, sp.Slug, func() error {
		out, err := gitx.RunRetry(sp.Repo, "fetch", r.clone, r.st.LastGreen+":"+ref)
		if err != nil {
			return fmt.Errorf("publish review ref %s: %v: %s", ref, err, out)
		}
		return nil
	})
}

// deleteReviewRef removes the source repo's reviewRoundRef, best-effort: a
// fix round must never leave this ref behind once its round ends.
func (r *run) deleteReviewRef() {
	sp := r.spec
	ref := reviewRoundRef(sp.Slug)
	err := withRepoLock(r.opts.StateRoot, sp.Repo, sp.Slug, func() error {
		if !refExists(sp.Repo, ref) {
			return nil
		}
		if out, err := gitx.RunRetry(sp.Repo, "update-ref", "-d", ref); err != nil {
			return fmt.Errorf("delete review ref %s: %v: %s", ref, err, out)
		}
		return nil
	})
	if err != nil {
		r.logf("review: %v", err)
	}
}

// syncBranchBack copies the clone's spec/<slug> branch back to the source
// repo: to refs/heads/spec/<slug> normally, or to the pending ref (forced)
// if that branch is checked out elsewhere in the source repo right now.
func (r *run) syncBranchBack() error {
	sp := r.spec
	st := r.st
	branch := st.Branch
	headsRef := "refs/heads/" + branch
	pending := r.pendingSyncRef()

	checkedOut, err := branchCheckedOut(sp.Repo, branch)
	if err != nil {
		return fmt.Errorf("check checked-out branch: %w", err)
	}

	dst := headsRef
	refspec := branch + ":" + dst
	if checkedOut {
		dst = pending
		refspec = "+" + branch + ":" + dst
	}

	err = withRepoLock(r.opts.StateRoot, sp.Repo, sp.Slug, func() error {
		out, ferr := gitx.RunRetry(sp.Repo, "fetch", r.clone, refspec)
		if ferr != nil {
			return fmt.Errorf("copy back to %s failed: %v: %s", dst, ferr, out)
		}
		if !checkedOut && refExists(sp.Repo, pending) {
			_, _ = gitx.RunRetry(sp.Repo, "update-ref", "-d", pending)
		}
		return nil
	})
	if err != nil {
		return err
	}
	st.SyncRef = dst
	return nil
}

// doFresh discards prior state for one spec before a run starts: with
// confirmation (TTY prompt, or --yes), it deletes the spec/<slug> branch
// and the refs/nightshift/<slug> pending ref from the SOURCE repo if they
// exist, then removes the state dir. D11.5: unlike the reference, this
// still deletes the refs even when there is no state dir to remove.
func doFresh(opts Options, sp *Spec, dir string) error {
	hasState := dirExists(dir)

	if !opts.Yes {
		if !opts.IsTTY {
			return fmt.Errorf("--fresh will delete %s and branch spec/%s — re-run with --yes to confirm (no TTY to prompt)", dir, sp.Slug)
		}
		fmt.Fprintf(opts.Stdout, "--fresh deletes %s and branch spec/%s in %s. Proceed? [y/N] ", dir, sp.Slug, sp.Repo)
		var ans string
		_, _ = fmt.Fscanln(opts.Stdin, &ans)
		if !strings.EqualFold(strings.TrimSpace(ans), "y") {
			return errors.New("aborted")
		}
	}

	branch := "refs/heads/spec/" + sp.Slug
	pending := "refs/nightshift/" + sp.Slug
	review := reviewRoundRef(sp.Slug)
	err := withRepoLock(opts.StateRoot, sp.Repo, sp.Slug, func() error {
		if refExists(sp.Repo, branch) {
			if out, err := gitx.RunRetry(sp.Repo, "update-ref", "-d", branch); err != nil {
				return fmt.Errorf("delete branch spec/%s: %v: %s", sp.Slug, err, out)
			}
		}
		if refExists(sp.Repo, pending) {
			if out, err := gitx.RunRetry(sp.Repo, "update-ref", "-d", pending); err != nil {
				return fmt.Errorf("delete pending ref %s: %v: %s", pending, err, out)
			}
		}
		if refExists(sp.Repo, review) {
			if out, err := gitx.RunRetry(sp.Repo, "update-ref", "-d", review); err != nil {
				return fmt.Errorf("delete review ref %s: %v: %s", review, err, out)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	if !hasState {
		return nil
	}
	return os.RemoveAll(dir)
}

// setup prepares a run's state dir and clone, resuming an existing clone
// if one is present or creating a fresh one otherwise.
func setup(opts Options, sp *Spec) (*run, error) {
	dir := filepath.Join(opts.StateRoot, sp.Slug)
	if opts.Fresh {
		if err := doFresh(opts, sp, dir); err != nil {
			return nil, err
		}
	}

	// Task 3: runLocked already resolved sp.depTips before calling setup;
	// a caller that skips runLocked (e.g. newTestRun) gets them resolved
	// here instead.
	if sp.depTips == nil && len(sp.DependsOn) > 0 && !opts.IgnoreDeps {
		sp.depTips = resolveDepTips(sp)
	}

	v := opts.Version
	if v == "" {
		v = "dev"
	}
	r := &run{opts: opts, spec: sp, dir: dir, clone: CloneDir(opts.StateRoot, sp.Slug), version: v}
	resuming := dirExists(r.clone)

	if err := os.MkdirAll(r.attemptsDir(), 0o755); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "runner.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	r.logFile = logFile
	r.logger = logx.NewText(logFile, slog.LevelInfo, nil)

	agentLog, err := os.OpenFile(filepath.Join(dir, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	r.agentLog = agentLog

	if resuming {
		if err := r.loadState(); err != nil {
			return nil, err
		}
		if err := r.checkRepo(sp); err != nil {
			return nil, err
		}
		if err := r.checkBase(sp); err != nil {
			return nil, err
		}
		if err := checkDepTipsResume(r, sp); err != nil {
			return nil, err
		}
		if err := validSHA("run.json base", r.st.Base); err != nil {
			return nil, fmt.Errorf("run.json has a corrupt base %q; re-run with --fresh", r.st.Base)
		}
		if err := validSHA("run.json last_green", r.st.LastGreen); err != nil {
			return nil, fmt.Errorf("run.json has a corrupt last_green %q; re-run with --fresh", r.st.LastGreen)
		}
		if r.scaffoldRemove(r.completePath()) {
			r.logf("resume: cleared stale .spec-complete")
			r.appendProgress("Runner: cleared a stale .spec-complete left by a previous session; re-claim completion when done.")
		}
		old := r.st.Agents
		r.st.Agents = &agentsRec{Plan: sp.PlannerSel.String(), Code: sp.ModelSel.String()}
		if old != nil && (old.Plan != r.st.Agents.Plan || old.Code != r.st.Agents.Code) {
			r.logf("agents changed: plan %s→%s, code %s→%s", old.Plan, r.st.Agents.Plan, old.Code, r.st.Agents.Code)
		}
		r.st.NightshiftVersion = r.version
		if err := r.saveState(); err != nil {
			return nil, err
		}
		r.logf("resume: branch=%s base=%s last_green=%s version=%s agents=plan:%s,code:%s",
			r.st.Branch, short(r.st.Base), short(r.st.LastGreen), r.version, r.st.Agents.Plan, r.st.Agents.Code)
		return r, nil
	}

	if err := r.cloneRepo(); err != nil {
		return nil, err
	}
	if len(sp.DependsOn) > 0 && !opts.IgnoreDeps {
		r.needIntegrate = true
	}
	r.logf("start: repo=%s branch=%s base=%s version=%s agents=plan:%s,code:%s",
		sp.Repo, r.st.Branch, short(r.st.Base), r.version, sp.PlannerSel.String(), sp.ModelSel.String())
	return r, nil
}
