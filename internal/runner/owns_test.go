package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOwnsViolationIsRed is the spec's owns check: a committed change
// outside owns makes an otherwise-green iteration red, resets to
// last_green, and logs/notes exactly why.
func TestOwnsViolationIsRed(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "owns: allowed.go\nmax_iter: 2\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Commits != 0 {
		t.Fatalf("Commits = %d, want 0 (reset to last green)", r.st.Commits)
	}
	if !strings.Contains(readFile(r.progressPath()), "iteration 2 changed work.go outside owns (allowed.go) — it was reset; stay inside owns or stop and ask") {
		t.Fatalf("PROGRESS.md missing the owns violation note: %s", readFile(r.progressPath()))
	}

	r.logFile.Close()
	logData, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "iter 2 changed paths outside owns: work.go") {
		t.Fatalf("runner.log missing the owns violation line: %s", logData)
	}
}

// TestOwnsUncommittedViolationIsRed is the same check for a path that is
// never committed: captureDirty wipes it from the working tree before
// verify runs, but the owns check still catches it from the porcelain
// status captured beforehand.
func TestOwnsUncommittedViolationIsRed(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			writeUncommitted(t, dir, "scratch.go", "package main\n")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "owns: allowed.go\nmax_iter: 2\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Commits != 0 {
		t.Fatalf("Commits = %d, want 0", r.st.Commits)
	}
	if !strings.Contains(readFile(r.progressPath()), "iteration 2 changed scratch.go outside owns (allowed.go) — it was reset; stay inside owns or stop and ask") {
		t.Fatalf("PROGRESS.md missing the owns violation note: %s", readFile(r.progressPath()))
	}
}

// TestOwnsAllowsScaffoldFiles confirms the owns check never flags the
// scaffold files, even one force-committed (commitFileForced) straight
// past .git/info/exclude by a misbehaving agent.
func TestOwnsAllowsScaffoldFiles(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			commitFileForced(t, dir, "REVIEW.md", "blocking: none\n", "oops, committed a scaffold file")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "owns: work.go\nmax_iter: 2\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Commits != 2 {
		t.Fatalf("Commits = %d, want 2 (not reset: REVIEW.md is scaffold-exempt)", r.st.Commits)
	}
	if strings.Contains(readFile(r.progressPath()), "outside owns") {
		t.Fatalf("PROGRESS.md has an owns violation note, want none: %s", readFile(r.progressPath()))
	}
}

// TestOwnsInsideCommits is the baseline: a change entirely inside owns
// proceeds exactly as it would with owns unset.
func TestOwnsInsideCommits(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "allowed.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "owns: allowed.go\n", Options{}, sb)
	status := r.loop()

	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if r.st.Commits != 1 {
		t.Fatalf("Commits = %d, want 1", r.st.Commits)
	}
	if strings.Contains(readFile(r.progressPath()), "outside owns") {
		t.Fatalf("PROGRESS.md has an owns violation note, want none: %s", readFile(r.progressPath()))
	}
}
