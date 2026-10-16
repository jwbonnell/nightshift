package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

func completingAgent(t *testing.T) func(ctx context.Context, iter int, dir string) error {
	return func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
}

func alwaysGreenVerify(iter int, dir string) error { return nil }

func TestDryRunCreatesNothing(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	var stdout, stderr bytes.Buffer
	opts := Options{SpecArg: "demo", Home: homeDir, DryRun: true, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if err != nil || status != statusDryRun {
		t.Fatalf("status=%v err=%v, want dry-run/nil", status, err)
	}

	stateRoot := home.RunsDir(homeDir)
	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("dry-run created a state dir")
	}
	if refExists(repo, "refs/heads/spec/demo") {
		t.Fatalf("dry-run created a branch")
	}
	if !strings.Contains(stdout.String(), "home:        "+homeDir) {
		t.Fatalf("dry-run output missing the home: line:\n%s", stdout.String())
	}
}

func TestDryRunShowsHeldLocks(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	otherLock, err := lock.TryAcquire(specLockPath(stateRoot, "other"), "other")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer otherLock.Release()
	slotLock, err := lock.TryAcquire(slotLockPath(stateRoot, 1), "other")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer slotLock.Release()

	var stdout, stderr bytes.Buffer
	opts := Options{SpecArg: "demo", Home: homeDir, StateRoot: stateRoot, DryRun: true, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if err != nil || status != statusDryRun {
		t.Fatalf("status=%v err=%v, want dry-run/nil", status, err)
	}

	out := stdout.String()
	if !strings.Contains(out, "spec-other.lock") {
		t.Fatalf("output missing spec-other.lock:\n%s", out)
	}
	if !strings.Contains(out, "slot-1.lock") {
		t.Fatalf("output missing slot-1.lock:\n%s", out)
	}
	if !strings.Contains(out, fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("output missing holder pid:\n%s", out)
	}

	if _, held := lock.Probe(specLockPath(stateRoot, "demo")); held {
		t.Fatalf("dry-run should not take a spec lock for demo")
	}
}

func TestSameSpecRefused(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	l, err := lock.TryAcquire(specLockPath(stateRoot, "demo"), "demo")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	var stdout, stderr bytes.Buffer
	opts := Options{SpecArg: "demo", Home: homeDir, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if status != StatusBusy || err != nil {
		t.Fatalf("status=%v err=%v, want Busy/nil", status, err)
	}
	if !strings.Contains(stderr.String(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Fatalf("stderr missing holder pid: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), specLockPath(stateRoot, "demo")) {
		t.Fatalf("stderr missing lock path: %q", stderr.String())
	}
	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("a refused run should not create a state dir")
	}
}

func TestLockPreventsConcurrentRuns(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	l, err := lock.TryAcquire(specLockPath(stateRoot, "demo"), "demo")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}

	opts := Options{SpecArg: "demo", Home: homeDir, Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if status != StatusBusy || err != nil {
		t.Fatalf("status=%v err=%v, want Busy/nil", status, err)
	}

	l.Release()

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	opts.Sandbox = sb
	opts.Notifier = noopNotifierForTest{}
	status2, err2 := Run(opts)
	if status2 != StatusComplete || err2 != nil {
		t.Fatalf("after release: status=%v err=%v, want Complete/nil", status2, err2)
	}
}

func TestFreshKeepsLock(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	l, err := lock.TryAcquire(specLockPath(stateRoot, "demo"), "demo")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	opts := Options{
		SpecArg: "demo", Home: homeDir, Fresh: true, Yes: true,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
	}
	status, err := Run(opts)
	if status != StatusBusy || err != nil {
		t.Fatalf("status=%v err=%v, want Busy/nil", status, err)
	}
	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("--fresh must not touch anything while the spec lock is held")
	}
}

func TestSlotCap(t *testing.T) {
	t.Run("cap 2: two running makes a third busy, naming both", func(t *testing.T) {
		stateRoot := t.TempDir()
		la, err := lock.TryAcquire(slotLockPath(stateRoot, 1), "a")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		defer la.Release()
		lb, err := lock.TryAcquire(slotLockPath(stateRoot, 2), "b")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		defer lb.Release()

		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "c", "", repo)

		var stderr bytes.Buffer
		opts := Options{SpecArg: "c", Home: homeDir, StateRoot: stateRoot, Stdout: io.Discard, Stderr: &stderr, Stdin: strings.NewReader("")}
		status, err := Run(opts)
		if status != StatusBusy || err != nil {
			t.Fatalf("status=%v err=%v, want Busy/nil", status, err)
		}
		if !strings.Contains(stderr.String(), "a") || !strings.Contains(stderr.String(), "b") {
			t.Fatalf("stderr missing running slugs: %q", stderr.String())
		}
	})

	t.Run("cap 1: a different spec is still busy", func(t *testing.T) {
		stateRoot := t.TempDir()
		la, err := lock.TryAcquire(slotLockPath(stateRoot, 1), "a")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		defer la.Release()

		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "d", "", repo)

		opts := Options{
			SpecArg: "d", Home: homeDir, StateRoot: stateRoot, MaxConcurrent: "1",
			Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
		}
		status, err := Run(opts)
		if status != StatusBusy || err != nil {
			t.Fatalf("status=%v err=%v, want Busy/nil", status, err)
		}
	})
}

