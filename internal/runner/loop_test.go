package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var errMockVerifyFail = errors.New("mock verify failure")

func TestGreenToComplete(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "", Options{}, sb)
	status := r.loop()

	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if r.st.Iterations != 2 {
		t.Fatalf("Iterations = %d, want 2", r.st.Iterations)
	}
	if r.st.Commits != 1 {
		t.Fatalf("Commits = %d, want 1", r.st.Commits)
	}
	if r.st.Status != "" {
		t.Fatalf("loop() itself should not set Status (that's finish, Task 10): got %q", r.st.Status)
	}
	if sb.resetVerifyN != sb.verifyN {
		t.Fatalf("resetVerifyN=%d != verifyN=%d, verify must run in a freshly reset sandbox every time", sb.resetVerifyN, sb.verifyN)
	}
}

func TestPlannerModelFirstIteration(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "", Options{}, sb)
	status := r.loop()

	if status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	if len(sb.models) != 2 {
		t.Fatalf("models = %v, want 2 entries", sb.models)
	}
	if sb.models[0] != r.spec.Planner {
		t.Fatalf("iter1 model = %q, want planner %q", sb.models[0], r.spec.Planner)
	}
	if sb.models[1] != r.spec.Model {
		t.Fatalf("iter2 model = %q, want iterator model %q", sb.models[1], r.spec.Model)
	}
}

// TestPlannerCommitResetsToLastGreenNotBase is D11.1: the reference resets
// a planning iteration's violating commit to base; nightshift resets to
// last_green instead. On a fresh run these coincide, so this test first
// advances last_green past base (as a resume would have) before forcing a
// fresh planning pass.
func TestPlannerCommitResetsToLastGreenNotBase(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			commitFile(t, dir, "violation.go", "oops\n", "planner violation")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "", Options{}, sb)

	commitFile(t, r.clone, "earlier.go", "package main\n", "earlier green work")
	greenSHA := mustGit(t, r.clone, "rev-parse", "HEAD")
	r.st.LastGreen = greenSHA
	r.st.Planned = false
	r.spec.MaxIter = 1

	status := r.loop()
	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.LastGreen != greenSHA {
		t.Fatalf("last_green changed: %q -> %q", greenSHA, r.st.LastGreen)
	}
	head := mustGit(t, r.clone, "rev-parse", "HEAD")
	if head != greenSHA {
		t.Fatalf("HEAD = %s, want reset back to last_green %s (D11.1), not base", short(head), short(greenSHA))
	}
	if !strings.Contains(readFile(r.progressPath()), "planning iteration committed") {
		t.Fatalf("PROGRESS.md missing the violation note: %s", readFile(r.progressPath()))
	}
}

func TestRedResetsToLastGreen(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "bad\n", "bad work")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error {
		if iter == 2 {
			return errMockVerifyFail
		}
		return nil
	}

	r := newTestRun(t, repo, "max_iter: 2\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Commits != 0 {
		t.Fatalf("Commits = %d, want 0 (reset to last green/base)", r.st.Commits)
	}
	if !fileExistsT(filepath.Join(r.attemptsDir(), "iter-2.patch")) {
		t.Fatalf("iter-2.patch missing")
	}
	if !fileExistsT(filepath.Join(r.attemptsDir(), "iter-2.log")) {
		t.Fatalf("iter-2.log missing")
	}
	if !strings.Contains(readFile(r.progressPath()), "reset to last green") {
		t.Fatalf("PROGRESS.md missing the reset note: %s", readFile(r.progressPath()))
	}
}

func TestTwoRedsStops(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 || iter == 3 {
			commitFile(t, dir, "work.go", "bad\n", "bad work")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error {
		if iter == 2 || iter == 3 {
			return errMockVerifyFail
		}
		return nil
	}

	r := newTestRun(t, repo, "", Options{}, sb)
	status := r.loop()

	if status != StatusRed {
		t.Fatalf("status = %v, want Red", status)
	}
	if r.st.Commits != 0 {
		t.Fatalf("Commits = %d, want 0", r.st.Commits)
	}
}

func TestDirtyTreeCaptured(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			writeUncommitted(t, dir, "scratch.txt", "leftover\n")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "max_iter: 1\n", Options{}, sb)
	r.loop()

	if !fileExistsT(filepath.Join(r.attemptsDir(), "iter-1.dirty.patch")) {
		t.Fatalf("iter-1.dirty.patch missing")
	}
	out := mustGit(t, r.clone, "status", "--porcelain")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("working tree not clean after captureDirty: %q", out)
	}
}

func TestFalseCompleteRejected(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			touchComplete(t, dir) // no commit alongside it
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "max_iter: 2\n", Options{}, sb)
	status := r.loop()

	if status == StatusComplete {
		t.Fatalf("status = Complete, want rejected (no commits)")
	}
	if fileExistsT(r.completePath()) {
		t.Fatalf(".spec-complete should have been removed")
	}
	if !strings.Contains(readFile(r.progressPath()), "rejected .spec-complete") {
		t.Fatalf("PROGRESS.md missing the rejection note: %s", readFile(r.progressPath()))
	}
}

