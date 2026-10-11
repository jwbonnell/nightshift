package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
	"github.com/jwbonnell/nightshift/internal/runner"
)

// recordingReviewer is the cmd package's own stub runner.Reviewer,
// injected through the newReviewer test seam var (runner's own
// stubReviewer, in internal/runner's test files, isn't visible here).
type recordingReviewer struct {
	text        string
	err         error
	calls       int
	model       string
	ws          string
	deadline    time.Time
	hasDeadline bool
}

func (s *recordingReviewer) Review(ctx context.Context, ws, model, prompt string) (string, error) {
	s.calls++
	s.model = model
	s.ws = ws
	s.deadline, s.hasDeadline = ctx.Deadline()
	return s.text, s.err
}

// newReviewFixture builds a source repo with a base commit on "main" and
// one further commit on "spec/<slug>" (the simulated agent work), plus a
// matching <stateRoot>/<slug>/run.json, the minimum `nightshift review`
// needs: no live *run, just run.json and the source repo.
func newReviewFixture(t *testing.T, slug string) (homeDir, repo, baseSHA, headSHA string) {
	t.Helper()
	repo = newTestSourceRepo(t)

	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	baseSHA = runGit("rev-parse", "HEAD")
	runGit("checkout", "-q", "-b", "spec/"+slug)
	if err := os.WriteFile(filepath.Join(repo, "feature.go"), []byte("package main\n\nfunc Feature() {}\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	runGit("add", "-A")
	runGit("commit", "-q", "-m", "add feature")
	headSHA = runGit("rev-parse", "HEAD")
	runGit("checkout", "-q", "main")

	homeDir = t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	dir := filepath.Join(stateRoot, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	writeRunState(t, dir, map[string]any{
		"slug": slug, "repo": repo, "branch": "spec/" + slug,
		"base": baseSHA, "base_ref": "main", "last_green": headSHA,
		"iterations": 1, "commits": 1, "status": "complete",
		"started": time.Now().Format(time.RFC3339),
	})
	return homeDir, repo, baseSHA, headSHA
}

func writeRunState(t *testing.T, dir string, st map[string]any) {
	t.Helper()
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal run state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatalf("write run.json: %v", err)
	}
}

func withStubReviewer(t *testing.T, rv runner.Reviewer) {
	t.Helper()
	newReviewer = func() runner.Reviewer { return rv }
	t.Cleanup(func() { newReviewer = nil })
}

func TestReviewCommandWritesNote(t *testing.T) {
	slug := "demo"
	homeDir, _, _, _ := newReviewFixture(t, slug)
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	rv := &recordingReviewer{text: "VERDICT: approve\nSUMMARY: looks good.\n"}
	withStubReviewer(t, rv)

	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader(""))
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	if rv.calls != 1 {
		t.Fatalf("reviewer called %d times, want 1", rv.calls)
	}
	if !strings.Contains(stdout.String(), "review: approve — ") {
		t.Fatalf("stdout = %q", stdout.String())
	}

	matches, err := filepath.Glob(filepath.Join(home.ResultsDir(homeDir), slug+"-*-review.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("expected exactly one review note, got %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read note: %v", err)
	}
	if !strings.Contains(string(data), "verdict: approve") {
		t.Fatalf("note missing verdict: approve:\n%s", data)
	}
}

func TestReviewCommandNoCommits(t *testing.T) {
	slug := "demo"
	homeDir, repo, baseSHA, _ := newReviewFixture(t, slug)
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	dir := filepath.Join(home.RunsDir(homeDir), slug)
	writeRunState(t, dir, map[string]any{
		"slug": slug, "repo": repo, "branch": "spec/" + slug,
		"base": baseSHA, "base_ref": "main", "last_green": baseSHA,
		"iterations": 1, "commits": 0, "status": "capped",
		"started": time.Now().Format(time.RFC3339),
	})

	rv := &recordingReviewer{text: "VERDICT: approve\n"}
	withStubReviewer(t, rv)

	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader(""))
	if exit != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "nothing to review: "+slug+" has no commits since base") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if rv.calls != 0 {
		t.Fatalf("reviewer should not have been called")
	}
}

func TestReviewCommandBusy(t *testing.T) {
	slug := "demo"
	homeDir, _, _, _ := newReviewFixture(t, slug)
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	stateRoot := home.RunsDir(homeDir)
	// Mirrors runner's own (unexported) specLockPath format: stateRoot/
	// .locks/spec-<slug>.lock.
	lockPath := filepath.Join(stateRoot, ".locks", "spec-"+slug+".lock")
	l, err := lock.TryAcquire(lockPath, slug)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	rv := &recordingReviewer{text: "VERDICT: approve\n"}
	withStubReviewer(t, rv)

	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader(""))
	if exit != runner.ExitCode(runner.StatusBusy) {
		t.Fatalf("exit=%d, want %d; stderr=%s", exit, runner.ExitCode(runner.StatusBusy), stderr.String())
	}
	if !strings.Contains(stderr.String(), "already running") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if rv.calls != 0 {
		t.Fatalf("reviewer should not have been called while busy")
	}
}

