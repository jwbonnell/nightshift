package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func normalOpts(homeDir string, sb *stubSandbox) Options {
	o := planOnlyOpts(homeDir, sb)
	o.PlanOnly = false
	return o
}

func askQuestion(ctx context.Context, iter int, dir string) error {
	return os.WriteFile(filepath.Join(dir, "QUESTION.md"), []byte("Which design doc?\n"), 0o644)
}

func specSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestPlanningQuestionIsNotPlanned(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	specPath := writeReadySpec(t, homeDir, "demo", "", repo)
	sb := newStubSandbox()
	sb.agent = askQuestion
	opts := normalOpts(homeDir, sb)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("status=%v err=%v, want blocked", status, err)
	}
	st := loadPriorState(opts, "demo")
	if st == nil || st.Planned {
		t.Fatalf("state = %+v, want planned=false", st)
	}
	if want := specSum(t, specPath); st.SpecHash != want {
		t.Fatalf("spec_hash = %s, want %s", st.SpecHash, want)
	}
}

func TestStaleQuestionClearedAfterSpecEdit(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	sb := newStubSandbox()
	sb.agent = askQuestion
	opts := normalOpts(homeDir, sb)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}

	writeReadySpec(t, homeDir, "demo", "model: edited\n", repo)
	sb2 := newStubSandbox()
	done := completingAgent(t)
	var existedAtPlan = true
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			_, err := os.Lstat(filepath.Join(dir, "QUESTION.md"))
			existedAtPlan = err == nil
		}
		return done(ctx, iter, dir)
	}
	sb2.verify = alwaysGreenVerify
	opts2 := normalOpts(homeDir, sb2)
	status, err := Run(opts2)
	if err != nil || status == StatusBlocked {
		t.Fatalf("second: status=%v err=%v, want not blocked", status, err)
	}
	if existedAtPlan {
		t.Fatal("QUESTION.md still present when the planning prompt ran")
	}
	if len(sb2.prompts) == 0 || !strings.HasPrefix(sb2.prompts[0], planPreamble) {
		t.Fatalf("second run did not start with planning")
	}
	prog, _ := os.ReadFile(filepath.Join(filepath.Join(opts2.StateRoot, "demo", "repo"), "PROGRESS.md"))
	if !strings.Contains(string(prog), "the question was cleared") {
		t.Fatalf("PROGRESS.md lacks the cleared note:\n%s", prog)
	}
}

func TestQuestionKeptWhenSpecUnchanged(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	sb := newStubSandbox()
	sb.agent = askQuestion
	opts := normalOpts(homeDir, sb)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}

	sb2 := newStubSandbox()
	existedAtPlan := false
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			_, err := os.Lstat(filepath.Join(dir, "QUESTION.md"))
			existedAtPlan = err == nil
		}
		return nil
	}
	opts2 := normalOpts(homeDir, sb2)
	opts2.Stdout = io.Discard
	if status, err := Run(opts2); err != nil || status != StatusBlocked {
		t.Fatalf("second: status=%v err=%v, want blocked", status, err)
	}
	if !existedAtPlan {
		t.Fatal("QUESTION.md was removed although the spec did not change")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Join(opts.StateRoot, "demo", "repo"), "QUESTION.md")); err != nil {
		t.Fatalf("QUESTION.md gone: %v", err)
	}
}

func TestSpecHashRecordedOnReplan(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "max_iter: 2\n", repo)
	run := func() *stubSandbox {
		sb := newStubSandbox()
		sb.verify = alwaysGreenVerify
		status, err := Run(normalOpts(homeDir, sb))
		if err != nil || status == StatusBlocked || status == StatusError || status == StatusComplete {
			t.Fatalf("status=%v err=%v, want a resumable non-blocked status", status, err)
		}
		return sb
	}
	run()
	writeReadySpec(t, homeDir, "demo", "max_iter: 2\niter_timeout: 31m\n", repo)
	sb2 := run()
	if len(sb2.prompts) == 0 || !strings.HasPrefix(sb2.prompts[0], planPreamble) {
		t.Fatalf("edited spec did not re-plan")
	}
	sb3 := run()
	if len(sb3.prompts) == 0 || strings.HasPrefix(sb3.prompts[0], planPreamble) {
		t.Fatalf("unedited resume re-planned: %q", sb3.prompts)
	}
}

func TestNextSkipsBlockedAfterReplan(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	sb := newStubSandbox()
	sb.agent = askQuestion
	if status, err := Run(normalOpts(homeDir, sb)); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}
	writeReadySpec(t, homeDir, "demo", "model: edited\n", repo)
	sb2 := newStubSandbox()
	sb2.agent = askQuestion
	opts := normalOpts(homeDir, sb2)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("second: status=%v err=%v, want blocked", status, err)
	}
	cands, _, err := selectNextCandidates(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.slug == "demo" {
			t.Fatalf("--next would pick the blocked spec: %v", cands)
		}
	}
}

func TestQuestionWithAgentErrorRecordsHash(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	specPath := writeReadySpec(t, homeDir, "demo", "", repo)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if err := askQuestion(ctx, iter, dir); err != nil {
			return err
		}
		return errors.New("agent crashed")
	}
	opts := normalOpts(homeDir, sb)
	if status, err := Run(opts); err != nil || status != StatusBlocked {
		t.Fatalf("first: status=%v err=%v, want blocked", status, err)
	}
	prev := loadPriorState(opts, "demo")
	if prev == nil || prev.SpecHash != specSum(t, specPath) {
		t.Fatalf("spec_hash = %+v, want current hash", prev)
	}

	sb2 := newStubSandbox()
	existed := false
	sb2.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			_, err := os.Lstat(filepath.Join(dir, "QUESTION.md"))
			existed = err == nil
		}
		return nil
	}
	opts2 := normalOpts(homeDir, sb2)
	opts2.Stdout = io.Discard
	if _, err := Run(opts2); err != nil {
		t.Fatal(err)
	}
	if !existed {
		t.Fatal("QUESTION.md was cleared although the spec did not change")
	}
	if _, err := os.Lstat(filepath.Join(CloneDir(opts.StateRoot, "demo"), "QUESTION.md")); err != nil {
		t.Fatalf("QUESTION.md gone: %v", err)
	}
}
