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
	if err := PrintStatusTable(&buf, homeDir, stateRoot, StatusTableOptions{}); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "BRANCH") || strings.Contains(out, "ready, not started") || strings.Contains(out, "drafts:") {
		t.Fatalf("output has retired BRANCH column or trailer lines:\n%s", out)
	}
	for _, want := range []string{
		"SLUG", "STATUS", "ITER", "COMMITS", "UPDATED", "REPO", "REVIEW",
		"blocked1", "blocked", "2026-01-02 00:01",
		"done1", "complete", "2026-01-01 00:05",
		"running1", fmt.Sprintf("running (pid %d)", os.Getpid()),
		"readyonly", "draftonly",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestStatusShowsWaiting(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	wakeAt := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	writeRunState(t, stateRoot, "waiting1", &runState{
		Slug: "waiting1", Branch: "spec/waiting1", Iterations: 1, Commits: 0,
		Started: "2026-01-03T00:00:00Z", WaitingUntil: wakeAt.Format(time.RFC3339),
	})
	l, err := lock.TryAcquire(specLockPath(stateRoot, "waiting1"), "waiting1")
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	defer l.Release()

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot, StatusTableOptions{}); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	out := buf.String()

	want := fmt.Sprintf("waiting (until %s)", wakeAt.Local().Format("15:04"))
	if !strings.Contains(out, want) {
		t.Fatalf("output missing %q:\n%s", want, out)
	}
	if strings.Contains(out, fmt.Sprintf("running (pid %d)", os.Getpid())) {
		t.Fatalf("output still shows running, want waiting:\n%s", out)
	}

	var detailBuf bytes.Buffer
	if err := PrintStatusDetail(&detailBuf, homeDir, stateRoot, "waiting1"); err != nil {
		t.Fatalf("PrintStatusDetail: %v", err)
	}
	wantDetail := "waiting_until: " + wakeAt.Format(time.RFC3339)
	if !strings.Contains(detailBuf.String(), wantDetail) {
		t.Fatalf("detail output missing %q:\n%s", wantDetail, detailBuf.String())
	}
}

// TestStatusShowsWaitsOn checks a ready spec waiting on an unmet dependency
// shows "ready (waits on <d>)", while specs with no dependency (or an
// already-satisfied one) show a bare "ready".
func TestStatusShowsWaitsOn(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	repo := newSourceRepo(t)

	writeReadySpec(t, homeDir, "a", "", repo)
	writeReadySpec(t, homeDir, "b", "depends_on: a\n", repo)
	writeReadySpec(t, homeDir, "c", "", repo)
	writeReadySpec(t, homeDir, "d", "depends_on: done1\n", repo)

	writeRunState(t, stateRoot, "done1", &runState{
		Slug: "done1", Branch: "spec/done1", Status: "complete",
		Started: "2026-01-01T00:00:00Z",
	})

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot, StatusTableOptions{}); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	cells := map[string]string{}
	for _, line := range strings.Split(buf.String(), "\n") {
		if f := strings.Fields(line); len(f) > 1 {
			cells[f[0]] = strings.Join(f[1:], " ")
		}
	}
	for slug, want := range map[string]string{"a": "ready", "b": "ready (waits on a)", "c": "ready", "d": "ready"} {
		if !strings.HasPrefix(cells[slug], want) || (want == "ready" && strings.HasPrefix(cells[slug], "ready (")) {
			t.Fatalf("row %s = %q, want status %q:\n%s", slug, cells[slug], want, buf.String())
		}
	}
}

func TestStatusShowsUsageLimitMarker(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	writeRunState(t, stateRoot, "done1", &runState{
		Slug: "done1", Branch: "spec/done1", Iterations: 3, Commits: 2, Status: "complete",
		Started: "2026-01-01T00:00:00Z", History: []string{"2026-01-01 00:05 complete"},
	})

	now := time.Now()
	resetAt := now.Add(30 * time.Minute)
	if err := writeLimitMarker(stateRoot, limitHit{ResetAt: resetAt}, now, "demo", time.Hour); err != nil {
		t.Fatalf("writeLimitMarker: %v", err)
	}

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot, StatusTableOptions{}); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	out := buf.String()

	want := "usage limit: until " + resetAt.Format("Mon 15:04")
	if !strings.Contains(out, want) {
		t.Fatalf("output missing %q:\n%s", want, out)
	}
}

