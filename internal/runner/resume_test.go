package runner

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

// TestProgressStampsUseWallClock is D11.9: appendProgress stamps with the
// live wall clock at write time, not the run's frozen start time — so two
// PROGRESS.md lines written at different points in one run, with an
// advancing fake clock, carry different minutes.
func TestProgressStampsUseWallClock(t *testing.T) {
	repo := newSourceRepo(t)

	now := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			now = now.Add(2 * time.Minute)
			commitFile(t, dir, "work.go", "bad\n", "bad")
		}
		if iter == 3 {
			now = now.Add(3 * time.Minute)
			commitFile(t, dir, "work.go", "bad again\n", "bad again")
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error {
		if iter == 2 || iter == 3 {
			return errMockVerifyFail
		}
		return nil
	}

	opts := Options{Stdout: io.Discard}
	opts.clock = func() time.Time { return now }

	r := newTestRun(t, repo, "", opts, sb)
	r.loop()
	r.logFile.Close()

	progress := readFile(r.progressPath())
	var minutes []string
	for _, line := range strings.Split(progress, "\n") {
		if strings.HasPrefix(line, "- ") && len(line) >= 7 {
			minutes = append(minutes, line[2:18]) // "2006-01-02 15:04"
		}
	}
	if len(minutes) < 2 {
		t.Fatalf("expected at least 2 timestamped PROGRESS.md lines, got %v", minutes)
	}
	allSame := true
	for _, m := range minutes[1:] {
		if m != minutes[0] {
			allSame = false
		}
	}
	if allSame {
		t.Fatalf("all PROGRESS.md timestamps are identical (%v), want the advancing wall clock reflected", minutes)
	}
}

// TestPlannerCommitOnResumeResetsToLastGreen is D11.1's checklist test,
// through two real Run() invocations (not just two loop() calls on one
// *run): run1 leaves last_green ahead of base; editing the spec forces a
// fresh planning pass on resume, whose violating commit must reset to
// that last_green, not to base, and the run then completes.
func TestPlannerCommitOnResumeResetsToLastGreen(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 2\n", repo)
	stateRoot := home.RunsDir(homeDir)
	clone := filepath.Join(stateRoot, "demo", "repo")

	sb1 := newStubSandbox()
	sb1.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "legit.go", "package main\n", "legit work")
		}
		return nil
	}
	sb1.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}
	greenSHA := mustGit(t, clone, "rev-parse", "HEAD")

	// Edit the spec: its content hash changes, forcing a fresh planning
	// pass on resume even though a planning pass already ran once.
	writeReadySpec(t, homeDir, "demo", "max_iter: 3\nmodel: sonnet\n", repo)

	sb2 := newStubSandbox()
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		switch iter {
		case 1:
			commitFile(t, dir, "violation.go", "oops\n", "planner violation")
		case 2:
			commitFile(t, dir, "final.go", "package main\n", "final work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("run2: status=%v err=%v, want Complete/nil", status2, err2)
	}

	log := mustGit(t, clone, "log", "--oneline", "--all")
	if !strings.Contains(log, "legit work") {
		t.Fatalf("expected the earlier green commit to survive: %s", log)
	}

	finalSHA := mustGit(t, clone, "rev-parse", "HEAD")
	parent := mustGit(t, clone, "rev-parse", finalSHA+"^")
	if parent != greenSHA {
		t.Fatalf("final commit's parent = %s, want last_green %s (D11.1: reset to last_green, not base)", short(parent), short(greenSHA))
	}
}

// TestResumeContinues: a resume with an unchanged spec hash uses the
// iterator model first (no fresh planning pass) and reuses the existing
// clone; after a spec edit, the first model on resume is the planner's.
func TestResumeContinues(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\n", repo)
	stateRoot := home.RunsDir(homeDir)
	clone := filepath.Join(stateRoot, "demo", "repo")

	sb1 := newStubSandbox()
	sb1.agent = func(ctx context.Context, iter int, dir string) error {
		writeUncommitted(t, dir, "ignored.txt", "x\n") // harmless no-op, no commit
		return nil
	}
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}
	if !fileExistsT(filepath.Join(clone, "ignored.txt")) {
		// resetHard would have removed an uncommitted file; this just
		// confirms the clone itself still exists for the resume to reuse.
	}

	sb2 := newStubSandbox()
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			commitFile(t, dir, "step1.go", "package main\n", "step1")
		}
		return nil
	}
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusCapped {
		t.Fatalf("resume (unchanged hash): status=%v err=%v, want Capped/nil", status2, err2)
	}
	if len(sb2.models) != 1 || sb2.models[0] != "sonnet" {
		t.Fatalf("resume with unchanged hash should use the iterator model first, got %v", sb2.models)
	}
	if !fileExistsT(filepath.Join(clone, "step1.go")) {
		t.Fatalf("resume did not reuse the existing clone (step1.go missing)")
	}

	// Now edit the spec: the next resume's first model should be the
	// planner's again.
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\nmodel: sonnet\n", repo)
	sb3 := newStubSandbox()
	sb3.verify = alwaysGreenVerify
	opts3 := opts
	opts3.Sandbox = sb3
	status3, err3 := Run(opts3)
	if err3 != nil || status3 != StatusCapped {
		t.Fatalf("resume (changed hash): status=%v err=%v, want Capped/nil", status3, err3)
	}
	if len(sb3.models) != 1 || sb3.models[0] != "opus" {
		t.Fatalf("resume after a spec edit should use the planner model first, got %v", sb3.models)
	}
}