func TestReviewCommandModelPrecedence(t *testing.T) {
	slug := "demo"
	homeDir, _, _, _ := newReviewFixture(t, slug)
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir ready: %v", err)
	}
	specPath := filepath.Join(home.ReadyDir(homeDir), slug+".md")
	if err := os.WriteFile(specPath, []byte("---\nrepo: .\nverify: true\nreview_model: sonnet\n---\n\n# spec\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	t.Run("frontmatter wins over default", func(t *testing.T) {
		rv := &recordingReviewer{text: "VERDICT: approve\n"}
		withStubReviewer(t, rv)
		var stdout, stderr bytes.Buffer
		if exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader("")); exit != 0 {
			t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
		}
		if rv.model != "sonnet" {
			t.Fatalf("model = %q, want sonnet (from frontmatter)", rv.model)
		}
	})

	t.Run("flag wins over frontmatter", func(t *testing.T) {
		rv := &recordingReviewer{text: "VERDICT: approve\n"}
		withStubReviewer(t, rv)
		var stdout, stderr bytes.Buffer
		if exit := dispatch([]string{"review", slug, "--model", "haiku"}, &stdout, &stderr, strings.NewReader("")); exit != 0 {
			t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
		}
		if rv.model != "haiku" {
			t.Fatalf("model = %q, want haiku (from --model flag)", rv.model)
		}
	})
}

func TestReviewCommandLockBeforeState(t *testing.T) {
	slug := "demo"
	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	stateRoot := home.RunsDir(homeDir)
	// Mirrors runner's own (unexported) specLockPath format: stateRoot/
	// .locks/spec-<slug>.lock. No run.json exists anywhere under
	// stateRoot/<slug> — the lock must still win first.
	lockPath := filepath.Join(stateRoot, ".locks", "spec-"+slug+".lock")
	l, err := lock.TryAcquire(lockPath, slug)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	rv := &recordingReviewer{text: "VERDICT: approve\n"}
	withStubReviewer(t, rv)

	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader(""))
	if exit != runner.ExitCode(runner.StatusBusy) {
		t.Fatalf("exit=%d, want %d; stderr=%s", exit, runner.ExitCode(runner.StatusBusy), stderr.String())
	}
	if !strings.Contains(stderr.String(), "already running") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if strings.Contains(stderr.String(), "no run state") {
		t.Fatalf("stderr should not mention missing run state when busy: %q", stderr.String())
	}
	if rv.calls != 0 {
		t.Fatalf("reviewer should not have been called while busy")
	}
}

func TestReviewCommandUsesIterTimeout(t *testing.T) {
	slug := "demo"
	homeDir, _, _, _ := newReviewFixture(t, slug)
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	t.Setenv("NIGHTSHIFT_ITER_TIMEOUT", "")

	if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir ready: %v", err)
	}
	specPath := filepath.Join(home.ReadyDir(homeDir), slug+".md")
	if err := os.WriteFile(specPath, []byte("---\nrepo: .\nverify: true\niter_timeout: 5m\n---\n\n# spec\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	t.Run("frontmatter iter_timeout", func(t *testing.T) {
		rv := &recordingReviewer{text: "VERDICT: approve\n"}
		withStubReviewer(t, rv)
		var stdout, stderr bytes.Buffer
		if exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader("")); exit != 0 {
			t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
		}
		if !rv.hasDeadline {
			t.Fatalf("reviewer ctx has no deadline")
		}
		want := time.Now().Add(5 * time.Minute)
		if d := rv.deadline.Sub(want); d < -time.Minute || d > time.Minute {
			t.Fatalf("deadline = %v, want within 1m of %v", rv.deadline, want)
		}
	})

	if err := os.Remove(specPath); err != nil {
		t.Fatalf("remove spec: %v", err)
	}

	t.Run("default iter_timeout", func(t *testing.T) {
		rv := &recordingReviewer{text: "VERDICT: approve\n"}
		withStubReviewer(t, rv)
		var stdout, stderr bytes.Buffer
		if exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader("")); exit != 0 {
			t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
		}
		if !rv.hasDeadline {
			t.Fatalf("reviewer ctx has no deadline")
		}
		want := time.Now().Add(30 * time.Minute)
		if d := rv.deadline.Sub(want); d < -time.Minute || d > time.Minute {
			t.Fatalf("deadline = %v, want within 1m of %v", rv.deadline, want)
		}
	})
}

func TestReviewCommandInvalidIterTimeout(t *testing.T) {
	slug := "demo"
	homeDir, _, _, _ := newReviewFixture(t, slug)
	t.Setenv("NIGHTSHIFT_HOME", homeDir)

	if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir ready: %v", err)
	}
	specPath := filepath.Join(home.ReadyDir(homeDir), slug+".md")
	if err := os.WriteFile(specPath, []byte("---\nrepo: .\nverify: true\niter_timeout: bogus\n---\n\n# spec\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	rv := &recordingReviewer{text: "VERDICT: approve\n"}
	withStubReviewer(t, rv)

	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"review", slug}, &stdout, &stderr, strings.NewReader(""))
	if exit != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%s stderr=%s", exit, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), `iter_timeout "bogus"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if rv.calls != 0 {
		t.Fatalf("reviewer called %d times, want 0", rv.calls)
	}
	matches, err := filepath.Glob(filepath.Join(home.ResultsDir(homeDir), slug+"-*-review*.md"))
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no review notes, got %v", matches)
	}
}

func TestUsageListsReview(t *testing.T) {
	if !strings.Contains(usage, "nightshift review <slug> [--model M]") {
		t.Fatalf("usage missing the review line:\n%s", usage)
	}
	if !strings.Contains(usage, "sandboxed Claude review of a run's work") {
		t.Fatalf("usage missing review's description:\n%s", usage)
	}
}
