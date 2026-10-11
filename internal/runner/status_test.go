package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

func writeRunState(t *testing.T, stateRoot, slug string, st *runState) {
	t.Helper()
	dir := filepath.Join(stateRoot, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), data, 0o644); err != nil {
		t.Fatalf("write run.json: %v", err)
	}
}

func TestStatusTable(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	writeRunState(t, stateRoot, "done1", &runState{
		Slug: "done1", Branch: "spec/done1", Iterations: 3, Commits: 2, Status: "complete",
		Started: "2026-01-01T00:00:00Z", History: []string{"2026-01-01 00:05 complete"},
	})
	writeRunState(t, stateRoot, "blocked1", &runState{
		Slug: "blocked1", Branch: "spec/blocked1", Iterations: 1, Commits: 0, Status: "blocked",
		Started: "2026-01-02T00:00:00Z", History: []string{"2026-01-02 00:01 blocked"},
	})
	writeRunState(t, stateRoot, "running1", &runState{
		Slug: "running1", Branch: "spec/running1", Iterations: 1, Commits: 0,
		Started: "2026-01-03T00:00:00Z",
	})
	l, err := lock.TryAcquire(specLockPath(stateRoot, "running1"), "running1")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	repo := newSourceRepo(t)
	writeReadySpec(t, homeDir, "readyonly", "", repo)
	writeDraftSpec(t, homeDir, "draftonly", "", repo)

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"SLUG", "STATUS", "ITER", "COMMITS", "UPDATED", "BRANCH",
		"blocked1", "blocked", "2026-01-02 00:01",
		"done1", "complete", "2026-01-01 00:05",
		"running1", fmt.Sprintf("running (pid %d)", os.Getpid()),
		"ready, not started: readyonly",
		"drafts: draftonly",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusTableEmpty(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	want := "no runs yet — nightshift new <slug> to start a spec\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

// TestStatusTableReadyOrDraftNoRuns is a regression test: with ready or
// draft specs but no runs at all, the table header (SLUG/STATUS/...) must
// not be printed above the trailer lines — there are no rows to put under
// it.
func TestStatusTableReadyOrDraftNoRuns(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	repo := newSourceRepo(t)
	writeReadySpec(t, homeDir, "readyonly", "", repo)
	writeDraftSpec(t, homeDir, "draftonly", "", repo)

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	want := "ready, not started: readyonly\ndrafts: draftonly\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q (no empty table header)", buf.String(), want)
	}
}

func TestStatusDetail(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	st := &runState{
		Slug: "demo", Repo: "/path/to/repo", Branch: "spec/demo",
		Base: strings.Repeat("a", 40), BaseRef: "main", LastGreen: strings.Repeat("b", 40),
		SpecHash: "hash123", Iterations: 2, Commits: 1, Status: "complete",
		Started: "2026-01-01T00:00:00Z", History: []string{"2026-01-01 00:05 complete"},
	}
	writeRunState(t, stateRoot, "demo", st)

	resultsDir := home.ResultsDir(homeDir)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}
	oldPath := filepath.Join(resultsDir, "demo-2026-01-01.md")
	if err := os.WriteFile(oldPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("write old note: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	newPath := filepath.Join(resultsDir, "demo-2026-01-02.md")
	if err := os.WriteFile(newPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("write new note: %v", err)
	}

	var buf bytes.Buffer
	if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
		t.Fatalf("PrintStatusDetail: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"slug: demo",
		"repo: /path/to/repo",
		"branch: spec/demo",
		"base_ref: main",
		"status: complete",
		"state dir: " + filepath.Join(stateRoot, "demo"),
		"clone: " + filepath.Join(stateRoot, "demo", "repo"),
		"runner log: " + filepath.Join(stateRoot, "demo", "runner.log"),
		"agent log: " + filepath.Join(stateRoot, "demo", "agent.log"),
		"latest result: " + newPath,
		"review: git -C /path/to/repo log --oneline",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	slugIdx := strings.Index(out, "slug:")
	repoIdx := strings.Index(out, "repo:")
	branchIdx := strings.Index(out, "branch:")
	if slugIdx < 0 || repoIdx < 0 || branchIdx < 0 || !(slugIdx < repoIdx && repoIdx < branchIdx) {
		t.Fatalf("fields not in struct order:\n%s", out)
	}

	err := PrintStatusDetail(&buf, homeDir, stateRoot, "nope")
	wantErr := "no run state for nope"
	if err == nil || err.Error() != wantErr {
		t.Fatalf("err = %v, want %q", err, wantErr)
	}
}

func TestStatusShowsLatestReview(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	writeRunState(t, stateRoot, "demo", &runState{
		Slug: "demo", Repo: "/path/to/repo", Branch: "spec/demo",
		Base: strings.Repeat("a", 40), BaseRef: "main", LastGreen: strings.Repeat("b", 40),
		Iterations: 1, Commits: 1, Status: "complete",
		Started: "2026-01-01T00:00:00Z",
	})

	t.Run("none when no review note exists", func(t *testing.T) {
		var buf bytes.Buffer
		if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
			t.Fatalf("PrintStatusDetail: %v", err)
		}
		if !strings.Contains(buf.String(), "latest review: none\n") {
			t.Fatalf("output missing latest review: none:\n%s", buf.String())
		}
	})

	resultsDir := home.ResultsDir(homeDir)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}
	oldPath := filepath.Join(resultsDir, "demo-2026-01-01-review.md")
	if err := os.WriteFile(oldPath, []byte("---\nverdict: concerns\n---\n\nold\n"), 0o644); err != nil {
		t.Fatalf("write old review note: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	newPath := filepath.Join(resultsDir, "demo-2026-01-02-review.md")
	if err := os.WriteFile(newPath, []byte("---\nverdict: approve\n---\n\nnew\n"), 0o644); err != nil {
		t.Fatalf("write new review note: %v", err)
	}

	t.Run("newest by mtime, with its verdict", func(t *testing.T) {
		var buf bytes.Buffer
		if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
			t.Fatalf("PrintStatusDetail: %v", err)
		}
		want := "latest review: approve — " + newPath
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
	})
}