// TestResumeReclaimCompletes: a run that capped without ever completing
// resumes and, this time, the agent commits and claims completion for
// real.
func TestResumeReclaimCompletes(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\n", repo)

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}

	sb2 := newStubSandbox()
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("resume: status=%v err=%v, want Complete/nil", status2, err2)
	}
}

// TestResumeClearsStaleComplete: a stale .spec-complete left by a prior
// session (that never actually re-claimed completion this run) is cleared
// on resume, logged, and the run does not falsely report complete.
func TestResumeClearsStaleComplete(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\n", repo)
	stateRoot := home.RunsDir(homeDir)
	clone := filepath.Join(stateRoot, "demo", "repo")

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}

	// Plant a stale .spec-complete directly (simulating a crash between
	// an agent touching it and the loop ever checking it).
	if err := os.WriteFile(filepath.Join(clone, ".spec-complete"), nil, 0o644); err != nil {
		t.Fatalf("plant stale .spec-complete: %v", err)
	}

	sb2 := newStubSandbox()
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusCapped {
		t.Fatalf("resume: status=%v err=%v, want Capped/nil (not falsely Complete)", status2, err2)
	}
	if fileExistsT(filepath.Join(clone, ".spec-complete")) {
		t.Fatalf(".spec-complete should have been cleared on resume")
	}

	logData, err := os.ReadFile(filepath.Join(stateRoot, "demo", "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "resume: cleared stale .spec-complete") {
		t.Fatalf("runner.log missing the resume-cleared-stale-complete message: %s", logData)
	}
}

// TestResumeStaleCompleteSymlinkNotFollowed: the same stale-complete
// clearing, but when .spec-complete is a symlink to a host file — it must
// be removed (the link itself), never followed, and the target must
// survive untouched.
func TestResumeStaleCompleteSymlinkNotFollowed(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\n", repo)
	stateRoot := home.RunsDir(homeDir)
	clone := filepath.Join(stateRoot, "demo", "repo")

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	if status, err := Run(opts); err != nil || status != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status, err)
	}

	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("do not leak"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	completePath := filepath.Join(clone, ".spec-complete")
	if err := os.Symlink(secret, completePath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	sb2 := newStubSandbox()
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	if status, err := Run(opts2); err != nil || status != StatusCapped {
		t.Fatalf("resume: status=%v err=%v, want Capped/nil", status, err)
	}

	if _, err := os.Lstat(completePath); !os.IsNotExist(err) {
		t.Fatalf("the symlink itself should have been removed")
	}
	secretData, err := os.ReadFile(secret)
	if err != nil {
		t.Fatalf("read secret: %v", err)
	}
	if string(secretData) != "do not leak" {
		t.Fatalf("secret target was modified: %q", secretData)
	}

	logData, err := os.ReadFile(filepath.Join(stateRoot, "demo", "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "SECURITY") {
		t.Fatalf("runner.log missing a SECURITY line: %s", logData)
	}
}

// TestResumeDepAlreadyInBase covers scenario (a): a dependency already
// merged into the base before the run ever started must not block a
// resume. Before this fix, integrateDeps left such a tip out of
// st.DepTips entirely, so checkDepTipsResume saw a resolved tip with no
// recorded entry and refused as if it were a newly added dependency.
func TestResumeDepAlreadyInBase(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-b", "spec/dep")
	commitFile(t, repo, "dep.go", "package dep\n", "dep work")
	depTip := mustGit(t, repo, "rev-parse", "HEAD")
	mustGit(t, repo, "checkout", "main")
	mustGit(t, repo, "merge", "--no-ff", "-m", "merge dep", "spec/dep")

	homeDir := t.TempDir()
	if err := os.MkdirAll(home.DoneDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir done: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.DoneDir(homeDir), "dep.md"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write done dep: %v", err)
	}
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\ndepends_on: dep\n", repo)
	stateRoot := home.RunsDir(homeDir)

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, StateRoot: stateRoot, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}
	if got := loadPriorState(opts, "demo").DepTips["dep"]; got != depTip {
		t.Fatalf("after run1, DepTips[dep] = %q, want %q (recorded even though already in base)", got, depTip)
	}

	sb2 := newStubSandbox()
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("resume: status=%v err=%v, want Complete/nil", status2, err2)
	}
}

// TestResumeDepAlreadyInBaseOldState is TestResumeDepAlreadyInBase with
// run.json's dep_tips wiped to nil before the resume, simulating a run
// started before this fix. checkDepTipsResume's ancestor check must
// tolerate this old-format state too, not just the newly-recorded one.
func TestResumeDepAlreadyInBaseOldState(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-b", "spec/dep")
	commitFile(t, repo, "dep.go", "package dep\n", "dep work")
	mustGit(t, repo, "checkout", "main")
	mustGit(t, repo, "merge", "--no-ff", "-m", "merge dep", "spec/dep")

	homeDir := t.TempDir()
	if err := os.MkdirAll(home.DoneDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir done: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home.DoneDir(homeDir), "dep.md"), []byte("done"), 0o644); err != nil {
		t.Fatalf("write done dep: %v", err)
	}
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\ndepends_on: dep\n", repo)
	stateRoot := home.RunsDir(homeDir)

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, StateRoot: stateRoot, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}

	statePath := filepath.Join(stateRoot, "demo", "run.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	var st runState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("parse run.json: %v", err)
	}
	st.DepTips = nil
	out, err := json.MarshalIndent(&st, "", "  ")
	if err != nil {
		t.Fatalf("marshal run.json: %v", err)
	}
	if err := os.WriteFile(statePath, append(out, '\n'), 0o644); err != nil {
		t.Fatalf("write run.json: %v", err)
	}

	sb2 := newStubSandbox()
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("resume: status=%v err=%v, want Complete/nil", status2, err2)
	}
}