func TestCapPrecedence(t *testing.T) {
	homeDir := t.TempDir()

	if n, src, err := resolveCap(Options{Home: homeDir}); err != nil || n != 2 || src != "default" {
		t.Fatalf("default: n=%d src=%q err=%v", n, src, err)
	}

	if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_MAX_CONCURRENT=5\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if n, src, err := resolveCap(Options{Home: homeDir}); err != nil || n != 5 || src != "config" {
		t.Fatalf("config: n=%d src=%q err=%v", n, src, err)
	}

	t.Setenv("NIGHTSHIFT_MAX_CONCURRENT", "7")
	if n, src, err := resolveCap(Options{Home: homeDir}); err != nil || n != 7 || src != "env" {
		t.Fatalf("env: n=%d src=%q err=%v", n, src, err)
	}

	if n, src, err := resolveCap(Options{Home: homeDir, MaxConcurrent: "3"}); err != nil || n != 3 || src != "flag" {
		t.Fatalf("flag: n=%d src=%q err=%v", n, src, err)
	}

	for _, bad := range []string{"0", "-1", "abc"} {
		if _, _, err := resolveCap(Options{Home: homeDir, MaxConcurrent: bad}); err == nil {
			t.Fatalf("--max-concurrent=%q: expected an error", bad)
		}
	}

	t.Setenv("NIGHTSHIFT_MAX_CONCURRENT", "nope")
	if _, _, err := resolveCap(Options{Home: homeDir}); err == nil {
		t.Fatalf("bad env value: expected an error")
	}
}

