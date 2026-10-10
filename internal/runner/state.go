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
	Slug       string   `json:"slug"`
	Repo       string   `json:"repo"`
	Branch     string   `json:"branch"`
	Base       string   `json:"base"`                // immutable base commit sha
	BaseRef    string   `json:"base_ref"`            // requested base name at run start
	LastGreen  string   `json:"last_green"`          // sha of the last verify-green HEAD
	SyncRef    string   `json:"sync_ref,omitempty"`  // refs/heads/<branch> or refs/nightshift/<slug>
	SyncOwed   bool     `json:"sync_owed,omitempty"` // a copy-back failed and hasn't succeeded since
	SpecHash   string   `json:"spec_hash"`           // spec content hash at last plan
	Planned    bool     `json:"planned"`             // a planning iteration ran for SpecHash
	Iterations int      `json:"iterations"`          // cumulative across resumes
	Commits    int      `json:"commits"`             // base..HEAD at last write
	Status     string   `json:"status"`
	StopReason string   `json:"stop_reason,omitempty"` // Task 1: the rate-limit message that ended the run, if any
	Started    string   `json:"started"`               // RFC3339, first run
	History    []string `json:"history"`               // human-readable status trail
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
	err      error // set by fail()/finalizeSync; returned alongside StatusError
}

// git runs args against the clone in the control sandbox, stripping any
// leading sbx banner lines from the output.
func (r *run) git(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := r.sb.Git(ctx, args...)
	return stripSbxBanner(out), err
}

func (r *run) attemptsDir() string  { return filepath.Join(r.dir, "attempts") }
func (r *run) progressPath() string { return filepath.Join(r.clone, "PROGRESS.md") }
func (r *run) questionPath() string { return filepath.Join(r.clone, "QUESTION.md") }
func (r *run) completePath() string { return filepath.Join(r.clone, ".spec-complete") }
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
	if err := appendLines(filepath.Join(r.clone, ".git", "info", "exclude"), "PROGRESS.md", "QUESTION.md", ".spec-complete"); err != nil {
		return err
	}

	r.seedProgress()

	r.st = &runState{
		Slug:      sp.Slug,
		Repo:      sp.Repo,
		Branch:    branch,
		Base:      sp.BaseSHA,
		BaseRef:   sp.Base,
		LastGreen: sp.BaseSHA,
		SpecHash:  sp.ContentHash,
		Started:   r.opts.Now.Format(time.RFC3339),
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

// pendingSyncRef is the D1 rename of the reference's refs/spec-runs/<slug>:
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

	r := &run{opts: opts, spec: sp, dir: dir, clone: filepath.Join(dir, "repo")}
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
		r.logf("resume: branch=%s base=%s last_green=%s", r.st.Branch, short(r.st.Base), short(r.st.LastGreen))
		return r, nil
	}

	if err := r.cloneRepo(); err != nil {
		return nil, err
	}
	r.logf("start: repo=%s branch=%s base=%s", sp.Repo, r.st.Branch, short(r.st.Base))
	return r, nil
}
