package runner

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
)

// setupRun resolves demo against repo in a fresh home and runs setup(),
// filling in BaseSHA (which resolveSpec itself leaves empty) the way the
// real run flow eventually will.
func setupRun(t *testing.T, homeDir, repo string, opts Options) (*run, *Spec) {
	t.Helper()
	opts.Home = homeDir
	if opts.SpecArg == "" {
		opts.SpecArg = "demo"
	}
	if opts.StateRoot == "" {
		opts.StateRoot = home.RunsDir(homeDir)
	}
	if opts.Now.IsZero() {
		opts.Now = time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	}

	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	r, err := setup(opts, sp)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() {
		if r.logFile != nil {
			r.logFile.Close()
		}
	})
	return r, sp
}

func TestSetupFreshCloneHasNoRemotes(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	r, sp := setupRun(t, homeDir, repo, Options{})

	remotes := mustGit(t, r.clone, "remote")
	if strings.TrimSpace(remotes) != "" {
		t.Fatalf("clone has remotes: %q", remotes)
	}

	branch := mustGit(t, r.clone, "rev-parse", "--abbrev-ref", "HEAD")
	if branch != "spec/demo" {
		t.Fatalf("branch = %q, want %q", branch, "spec/demo")
	}

	if r.st.Base != sp.BaseSHA || r.st.LastGreen != sp.BaseSHA {
		t.Fatalf("run.json base/last_green = %q/%q, want %q", r.st.Base, r.st.LastGreen, sp.BaseSHA)
	}
	if r.st.Started != "2026-01-02T03:04:00Z" {
		t.Fatalf("Started = %q", r.st.Started)
	}

	if !fileExistsT(filepath.Join(r.clone, "PROGRESS.md")) {
		t.Fatalf("PROGRESS.md not seeded")
	}
	exclude, err := os.ReadFile(filepath.Join(r.clone, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("read exclude: %v", err)
	}
	for _, name := range []string{"PROGRESS.md", "QUESTION.md", ".spec-complete"} {
		if !strings.Contains(string(exclude), name) {
			t.Fatalf("info/exclude missing %s: %q", name, exclude)
		}
	}
}

// TestCloneObjectsNotHardlinked is a regression test for the security fix
// found by the first real-sbx smoke run: a default `git clone` of a local
// path hardlinks .git/objects, so the clone's objects are the same inodes
// as the source repo's. The agent sandbox mounts the clone read-write
// with root inside it, so a hostile agent overwriting an object in place
// would silently corrupt the source repo's history. --no-local forces a
// real copy; this asserts every object file has a link count of 1 and
// that no objects/info/alternates (another way to share objects) exists.
func TestCloneObjectsNotHardlinked(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	r, _ := setupRun(t, homeDir, repo, Options{})

	objectsDir := filepath.Join(r.clone, ".git", "objects")
	found := false
	err := filepath.WalkDir(objectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		found = true
		info, err := d.Info()
		if err != nil {
			return err
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("no syscall.Stat_t for %s", path)
		}
		if stat.Nlink != 1 {
			t.Fatalf("%s has link count %d, want 1 (hardlinked into the source repo's objects)", path, stat.Nlink)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", objectsDir, err)
	}
	if !found {
		t.Fatalf("no object files found under %s", objectsDir)
	}

	if fileExistsT(filepath.Join(objectsDir, "info", "alternates")) {
		t.Fatalf("objects/info/alternates should not exist")
	}
}

func TestSourceRepoUntouchedBySetup(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	before := mustGit(t, repo, "rev-parse", "HEAD")
	r, _ := setupRun(t, homeDir, repo, Options{})
	after := mustGit(t, repo, "rev-parse", "HEAD")

	if before != after {
		t.Fatalf("source repo HEAD moved: %s -> %s", before, after)
	}
	// The spec/<slug> branch is created in the CLONE only; it reaches the
	// source repo later via the copy-back sync (Task 9), not setup().
	if refExists(repo, "refs/heads/spec/demo") {
		t.Fatalf("spec/demo should not exist in the source repo yet")
	}
	if !refExists(r.clone, "refs/heads/spec/demo") {
		t.Fatalf("expected refs/heads/spec/demo to exist in the clone")
	}
}

func TestSetupResumeReusesClone(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	r1, _ := setupRun(t, homeDir, repo, Options{})
	if err := os.WriteFile(filepath.Join(r1.clone, "step1.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write step1.go: %v", err)
	}
	mustGit(t, r1.clone, "add", "-A")
	mustGit(t, r1.clone, "commit", "-q", "-m", "step1")
	r1.logFile.Close()

	r2, _ := setupRun(t, homeDir, repo, Options{})
	if !fileExistsT(filepath.Join(r2.clone, "step1.go")) {
		t.Fatalf("resume did not reuse the existing clone (step1.go missing)")
	}
	if r2.st.Slug != "demo" {
		t.Fatalf("resumed state looks wrong: %+v", r2.st)
	}
}

func TestResumeRepoChangedRefuses(t *testing.T) {
	repo := newSourceRepo(t)
	otherRepo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	r1, _ := setupRun(t, homeDir, repo, Options{})
	r1.logFile.Close()

	writeReadySpec(t, homeDir, "demo", "", otherRepo)
	opts := Options{SpecArg: "demo", Home: homeDir, StateRoot: home.RunsDir(homeDir), Now: time.Now()}
	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	_, err = setup(opts, sp)
	want := "repo changed from " + repo + " to " + otherRepo + " since this run started; re-run with --fresh"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestResumeBaseChangedRefuses(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-q", "-b", "feat/x")
	if err := os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "feat/x tip")
	mustGit(t, repo, "checkout", "-q", "main")

	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "base: feat/x\n", repo)
	r1, _ := setupRun(t, homeDir, repo, Options{})
	r1.logFile.Close()

	writeReadySpec(t, homeDir, "demo", "base: main\n", repo)
	opts := Options{SpecArg: "demo", Home: homeDir, StateRoot: home.RunsDir(homeDir), Now: time.Now()}
	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	_, err = setup(opts, sp)
	want := "base changed from feat/x to main since this run started; re-run with --fresh"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestFreshStartsOver(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	r1, _ := setupRun(t, homeDir, repo, Options{})
	if err := os.WriteFile(filepath.Join(r1.clone, "step1.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write step1.go: %v", err)
	}
	mustGit(t, r1.clone, "add", "-A")
	mustGit(t, r1.clone, "commit", "-q", "-m", "step1")
	stateDir := r1.dir
	r1.logFile.Close()

	// Simulate a prior sync so the branch also exists in the source repo,
	// to confirm --fresh removes it from there too.
	if out, err := gitx.Run(repo, "fetch", r1.clone, "spec/demo:refs/heads/spec/demo"); err != nil {
		t.Fatalf("simulate sync: %v: %s", err, out)
	}

	r2, _ := setupRun(t, homeDir, repo, Options{Fresh: true, Yes: true})
	defer r2.logFile.Close()

	if !dirExists(stateDir) {
		t.Fatalf("expected a fresh state dir to exist after re-setup")
	}
	if fileExistsT(filepath.Join(r2.clone, "step1.go")) {
		t.Fatalf("--fresh should have discarded the old clone, but step1.go survived")
	}
	if refExists(repo, "refs/heads/spec/demo") {
		t.Fatalf("--fresh should have deleted spec/demo from the source repo")
	}
}

func TestFreshWithoutStateDeletesRefs(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	if out, err := mustGitOK(t, repo, "update-ref", "refs/heads/spec/demo", sp.BaseSHA); err != nil {
		t.Fatalf("plant branch: %v: %s", err, out)
	}
	if out, err := mustGitOK(t, repo, "update-ref", "refs/nightshift/demo", sp.BaseSHA); err != nil {
		t.Fatalf("plant pending ref: %v: %s", err, out)
	}

	stateRoot := home.RunsDir(homeDir)
	dir := filepath.Join(stateRoot, "demo")
	if dirExists(dir) {
		t.Fatalf("test setup: state dir should not exist yet")
	}

	opts := Options{StateRoot: stateRoot, Fresh: true, Yes: true}
	if err := doFresh(opts, sp, dir); err != nil {
		t.Fatalf("doFresh: %v", err)
	}

	if refExists(repo, "refs/heads/spec/demo") {
		t.Fatalf("branch still exists after --fresh --yes with no state dir")
	}
	if refExists(repo, "refs/nightshift/demo") {
		t.Fatalf("pending ref still exists after --fresh --yes with no state dir")
	}
}

func TestScaffoldSymlinkSafety(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	r, _ := setupRun(t, homeDir, repo, Options{})

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	questionLink := r.questionPath()
	if err := os.Symlink(secret, questionLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got := r.scaffoldRead(questionLink)
	if got != "" {
		t.Fatalf("scaffoldRead followed the symlink: got %q", got)
	}

	removed := r.scaffoldRemove(questionLink)
	if !removed {
		t.Fatalf("scaffoldRemove did not remove the symlink itself")
	}
	if _, err := os.Lstat(questionLink); !os.IsNotExist(err) {
		t.Fatalf("symlink still present after scaffoldRemove")
	}
	secretData, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(secretData) != "do not leak" {
		t.Fatalf("secret file was modified: %q", secretData)
	}

	r.logFile.Close()
	logData, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "not a regular file") {
		t.Fatalf("runner.log missing the SECURITY line: %q", logData)
	}
}

func TestDoFreshConfirmation(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("no TTY and no --yes refuses", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)
		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		dir := filepath.Join(home.RunsDir(homeDir), "demo")

		err = doFresh(Options{StateRoot: home.RunsDir(homeDir), IsTTY: false}, sp, dir)
		want := "--fresh will delete " + dir + " and branch spec/demo — re-run with --yes to confirm (no TTY to prompt)"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("TTY prompt accepts y", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)
		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		dir := filepath.Join(home.RunsDir(homeDir), "demo")

		var stdout strings.Builder
		err = doFresh(Options{
			StateRoot: home.RunsDir(homeDir),
			IsTTY:     true,
			Stdout:    &stdout,
			Stdin:     strings.NewReader("y\n"),
		}, sp, dir)
		if err != nil {
			t.Fatalf("doFresh: %v", err)
		}
		wantPrompt := "--fresh deletes " + dir + " and branch spec/demo in " + repo + ". Proceed? [y/N] "
		if stdout.String() != wantPrompt {
			t.Fatalf("prompt = %q, want %q", stdout.String(), wantPrompt)
		}
	})

	t.Run("TTY prompt rejects anything else", func(t *testing.T) {
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "demo", "", repo)
		sp, err := resolveSpec(Options{SpecArg: "demo", Home: homeDir})
		if err != nil {
			t.Fatalf("resolveSpec: %v", err)
		}
		dir := filepath.Join(home.RunsDir(homeDir), "demo")

		err = doFresh(Options{
			StateRoot: home.RunsDir(homeDir),
			IsTTY:     true,
			Stdout:    &strings.Builder{},
			Stdin:     strings.NewReader("n\n"),
		}, sp, dir)
		if err == nil || err.Error() != "aborted" {
			t.Fatalf("err = %v, want %q", err, "aborted")
		}
	})
}

func fileExistsT(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func mustGitOK(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	return gitx.Run(dir, args...)
}