func TestNoSandboxNoRun(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.avail = false

	opts := Options{SpecArg: "demo", Home: homeDir, Sandbox: sb, Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if status != StatusError || err == nil {
		t.Fatalf("status=%v err=%v, want Error/non-nil", status, err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "sandbox") {
		t.Fatalf("error does not mention the sandbox: %q", err.Error())
	}
	if sb.agentN != 0 {
		t.Fatalf("agent ran despite the sandbox being unavailable")
	}
}

// TestSelectorErrorBeforeSetup checks that a bad model selector fails
// resolution before anything is created: no sandbox Create call, and no
// state dir for the spec.
func TestSelectorErrorBeforeSetup(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "model: foo/bar\n", repo)

	sb := newStubSandbox()
	opts := Options{SpecArg: "demo", Home: homeDir, Sandbox: sb, Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if status != StatusError || err == nil {
		t.Fatalf("status=%v err=%v, want Error/non-nil", status, err)
	}
	if !strings.Contains(err.Error(), "model (from frontmatter): unknown agent") {
		t.Fatalf("err = %q, want it to contain %q", err.Error(), "model (from frontmatter): unknown agent")
	}
	if sb.mountDir != "" {
		t.Fatalf("sandbox Create was called despite the bad selector")
	}
	stateRoot := home.RunsDir(homeDir)
	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("a state dir was created despite the bad selector")
	}
}

func TestRerunCompleteRefuses(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("first run: status=%v err=%v", status, err)
	}

	sb2 := newStubSandbox()
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if status2 != StatusError || err2 == nil {
		t.Fatalf("rerun: status=%v err=%v, want Error/non-nil", status2, err2)
	}
	if !strings.Contains(err2.Error(), "specs/done/") || !strings.Contains(err2.Error(), "--fresh") {
		t.Fatalf("err = %q, missing expected hints", err2.Error())
	}
	if sb2.agentN != 0 {
		t.Fatalf("agent ran on a refused rerun")
	}

	sb3 := newStubSandbox()
	sb3.agent = completingAgent(t)
	sb3.verify = alwaysGreenVerify
	opts3 := opts
	opts3.Sandbox = sb3
	opts3.Fresh = true
	opts3.Yes = true
	status3, err3 := Run(opts3)
	if err3 != nil || status3 != StatusComplete {
		t.Fatalf("fresh rerun: status=%v err=%v", status3, err3)
	}
}

func TestLocksReleased(t *testing.T) {
	repo := newSourceRepo(t)
	cases := []struct {
		name       string
		agent      func(ctx context.Context, iter int, dir string) error
		verify     func(iter int, dir string) error
		wantStatus Status
	}{
		{"complete", completingAgent(t), alwaysGreenVerify, StatusComplete},
		{"blocked", func(ctx context.Context, iter int, dir string) error {
			if iter == 2 {
				writeQuestion(t, dir, "q?")
			}
			return nil
		}, alwaysGreenVerify, StatusBlocked},
		{"red", func(ctx context.Context, iter int, dir string) error {
			if iter == 2 || iter == 3 {
				commitFile(t, dir, "work.go", "bad\n", "bad")
			}
			return nil
		}, func(iter int, dir string) error {
			if iter == 2 || iter == 3 {
				return errMockVerifyFail
			}
			return nil
		}, StatusRed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeDir := t.TempDir()
			writeReadySpec(t, homeDir, tc.name, "", repo)
			sb := newStubSandbox()
			sb.agent = tc.agent
			sb.verify = tc.verify

			opts := Options{
				SpecArg: tc.name, Home: homeDir, Sandbox: sb,
				Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
			}
			status, _ := Run(opts)
			if status != tc.wantStatus {
				t.Fatalf("status = %v, want %v", status, tc.wantStatus)
			}

			stateRoot := home.RunsDir(homeDir)
			entries, err := os.ReadDir(filepath.Join(stateRoot, ".locks"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("readdir .locks: %v", err)
			}
			for _, e := range entries {
				name := e.Name()
				if strings.HasPrefix(name, "spec-") || strings.HasPrefix(name, "slot-") {
					t.Fatalf("lock file left behind: %s", name)
				}
			}
		})
	}
}

func TestNextPicksOldestEligible(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "b", "created: 2026-01-02T00:00:00Z\n", repo)
	writeReadySpec(t, homeDir, "a", "created: 2026-01-01T00:00:00Z\n", repo)

	path, _, err := selectNext(Options{Home: homeDir})
	if err != nil {
		t.Fatalf("selectNext: %v", err)
	}
	if !strings.HasSuffix(path, string(os.PathSeparator)+"a.md") {
		t.Fatalf("selectNext = %q, want the oldest-created spec (a.md)", path)
	}
}

