package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/lock"
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
	Started    string   `json:"started"` // RFC3339, first run
	History    []string `json:"history"` // human-readable status trail
}

// run holds one spec's in-progress execution state. Sandbox (sb) is added
// in Task 8, once internal/runner has a Sandbox type.
type run struct {
	opts  Options
	spec  *Spec
	st    *runState
	dir   string // state dir
	clone string // dir/repo
	log   *os.File
	err   error // set by fail()/finalizeSync; returned alongside StatusError
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

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// validSHA reports an error if s does not look like a full commit id.
// TODO(Task 9): move into loop.go to match the reference's file split.
func validSHA(what, s string) error {
	if !fullSHA.MatchString(s) {
		return fmt.Errorf("%s returned %q, which is not a commit id — refusing to record it", what, s)
	}
	return nil
}

// logf writes one plain timestamped line to runner.log. Task 9 (D5)
// replaces this with JSON lines via go-libs logx; call sites' message
// text is already written to match verbatim, so that task only needs to
// change the OUTPUT mechanism, not the wording.
func (r *run) logf(format string, args ...any) {
	if r.log == nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	fmt.Fprintf(r.log, "%s %s\n", time.Now().Format("15:04:05"), line)
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

// appendProgress appends one dated line to PROGRESS.md. Task 9 (D11.9)
// switches the timestamp to the wall clock at write time; for now it
// matches the reference's use of the run's frozen start time.
// TODO(Task 9): move into loop.go to match the reference's file split.
func (r *run) appendProgress(line string) {
	r.scaffoldAppend(r.progressPath(), fmt.Sprintf("\n- %s %s\n", r.opts.Now.Format("2006-01-02 15:04"), line))
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
// and seeded.
func (r *run) cloneRepo() error {
	sp := r.spec
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return err
	}
	if out, err := gitx.Run(r.dir, "clone", sp.Repo, "repo"); err != nil {
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

// repoHash is a stable 12-hex-char id for repoPath's git-common-dir, used
// to name its repo lock file. Worktrees and symlinked repo paths that
// share a git dir canonicalize to the same hash.
// TODO(Task 11): move into locks.go to match the reference's file split.
func repoHash(repoPath string) (string, error) {
	gitDir, err := gitx.GitDir(repoPath, "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git common dir for %s: %w", repoPath, err)
	}
	sum := sha256.Sum256([]byte(gitDir))
	return hex.EncodeToString(sum[:])[:12], nil
}

var repoLockWait = 2 * time.Minute

// withRepoLock serializes local ref writes against repoPath across
// concurrent nightshift processes.
// TODO(Task 11): move into locks.go to match the reference's file split.
func withRepoLock(stateRoot, repoPath, label string, fn func() error) error {
	hash, err := repoHash(repoPath)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(stateRoot, ".locks", "repo-"+hash+".lock")
	ctx, cancel := context.WithTimeout(context.Background(), repoLockWait)
	defer cancel()
	l, err := lock.Acquire(ctx, lockPath, label)
	if err != nil {
		return fmt.Errorf("repo lock for %s: %w", repoPath, err)
	}
	defer l.Release()
	return fn()
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
	f, err := os.OpenFile(filepath.Join(dir, "runner.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	r.log = f

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
		r.logf("resume: slug=%s branch=%s base=%s last_green=%s", sp.Slug, r.st.Branch, short(r.st.Base), short(r.st.LastGreen))
		return r, nil
	}

	if err := r.cloneRepo(); err != nil {
		return nil, err
	}
	r.logf("start: slug=%s repo=%s branch=%s base=%s", sp.Slug, sp.Repo, r.st.Branch, short(r.st.Base))
	return r, nil
}
