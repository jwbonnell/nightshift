package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailedAgentNoChangesSkipsVerify: a worker agent that always errors
// and leaves the clone untouched must not trigger verify — two such
// iterations stall the run exactly as a worker that makes no progress
// otherwise would, but without ever calling RunVerify.
func TestFailedAgentNoChangesSkipsVerify(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			return nil // planning iteration; the stub auto-writes PROGRESS.md
		}
		return errors.New("boom")
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "max_iter: 6\n", Options{}, sb)
	status := r.loop()

	if status != StatusStalled {
		t.Fatalf("status = %v, want Stalled", status)
	}
	if sb.verifyN != 0 {
		t.Fatalf("verifyN = %d, want 0", sb.verifyN)
	}
	logData, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "verify skipped (agent failed, no changes)") {
		t.Fatalf("runner.log missing the skip line: %s", logData)
	}
}

// TestFailedAgentWithCommitStillVerifies: an agent that fails after
// leaving a commit behind must still be verified — the commit is real
// work that needs checking, regardless of the agent's own exit status.
func TestFailedAgentWithCommitStillVerifies(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			return errors.New("boom")
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "max_iter: 2\n", Options{}, sb)
	r.loop()

	if sb.verifyN != 1 {
		t.Fatalf("verifyN = %d, want 1", sb.verifyN)
	}
	logData, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if strings.Contains(string(logData), "verify skipped") {
		t.Fatalf("runner.log should not mention a skip: %s", logData)
	}
}

// TestFailedAgentDirtyTreeStillVerifies: an agent that fails but leaves
// uncommitted changes on disk must still be verified, even though
// captureDirty resets the tree before verify actually runs.
func TestFailedAgentDirtyTreeStillVerifies(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			writeUncommitted(t, dir, "work.go", "package main\n")
			return errors.New("boom")
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "max_iter: 2\n", Options{}, sb)
	r.loop()

	if sb.verifyN != 1 {
		t.Fatalf("verifyN = %d, want 1", sb.verifyN)
	}
}