func TestNextSkipsTerminalUnchanged(t *testing.T) {
	repo := newSourceRepo(t)
	for _, status := range []string{"blocked", "complete"} {
		t.Run(status, func(t *testing.T) {
			homeDir := t.TempDir()
			path := writeReadySpec(t, homeDir, "demo", "", repo)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read spec: %v", err)
			}
			sum := sha256.Sum256(data)
			hash := hex.EncodeToString(sum[:])

			stateRoot := home.RunsDir(homeDir)
			stDir := filepath.Join(stateRoot, "demo")
			if err := os.MkdirAll(stDir, 0o755); err != nil {
				t.Fatalf("mkdir: %v", err)
			}
			runJSON := fmt.Sprintf(`{"slug":"demo","spec_hash":%q,"status":%q}`, hash, status)
			if err := os.WriteFile(filepath.Join(stDir, "run.json"), []byte(runJSON), 0o644); err != nil {
				t.Fatalf("write run.json: %v", err)
			}

			cands, _, err := selectNextCandidates(Options{Home: homeDir, StateRoot: stateRoot})
			if err != nil {
				t.Fatalf("selectNextCandidates: %v", err)
			}
			if len(cands) != 0 {
				t.Fatalf("%s+unchanged: expected 0 candidates, got %v", status, cands)
			}

			writeReadySpec(t, homeDir, "demo", "model: edited\n", repo)
			cands, _, err = selectNextCandidates(Options{Home: homeDir, StateRoot: stateRoot})
			if err != nil {
				t.Fatalf("selectNextCandidates: %v", err)
			}
			if len(cands) != 1 {
				t.Fatalf("%s+edited: expected 1 candidate, got %v", status, cands)
			}
		})
	}
}

// TestRunNoFreeSlot covers runLocked's own "no free slot ... running"
// message on a direct `run <spec>` (no --next) when every concurrency
// slot is already held — the scheduler has no equivalent of this
// precheck (it relies on runLocked's slot acquisition when it spawns a
// direct child), so this is the only remaining coverage for the message.
func TestRunNoFreeSlot(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	stateRoot := home.RunsDir(homeDir)

	l1, err := lock.TryAcquire(slotLockPath(stateRoot, 1), "x")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l1.Release()
	l2, err := lock.TryAcquire(slotLockPath(stateRoot, 2), "y")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l2.Release()

	var stderr bytes.Buffer
	opts := Options{SpecArg: "a", Home: homeDir, StateRoot: stateRoot, Stdout: io.Discard, Stderr: &stderr, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if status != StatusBusy || err != nil {
		t.Fatalf("status=%v err=%v, want Busy/nil", status, err)
	}
	if !strings.Contains(stderr.String(), "x") || !strings.Contains(stderr.String(), "y") {
		t.Fatalf("stderr missing running labels: %q", stderr.String())
	}
}

func TestNextNothingEligible(t *testing.T) {
	homeDir := t.TempDir()
	var stdout bytes.Buffer
	opts := Options{Next: true, Home: homeDir, Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if err != nil || status != statusNothing {
		t.Fatalf("status=%v err=%v, want nothing-eligible/nil", status, err)
	}
	if !strings.Contains(stdout.String(), "no eligible spec in specs/ready/") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

// TestRunRefusesUnmetDeps covers a direct `run <spec>` refusal: a known
// but unsatisfied dependency fails with the documented message, exit 1
// (StatusError), and nothing is created — no state dir, no spec lock.
func TestRunRefusesUnmetDeps(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "other", "", repo)
	writeReadySpec(t, homeDir, "demo", "depends_on: other\n", repo)
	stateRoot := home.RunsDir(homeDir)

	opts := Options{
		SpecArg: "demo", Home: homeDir, StateRoot: stateRoot,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
	}
	status, err := Run(opts)
	if status != StatusError || err == nil {
		t.Fatalf("status=%v err=%v, want StatusError with an error", status, err)
	}
	want := "demo waits on other (not started) — run it first, or pass --ignore-deps"
	if err.Error() != want {
		t.Fatalf("err = %q, want %q", err.Error(), want)
	}
	if ExitCode(status) != 1 {
		t.Fatalf("ExitCode(%v) = %d, want 1", status, ExitCode(status))
	}

	if dirExists(filepath.Join(stateRoot, "demo")) {
		t.Fatalf("state dir for demo should not have been created")
	}
	if _, held := lock.Probe(specLockPath(stateRoot, "demo")); held {
		t.Fatalf("spec lock for demo should not be held")
	}
}

// TestIgnoreDeps proves --ignore-deps lets a spec with an unsatisfied (but
// known) dependency run to completion anyway.
func TestIgnoreDeps(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "other", "", repo)
	writeReadySpec(t, homeDir, "demo", "depends_on: other\n", repo)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", IgnoreDeps: true, Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}
}

// TestNextSkipsWaiting covers --next passing over a spec with an unmet
// dependency: the real run skips straight to an eligible spec, and the
// dry-run path prints a "skip" line for the waiting one before the plan.
func TestNextSkipsWaiting(t *testing.T) {
	t.Run("real run skips the waiting spec", func(t *testing.T) {
		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "a", "created: 2026-01-01T00:00:00Z\n", repo)
		writeReadySpec(t, homeDir, "b", "created: 2026-01-02T00:00:00Z\ndepends_on: a\n", repo)
		stateRoot := home.RunsDir(homeDir)

		// a ends non-complete (red), so b — gated on a — stays waiting and
		// is never started in this invocation.
		scripts := map[string]childScript{"a": {exit: 3, runStatus: "red"}}
		var calls []string
		var callsMu sync.Mutex
		spawn := fakeSchedulerSpawner(t, stateRoot, scripts, &calls, &callsMu)

		opts := Options{
			Next: true, Home: homeDir, StateRoot: stateRoot,
			Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
		}
		if _, err := Run(opts); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(calls) != 1 || calls[0] != "a" {
			t.Fatalf("calls = %v, want exactly [a]", calls)
		}
	})

	t.Run("dry-run prints a skip line", func(t *testing.T) {
		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeReadySpec(t, homeDir, "a", "created: 2026-01-01T00:00:00Z\n", repo)
		writeReadySpec(t, homeDir, "b", "created: 2026-01-02T00:00:00Z\ndepends_on: a\n", repo)

		var stdout bytes.Buffer
		opts := Options{
			Next: true, DryRun: true, Home: homeDir,
			Stdout: &stdout, Stderr: io.Discard, Stdin: strings.NewReader(""),
		}
		status, err := Run(opts)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		if status != statusDryRun {
			t.Fatalf("status = %v, want dry-run", status)
		}
		if !strings.Contains(stdout.String(), "skip  b: waits on a (not started)\n") {
			t.Fatalf("missing skip line:\n%s", stdout.String())
		}
	})
}