func TestStallStops(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "max_iter: 6\n", Options{}, sb)
	status := r.loop()

	if status != StatusStalled {
		t.Fatalf("status = %v, want Stalled", status)
	}
	if sb.agentN != 3 {
		t.Fatalf("agentN = %d, want exactly 3 (1 planning + 2 no-progress iterations)", sb.agentN)
	}
}

func TestCapStops(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter > 1 {
			appendProgressRaw(t, dir, "progress from iter "+strconv.Itoa(iter))
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "max_iter: 3\n", Options{}, sb)
	status := r.loop()

	if status != StatusCapped {
		t.Fatalf("status = %v, want Capped", status)
	}
	if r.st.Iterations != 3 {
		t.Fatalf("Iterations = %d, want 3", r.st.Iterations)
	}
}

func TestIterationTimeout(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "iter_timeout: 20ms\nmax_iter: 5\n", Options{}, sb)

	done := make(chan Status, 1)
	go func() { done <- r.loop() }()
	select {
	case status := <-done:
		if status != StatusStalled {
			t.Fatalf("status = %v, want Stalled", status)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("loop() did not finish within 5s")
	}
}

func TestUntilWindow(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()

	now := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			writeUncommitted(t, dir, "scratch.txt", "leftover\n")
			now = now.Add(2 * time.Hour) // push past the 06:00 deadline mid-iteration
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	opts := Options{Until: "06:00"}
	opts.clock = func() time.Time { return now }
	r := newTestRun(t, repo, "", opts, sb)

	status := r.loop()
	if status != StatusWindowClosed {
		t.Fatalf("status = %v, want WindowClosed", status)
	}
	if !fileExistsT(filepath.Join(r.attemptsDir(), "iter-2.dirty.patch")) {
		t.Fatalf("iter-2.dirty.patch missing")
	}
}

func TestQuestionBlocks(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			writeQuestion(t, dir, "should I use X or Y?")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "", Options{}, sb)
	status := r.loop()

	if status != StatusBlocked {
		t.Fatalf("status = %v, want Blocked", status)
	}
}

func TestAgentKilledBeforeVerify(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "iter_timeout: 20ms\nmax_iter: 2\n", Options{}, sb)
	r.loop()

	if sb.verifyRaced {
		t.Fatalf("verify ran while the agent still looked alive")
	}
	if sb.stopN != sb.agentN {
		t.Fatalf("stopN=%d != agentN=%d, StopAgent must run after every RunAgent", sb.stopN, sb.agentN)
	}
}

func TestRunnerLogIsJSON(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "", Options{}, sb)
	if status := r.loop(); status != StatusComplete {
		t.Fatalf("status = %v, want Complete", status)
	}
	r.logFile.Close()

	data, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}

	foundIterAttr := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line is not valid JSON: %q: %v", line, err)
		}
		if rec["service"] != "nightshift" {
			t.Fatalf("record missing service=nightshift: %v", rec)
		}
		if _, ok := rec["msg"]; !ok {
			t.Fatalf("record missing msg: %v", rec)
		}
		if _, ok := rec["slug"]; !ok {
			t.Fatalf("record missing slug: %v", rec)
		}
		if msg, _ := rec["msg"].(string); strings.Contains(msg, "verify GREEN") {
			if _, ok := rec["iter"]; !ok {
				t.Fatalf("verify GREEN record missing iter attr: %v", rec)
			}
			foundIterAttr = true
		}
	}
	if !foundIterAttr {
		t.Fatalf("no 'verify GREEN' record found in runner.log")
	}
	if strings.Contains(string(data), "AGENT RAW OUTPUT") {
		t.Fatalf("raw agent output leaked into runner.log")
	}
}

func TestAgentLogSeparate(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.verify = func(iter int, dir string) error { return nil }

	r := newTestRun(t, repo, "max_iter: 1\n", Options{}, sb)
	r.loop()
	r.agentLog.Close()

	data, err := os.ReadFile(filepath.Join(r.dir, "agent.log"))
	if err != nil {
		t.Fatalf("read agent.log: %v", err)
	}
	if !strings.Contains(string(data), "=== iter 1 (session 1)") {
		t.Fatalf("agent.log missing the iteration header: %q", data)
	}
	if !strings.Contains(string(data), "AGENT RAW OUTPUT") {
		t.Fatalf("agent.log missing the raw agent output: %q", data)
	}
}

func TestScaffoldSymlinkRejected(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	r := newTestRun(t, repo, "", Options{}, sb)

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	if err := os.Symlink(secret, r.questionPath()); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if got := r.scaffoldRead(r.questionPath()); got != "" {
		t.Fatalf("scaffoldRead followed the symlink: %q", got)
	}
	r.logFile.Close()

	data, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}

	found := false
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line is not valid JSON: %q: %v", line, err)
		}
		if rec["level"] == "WARN" {
			if msg, _ := rec["msg"].(string); strings.Contains(msg, "not a regular file") {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no WARN record mentioning 'not a regular file' in runner.log: %s", data)
	}

	secretData, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(secretData) != "do not leak" {
		t.Fatalf("secret file was modified: %q", secretData)
	}
}