func TestStatusTableEmpty(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot, StatusTableOptions{}); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	want := "no runs yet — nightshift new <slug> to start a spec\n"
	if buf.String() != want {
		t.Fatalf("output = %q, want %q", buf.String(), want)
	}
}

// TestStatusTableReadyOrDraftNoRuns: with ready and draft specs but no
// runs at all, both are rows under the header, with "-" run cells.
func TestStatusTableReadyOrDraftNoRuns(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)

	repo := newSourceRepo(t)
	writeReadySpec(t, homeDir, "readyonly", "", repo)
	writeDraftSpec(t, homeDir, "draftonly", "", repo)

	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot, StatusTableOptions{}); err != nil {
		t.Fatalf("PrintStatusTable: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "SLUG") ||
		!strings.HasPrefix(lines[1], "readyonly") || !strings.HasPrefix(lines[2], "draftonly") {
		t.Fatalf("output = %q, want header, ready row, draft row", buf.String())
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

// TestStatusDetailShowsDeps checks `status <slug>` prints depends_on and
// owns (read from the spec's frontmatter, since run.json never stores
// them) plus dep_tips and base_resolved (from run.json) when set, and
// omits all four when unset.
func TestStatusDetailShowsDeps(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	repo := newSourceRepo(t)

	writeReadySpec(t, homeDir, "demo", "depends_on: a, b\nowns: internal/api/, go.mod\n", repo)
	writeRunState(t, stateRoot, "demo", &runState{
		Slug: "demo", Repo: repo, Branch: "spec/demo",
		Base: strings.Repeat("a", 40), BaseRef: "main", LastGreen: strings.Repeat("b", 40),
		DepTips:      map[string]string{"a": strings.Repeat("c", 40), "b": strings.Repeat("d", 40)},
		BaseResolved: strings.Repeat("e", 40),
		Iterations:   1, Commits: 1, Status: "complete",
		Started: "2026-01-01T00:00:00Z",
	})

	var buf bytes.Buffer
	if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
		t.Fatalf("PrintStatusDetail: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"depends_on: a, b\n",
		"owns: internal/api/, go.mod\n",
		"dep_tips: a=" + strings.Repeat("c", 40) + ", b=" + strings.Repeat("d", 40) + "\n",
		"base_resolved: " + strings.Repeat("e", 40) + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	writeReadySpec(t, homeDir, "plain", "", repo)
	writeRunState(t, stateRoot, "plain", &runState{
		Slug: "plain", Repo: repo, Branch: "spec/plain",
		Base: strings.Repeat("a", 40), BaseRef: "main", LastGreen: strings.Repeat("b", 40),
		Iterations: 1, Commits: 1, Status: "complete",
		Started: "2026-01-01T00:00:00Z",
	})

	var plainBuf bytes.Buffer
	if err := PrintStatusDetail(&plainBuf, homeDir, stateRoot, "plain"); err != nil {
		t.Fatalf("PrintStatusDetail: %v", err)
	}
	plainOut := plainBuf.String()
	for _, notWant := range []string{"depends_on:", "owns:", "dep_tips:", "base_resolved:"} {
		if strings.Contains(plainOut, notWant) {
			t.Fatalf("output should omit %q:\n%s", notWant, plainOut)
		}
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

	newTime := time.Now().Add(time.Hour)
	if err := os.Chtimes(newPath, newTime, newTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	repeatPath := filepath.Join(resultsDir, "demo-2026-01-02-review-2.md")
	if err := os.WriteFile(repeatPath, []byte("---\nverdict: reject\n---\n\nrepeat\n"), 0o644); err != nil {
		t.Fatalf("write repeat review note: %v", err)
	}
	repeatTime := newTime.Add(time.Hour)
	if err := os.Chtimes(repeatPath, repeatTime, repeatTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	t.Run("same-day repeat review note wins", func(t *testing.T) {
		var buf bytes.Buffer
		if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
			t.Fatalf("PrintStatusDetail: %v", err)
		}
		want := "latest review: reject — " + repeatPath
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
	})
}

// TestLatestResultIgnoresReviewNotes is a regression test: a review note
// newer than the result note must not make "latest result:" report the
// review note instead.
func TestLatestResultIgnoresReviewNotes(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	writeRunState(t, stateRoot, "demo", &runState{
		Slug: "demo", Repo: "/path/to/repo", Branch: "spec/demo",
		Base: strings.Repeat("a", 40), BaseRef: "main", LastGreen: strings.Repeat("b", 40),
		Iterations: 1, Commits: 1, Status: "complete",
		Started: "2026-01-01T00:00:00Z",
	})

	resultsDir := home.ResultsDir(homeDir)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}
	resultPath := filepath.Join(resultsDir, "demo-2026-01-01.md")
	if err := os.WriteFile(resultPath, []byte("result"), 0o644); err != nil {
		t.Fatalf("write result note: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(resultPath, oldTime, oldTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	reviewPath := filepath.Join(resultsDir, "demo-2026-01-01-review.md")
	if err := os.WriteFile(reviewPath, []byte("---\nverdict: approve\n---\n\nreview\n"), 0o644); err != nil {
		t.Fatalf("write review note: %v", err)
	}

	var buf bytes.Buffer
	if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
		t.Fatalf("PrintStatusDetail: %v", err)
	}
	out := buf.String()
	want := "latest result: " + resultPath
	if !strings.Contains(out, want) {
		t.Fatalf("output missing %q:\n%s", want, out)
	}
	if strings.Contains(out, "latest result: "+reviewPath) {
		t.Fatalf("latest result matched the review note:\n%s", out)
	}
}

// TestLatestNoteIgnoresLongerSlugs is a regression test: notes for a
// longer slug that shares this slug as a prefix (e.g. "demo-extra") must
// never match "demo"'s latest result or latest review.
func TestLatestNoteIgnoresLongerSlugs(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	writeRunState(t, stateRoot, "demo", &runState{
		Slug: "demo", Repo: "/path/to/repo", Branch: "spec/demo",
		Base: strings.Repeat("a", 40), BaseRef: "main", LastGreen: strings.Repeat("b", 40),
		Iterations: 1, Commits: 1, Status: "complete",
		Started: "2026-01-01T00:00:00Z",
	})

	resultsDir := home.ResultsDir(homeDir)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}

	demoResult := filepath.Join(resultsDir, "demo-2026-01-01.md")
	if err := os.WriteFile(demoResult, []byte("demo result"), 0o644); err != nil {
		t.Fatalf("write demo result note: %v", err)
	}
	demoReview := filepath.Join(resultsDir, "demo-2026-01-01-review.md")
	if err := os.WriteFile(demoReview, []byte("---\nverdict: approve\n---\n\ndemo review\n"), 0o644); err != nil {
		t.Fatalf("write demo review note: %v", err)
	}
	oldTime := time.Now().Add(-time.Hour)
	for _, p := range []string{demoResult, demoReview} {
		if err := os.Chtimes(p, oldTime, oldTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	extraResult := filepath.Join(resultsDir, "demo-extra-2026-01-02.md")
	if err := os.WriteFile(extraResult, []byte("extra result"), 0o644); err != nil {
		t.Fatalf("write demo-extra result note: %v", err)
	}
	extraReview := filepath.Join(resultsDir, "demo-extra-2026-01-02-review.md")
	if err := os.WriteFile(extraReview, []byte("---\nverdict: reject\n---\n\nextra review\n"), 0o644); err != nil {
		t.Fatalf("write demo-extra review note: %v", err)
	}
	newTime := time.Now().Add(time.Hour)
	for _, p := range []string{extraResult, extraReview} {
		if err := os.Chtimes(p, newTime, newTime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	var buf bytes.Buffer
	if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
		t.Fatalf("PrintStatusDetail: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"latest result: " + demoResult,
		"latest review: approve — " + demoReview,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "demo-extra") {
		t.Fatalf("output matched demo-extra's notes:\n%s", out)
	}
}