// TestUppercaseMDSpecFullRun completes TestUppercaseMDSpec's (spec_test.go)
// resolveSpec-level coverage with a full run: found by a lowercase guess
// and by --next, with the slug (and so the lock/state-dir name, which
// share sp.Slug) resolved to DEMO, completing successfully.
func TestUppercaseMDSpecFullRun(t *testing.T) {
	t.Run("found by name", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:01s +0000")
		t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:01s +0000")
		repo := newSourceRepo(t)
		content := "---\nrepo: " + repo + "\nverify: true\n---\n\n# demo\n"
		homeDir := t.TempDir()
		if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home.ReadyDir(homeDir), "DEMO.MD"), []byte(content), 0o644); err != nil {
			t.Fatalf("write spec: %v", err)
		}

		sb := newStubSandbox()
		sb.agent = completingAgent(t)
		sb.verify = alwaysGreenVerify
		opts := Options{
			SpecArg: "demo", Home: homeDir, Sandbox: sb,
			Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		}
		status, err := Run(opts)
		if err != nil || status != StatusComplete {
			t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
		}

		stateRoot := home.RunsDir(homeDir)
		if !dirExists(filepath.Join(stateRoot, "DEMO")) {
			t.Fatalf("expected the state dir (and so the lock label) to be DEMO")
		}
	})

	t.Run("found by --next", func(t *testing.T) {
		t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:02s +0000")
		t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:02s +0000")
		repo := newSourceRepo(t)
		content := "---\nrepo: " + repo + "\nverify: true\n---\n\n# demo\n"
		homeDir := t.TempDir()
		if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(home.ReadyDir(homeDir), "DEMO.MD"), []byte(content), 0o644); err != nil {
			t.Fatalf("write spec: %v", err)
		}
		stateRoot := home.RunsDir(homeDir)

		var calls [][]string
		var callsMu sync.Mutex
		spawn := func(_ context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
			callsMu.Lock()
			calls = append(calls, args)
			callsMu.Unlock()
			return &scriptedChildWaiter{t: t, stateRoot: stateRoot, slug: slugFromRunArgs(args), sc: childScript{exit: 0, runStatus: "complete"}}, nil
		}
		opts := Options{
			Next: true, Home: homeDir, StateRoot: stateRoot,
			Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
		}
		if _, err := Run(opts); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if len(calls) != 1 {
			t.Fatalf("calls = %v, want exactly one spawn", calls)
		}
		if !strings.HasSuffix(calls[0][1], "DEMO.MD") {
			t.Fatalf("calls[0] = %v, want args[1] to end in DEMO.MD", calls[0])
		}
		if got := slugFromRunArgs(calls[0]); got != "DEMO" {
			t.Fatalf("slugFromRunArgs(calls[0]) = %q, want DEMO", got)
		}
	})
}

