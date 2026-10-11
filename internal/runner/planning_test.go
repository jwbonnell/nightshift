package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlannerFailureNotMarkedPlanned: the planner exits with an error on
// its first attempt, so Planned must stay false after that session. The
// next session (a second loop() call, standing in for a resume) retries
// planning with the planner model and this time succeeds.
func TestPlannerFailureNotMarkedPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			return errors.New("planner boom")
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "max_iter: 1\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Planned {
		t.Fatalf("Planned = true, want false after a failed planning iteration")
	}
	logData, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "planning iteration failed") {
		t.Fatalf("runner.log missing the planning-failure line: %s", logData)
	}

	status = r.loop()
	if status != StatusCapped {
		t.Fatalf("second session status = %v, want Capped", status)
	}
	if !r.st.Planned {
		t.Fatalf("Planned = false, want true once the retried planner succeeded")
	}
	if len(sb.models) != 2 || sb.models[0] != r.spec.Planner || sb.models[1] != r.spec.Planner {
		t.Fatalf("models = %v, want both entries to be the planner %q", sb.models, r.spec.Planner)
	}
}

// TestPlannerNoProgressNotPlanned: the planner exits cleanly but leaves
// PROGRESS.md untouched, which must not count as a successful plan.
func TestPlannerNoProgressNotPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.plannerNoWrite = true
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "max_iter: 1\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Planned {
		t.Fatalf("Planned = true, want false when PROGRESS.md didn't change")
	}
}

// TestPlannerFailsTwiceStalls: a planner that never makes progress stalls
// the run after two failed attempts, exactly like worker iterations.
func TestPlannerFailsTwiceStalls(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.plannerNoWrite = true
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "max_iter: 6\n", Options{}, sb)
	status := r.loop()

	if status != StatusStalled {
		t.Fatalf("status = %v, want Stalled", status)
	}
	if sb.agentN != 2 {
		t.Fatalf("agentN = %d, want exactly 2 (both planning attempts)", sb.agentN)
	}
	if len(sb.models) != 2 || sb.models[0] != r.spec.Planner || sb.models[1] != r.spec.Planner {
		t.Fatalf("models = %v, want both entries to be the planner %q", sb.models, r.spec.Planner)
	}
	if r.st.Planned {
		t.Fatalf("Planned = true, want false")
	}
}

// TestPlannerSuccessStillPlanned is today's happy path, unchanged: a
// planner that exits cleanly and writes PROGRESS.md is marked Planned and
// the next iteration uses the worker model.
func TestPlannerSuccessStillPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{}, sb)
	status := r.loop()

	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if !r.st.Planned {
		t.Fatalf("Planned = false, want true")
	}
	if len(sb.models) != 2 || sb.models[0] != r.spec.Planner || sb.models[1] != r.spec.Model {
		t.Fatalf("models = %v, want [planner, worker]", sb.models)
	}
}