func TestConcurrentDifferentSpecs(t *testing.T) {
	homeDir := t.TempDir()
	repoA := newSourceRepo(t)
	repoB := newSourceRepo(t)
	writeReadySpec(t, homeDir, "a", "", repoA)
	writeReadySpec(t, homeDir, "b", "", repoB)

	runSlug := func(slug string) (Status, error) {
		sb := newStubSandbox()
		sb.agent = completingAgent(t)
		sb.verify = alwaysGreenVerify
		opts := Options{
			SpecArg: slug, Home: homeDir, Sandbox: sb,
			Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		}
		return Run(opts)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	results := map[string]Status{}
	for _, slug := range []string{"a", "b"} {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			status, err := runSlug(slug)
			if err != nil {
				t.Errorf("%s: %v", slug, err)
			}
			mu.Lock()
			results[slug] = status
			mu.Unlock()
		}(slug)
	}
	wg.Wait()

	for _, slug := range []string{"a", "b"} {
		if results[slug] != StatusComplete {
			t.Fatalf("%s status = %v, want Complete", slug, results[slug])
		}
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil {
		t.Fatalf("readdir results: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 result notes, got %d", len(entries))
	}
}

func TestConcurrentSameRepo(t *testing.T) {
	homeDir := t.TempDir()
	repo := newSourceRepo(t)
	writeReadySpec(t, homeDir, "a", "", repo)
	writeReadySpec(t, homeDir, "b", "", repo)
	before := mustGit(t, repo, "rev-parse", "HEAD")

	runSlug := func(slug string) (Status, error) {
		sb := newStubSandbox()
		sb.agent = completingAgent(t)
		sb.verify = alwaysGreenVerify
		opts := Options{
			SpecArg: slug, Home: homeDir, Sandbox: sb,
			Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		}
		return Run(opts)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	results := map[string]Status{}
	for _, slug := range []string{"a", "b"} {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			status, err := runSlug(slug)
			if err != nil {
				t.Errorf("%s: %v", slug, err)
			}
			mu.Lock()
			results[slug] = status
			mu.Unlock()
		}(slug)
	}
	wg.Wait()

	for _, slug := range []string{"a", "b"} {
		if results[slug] != StatusComplete {
			t.Fatalf("%s status = %v, want Complete", slug, results[slug])
		}
	}

	after := mustGit(t, repo, "rev-parse", "HEAD")
	if before != after {
		t.Fatalf("source repo HEAD moved: %s -> %s", before, after)
	}
	for _, slug := range []string{"a", "b"} {
		if !refExists(repo, "refs/heads/spec/"+slug) {
			t.Fatalf("expected refs/heads/spec/%s to exist in the source repo", slug)
		}
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil {
		t.Fatalf("readdir results: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 result notes, got %d", len(entries))
	}
}
