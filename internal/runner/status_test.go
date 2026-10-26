package runner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
		"logs: nightshift logs demo [-f] [--agent]",
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

// statusFixture builds the spec's fixture home. Clock: 2026-10-24 12:00
// America/Detroit.
func statusFixture(t *testing.T) (homeDir, stateRoot string, now time.Time, release func()) {
	t.Helper()
	loc, err := time.LoadLocation("America/Detroit")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TZ", "America/Detroit")
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
	now = time.Date(2026, 10, 24, 12, 0, 0, 0, loc)

	homeDir = t.TempDir()
	stateRoot = home.RunsDir(homeDir)
	mt := func(path string, t0 time.Time) {
		if err := os.Chtimes(path, t0, t0); err != nil {
			t.Fatal(err)
		}
	}
	writeDone := func(slug, fm string) {
		if err := os.MkdirAll(home.DoneDir(homeDir), 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(home.DoneDir(homeDir), slug+".md")
		if err := os.WriteFile(p, []byte("---\nrepo: /src/alpha\nverify: true\n"+fm+"---\n\n# s\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mt(p, now.Add(-30*24*time.Hour))
	}
	run := func(slug, status, repo, hist string) {
		writeRunState(t, stateRoot, slug, &runState{
			Slug: slug, Repo: repo, Status: status, Iterations: 2, Commits: 1,
			Started: "2026-10-01T00:00:00Z", History: []string{hist + " " + status},
		})
	}
	p := writeDraftSpec(t, homeDir, "drafty", "", "/src/beta")
	mt(p, now.Add(-2*time.Hour))
	p = writeReadySpec(t, homeDir, "fresh", "", "/src/alpha")
	mt(p, now.Add(-5*time.Hour))
	p = writeReadySpec(t, homeDir, "waity", "depends_on: fresh\n", "/src/beta")
	mt(p, now.Add(-6*time.Hour))
	writeReadySpec(t, homeDir, "busy", "", "/src/alpha")
	run("busy", "", "/src/alpha", "2026-10-24 11:00")
	writeReadySpec(t, homeDir, "capd", "", "/src/beta")
	run("capd", "capped", "/src/beta", "2026-10-24 09:30")
	writeReadySpec(t, homeDir, "fin", "", "/src/alpha")
	run("fin", "complete", "/src/alpha", "2026-10-23 08:15")
	writeDone("old1", "done: 2026-10-23T12:00:00-04:00\n")
	run("old1", "complete", "/src/alpha", "2026-10-20 08:00")
	writeDone("old10", "done: 2026-10-14T12:00:00-04:00\n")
	run("old10", "complete", "/src/beta", "2026-10-10 08:00")
	writeDone("nodone", "")
	mt(filepath.Join(home.DoneDir(homeDir), "nodone.md"), now.Add(-8*24*time.Hour))
	l, err := lock.TryAcquire(specLockPath(stateRoot, "busy"), "busy")
	if err != nil {
		t.Fatal(err)
	}
	return homeDir, stateRoot, now, func() { _ = l.Release() }
}

func statusOut(t *testing.T, homeDir, stateRoot string, o StatusTableOptions) string {
	t.Helper()
	var buf bytes.Buffer
	if err := PrintStatusTable(&buf, homeDir, stateRoot, o); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func TestStatusTableUnified(t *testing.T) {
	homeDir, stateRoot, now, release := statusFixture(t)
	defer release()
	if err := os.MkdirAll(home.ResultsDir(homeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	note := filepath.Join(home.ResultsDir(homeDir), "fin-2026-10-23-review.md")
	if err := os.WriteFile(note, []byte("---\nverdict: approve\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Same group as capd but a smaller slug and a later Updated: Updated desc wins.
	writeReadySpec(t, homeDir, "abc", "", "/src/beta")
	writeRunState(t, stateRoot, "abc", &runState{
		Slug: "abc", Repo: "/src/beta", Status: "red", Iterations: 2, Commits: 1,
		Started: "2026-10-01T00:00:00Z", History: []string{"2026-10-24 10:30 red"},
	})
	// Two ready rows with equal Updated: slug breaks the tie.
	for _, s := range []string{"rdyb", "rdya"} {
		p := writeReadySpec(t, homeDir, s, "", "/src/alpha")
		if err := os.Chtimes(p, now.Add(-4*time.Hour), now.Add(-4*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	got := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now})
	pid := os.Getpid()
	// Golden is whitespace-aligned by tabwriter; compare field-wise per line.
	norm := func(s string) []string {
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			out = append(out, strings.Join(strings.Fields(l), " "))
		}
		return out
	}
	wantLines := []string{
		"SLUG STATUS ITER COMMITS UPDATED REPO REVIEW",
		fmt.Sprintf("busy running (pid %d) 2 1 2026-10-24 11:00 alpha -", pid),
		"abc red 2 1 2026-10-24 10:30 beta -",
		"capd capped 2 1 2026-10-24 09:30 beta -",
		"fin complete 2 1 2026-10-23 08:15 alpha approve",
		"rdya ready - - 2026-10-24 08:00 alpha -",
		"rdyb ready - - 2026-10-24 08:00 alpha -",
		"fresh ready - - 2026-10-24 07:00 alpha -",
		"waity ready (waits on fresh) - - 2026-10-24 06:00 beta -",
		"drafty draft - - 2026-10-24 10:00 beta -",
		"old1 done 2 1 2026-10-23 12:00 alpha -",
		"nodone done - - 2026-10-16 12:00 alpha -",
		"old10 done 2 1 2026-10-14 12:00 beta -",
		"1 complete — review and merge, then nightshift done <slug> (or nightshift done --complete)",
	}
	g := norm(got)
	if strings.Join(g, "\n") != strings.Join(wantLines, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", got, strings.Join(wantLines, "\n"))
	}
}

func TestStatusDoneVsComplete(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	writeReadySpec(t, homeDir, "demo", "", "/src/alpha")
	writeRunState(t, stateRoot, "demo", &runState{Slug: "demo", Status: "complete", History: []string{"2026-10-24 08:00 complete"}})
	if out := statusOut(t, homeDir, stateRoot, StatusTableOptions{All: true}); !strings.Contains(out, "demo  complete") {
		t.Fatalf("want complete:\n%s", out)
	}
	if err := home.Done(homeDir, "demo", time.Now(), io.Discard); err != nil {
		t.Fatal(err)
	}
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{All: true})
	if !strings.Contains(out, "demo  done") || strings.Contains(out, "complete") {
		t.Fatalf("want done:\n%s", out)
	}
}

func TestStatusDetailShowsDone(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	writeReadySpec(t, homeDir, "demo", "", "/src/alpha")
	writeRunState(t, stateRoot, "demo", &runState{Slug: "demo", Status: "complete"})
	if err := home.Done(homeDir, "demo", time.Now(), io.Discard); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := PrintStatusDetail(&buf, homeDir, stateRoot, "demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "status: done\n") {
		t.Fatalf("detail:\n%s", buf.String())
	}
}

func TestStatusUpdatedSources(t *testing.T) {
	homeDir, stateRoot, now, release := statusFixture(t)
	defer release()
	// run with no history: falls back to started (UTC → Detroit local).
	writeReadySpec(t, homeDir, "nohist", "", "/src/alpha")
	writeRunState(t, stateRoot, "nohist", &runState{Slug: "nohist", Status: "red", Started: "2026-10-24T14:00:00Z"})
	// done spec with no done: key: Updated falls back to run.json history,
	// not the file's mtime.
	dp := filepath.Join(home.DoneDir(homeDir), "donehist.md")
	if err := os.WriteFile(dp, []byte("---\nrepo: /src/alpha\nverify: true\n---\n\n# s\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dp, now.Add(-10*24*time.Hour), now.Add(-10*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	writeRunState(t, stateRoot, "donehist", &runState{Slug: "donehist", Status: "complete", History: []string{"2026-10-22 09:00 complete"}})
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, All: true})
	for slug, want := range map[string]string{
		"donehist": "2026-10-22 09:00",
		"busy":     "2026-10-24 11:00", // history, local
		"fresh":    "2026-10-24 07:00", // mtime (12:00-5h)
		"drafty":   "2026-10-24 10:00", // mtime
		"old1":     "2026-10-23 12:00", // done: frontmatter
		"nodone":   "2026-10-16 12:00", // no done:, no run: mtime
		"nohist":   "2026-10-24 10:00", // started 14:00Z == 10:00 EDT
	} {
		if !statusLineHas(out, slug, want) {
			t.Errorf("%s: want %q in:\n%s", slug, want, out)
		}
	}
}

func statusLineHas(out, slug, sub string) bool {
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, slug+" ") && strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func TestStatusSinceAndAll(t *testing.T) {
	homeDir, stateRoot, now, release := statusFixture(t)
	defer release()
	d, text := 14*24*time.Hour, "14d"
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Since: d, SinceText: text})
	if !statusLineHas(out, "old10", "done") || strings.Contains(out, "more than") {
		t.Fatalf("14d:\n%s", out)
	}
	out = statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Since: 5 * 24 * time.Hour, SinceText: "5d"})
	if !strings.Contains(out, "+2 done more than 5d ago") {
		t.Fatalf("5d:\n%s", out)
	}
	out = statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Since: 36 * time.Hour, SinceText: "36h"})
	if !statusLineHas(out, "old1", "done") || !strings.Contains(out, "+2 done more than 36h ago") {
		t.Fatalf("36h:\n%s", out)
	}
	out = statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, All: true})
	for _, s := range []string{"old1", "old10", "nodone"} {
		if !statusLineHas(out, s, "done") {
			t.Fatalf("--all missing %s:\n%s", s, out)
		}
	}
	if strings.Contains(out, "more than") {
		t.Fatalf("--all footer:\n%s", out)
	}
	for in, want := range map[string]time.Duration{"3d": 72 * time.Hour, "36h": 36 * time.Hour, "90m": 90 * time.Minute} {
		if got, err := ParseStatusSince(in); err != nil || got != want {
			t.Errorf("ParseStatusSince(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "x", "0d", "-1d", "3dd", "d"} {
		if _, err := ParseStatusSince(bad); err == nil || !strings.Contains(err.Error(), "want a duration like 3d or 36h") {
			t.Errorf("ParseStatusSince(%q) err = %v", bad, err)
		}
	}
}

func TestStatusRepoFilter(t *testing.T) {
	homeDir, stateRoot, now, release := statusFixture(t)
	defer release()
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Repo: "beta"})
	if strings.Contains(out, "alpha") || !statusLineHas(out, "capd", "beta") {
		t.Fatalf("repo filter:\n%s", out)
	}
	// The only beta done row (old10) fits under the default count.
	if !statusLineHas(out, "old10", "done") || strings.Contains(out, "older done") || strings.Contains(out, "more than") {
		t.Fatalf("done rows under --repo:\n%s", out)
	}
	if strings.Contains(out, "complete —") {
		t.Fatalf("hint should be absent (no complete row kept):\n%s", out)
	}
}

func TestStatusOnlyHidden(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	now := time.Now()
	if err := os.MkdirAll(home.DoneDir(homeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(home.DoneDir(homeDir), "old.md")
	if err := os.WriteFile(p, []byte("---\ndone: 2020-01-01T00:00:00Z\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, DoneLimit: DoneNone})
	if out != "+1 older done (--all to show)\n" {
		t.Fatalf("got %q", out)
	}
}

func TestStatusCompleteHint(t *testing.T) {
	homeDir, stateRoot, now, release := statusFixture(t)
	defer release()
	writeReadySpec(t, homeDir, "fin2", "", "/src/alpha")
	writeRunState(t, stateRoot, "fin2", &runState{Slug: "fin2", Status: "complete", Repo: "/src/alpha"})
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now})
	if !strings.Contains(out, "2 complete — review and merge, then nightshift done <slug> (or nightshift done --complete)\n") {
		t.Fatalf("hint:\n%s", out)
	}
	out = statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Repo: "beta"})
	if strings.Contains(out, "complete —") {
		t.Fatalf("hint should be absent:\n%s", out)
	}
}

func TestStatusReviewColumn(t *testing.T) {
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	dir := home.ResultsDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a", "b", "c", "d", "e"} {
		writeReadySpec(t, homeDir, s, "", "/src/alpha")
	}
	base := time.Now().Add(-time.Hour)
	note := func(name, body string, age time.Duration) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		ts := base.Add(age)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	note("a-2026-10-01-review.md", "---\nverdict: reject\n---\n", 0)
	note("a-2026-10-02-review.md", "---\nverdict: approve\n---\n", time.Minute) // newest wins
	note("b-2026-10-01-review.md", "---\nverdict: reject\n---\n", 0)
	note("b-2026-10-01-review-2.md", "---\nverdict: approve\n---\n", time.Minute) // same-day -2
	note("c-2026-10-01-review.md", "no frontmatter\n", 0)                         // unknown
	note("d-extra-2026-10-01-review.md", "---\nverdict: approve\n---\n", 0)       // longer slug: d gets -
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{All: true})
	for slug, want := range map[string]string{"a": "approve", "b": "approve", "c": "unknown", "d": "-", "e": "-"} {
		if !statusLineHas(out, slug, "alpha  "+want) {
			t.Errorf("%s: want %s in:\n%s", slug, want, out)
		}
	}
}

// doneCountFixture writes done specs d1..d5 (d1 newest, 4..8 days before
// now; repos[i] names each one's repo, default alpha) plus a ready spec each
// for a complete and a capped run, all older than 3 days.
func doneCountFixture(t *testing.T, repos ...string) (homeDir, stateRoot string, now time.Time) {
	t.Helper()
	now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	homeDir = t.TempDir()
	stateRoot = home.RunsDir(homeDir)
	if err := os.MkdirAll(home.DoneDir(homeDir), 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 5; i++ {
		repo := "/src/alpha"
		if i-1 < len(repos) {
			repo = "/src/" + repos[i-1]
		}
		stamp := now.Add(-time.Duration(3+i) * 24 * time.Hour).Format(time.RFC3339)
		body := "---\nrepo: " + repo + "\nverify: true\ndone: " + stamp + "\n---\n"
		if err := os.WriteFile(filepath.Join(home.DoneDir(homeDir), fmt.Sprintf("d%d.md", i)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct{ slug, status string }{{"fin", "complete"}, {"capd", "capped"}} {
		writeReadySpec(t, homeDir, c.slug, "", "/src/alpha")
		writeRunState(t, stateRoot, c.slug, &runState{
			Slug: c.slug, Repo: "/src/alpha", Status: c.status, Iterations: 1,
			Started: "2026-10-01T00:00:00Z", History: []string{"2026-10-10 08:00 " + c.status},
		})
	}
	return homeDir, stateRoot, now
}

func doneSlugs(out string) []string {
	var got []string
	for _, l := range strings.Split(out, "\n") {
		if f := strings.Fields(l); len(f) > 2 && f[1] == "done" && !strings.HasPrefix(f[0], "+") {
			got = append(got, f[0])
		}
	}
	return got
}

func TestStatusDoneDefaultCount(t *testing.T) {
	homeDir, stateRoot, now := doneCountFixture(t)
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now})
	if got := strings.Join(doneSlugs(out), ","); got != "d1,d2,d3" {
		t.Fatalf("done rows %q:\n%s", got, out)
	}
	if !statusLineHas(out, "fin", "complete") || !statusLineHas(out, "capd", "capped") {
		t.Fatalf("complete/capped rows must stay:\n%s", out)
	}
	if !strings.Contains(out, "+2 older done (--all to show)\n") || strings.Contains(out, "more than") {
		t.Fatalf("footer:\n%s", out)
	}
}

func TestStatusDoneFlag(t *testing.T) {
	homeDir, stateRoot, now := doneCountFixture(t)
	for _, c := range []struct {
		limit  int
		want   string
		footer string
	}{
		{1, "d1", "+4 older done (--all to show)\n"},
		{DoneNone, "", "+5 older done (--all to show)\n"},
		{10, "d1,d2,d3,d4,d5", ""},
	} {
		out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, DoneLimit: c.limit})
		if got := strings.Join(doneSlugs(out), ","); got != c.want {
			t.Errorf("limit %d: done rows %q:\n%s", c.limit, got, out)
		}
		if c.footer == "" {
			if strings.Contains(out, "older done") {
				t.Errorf("limit %d: unexpected footer:\n%s", c.limit, out)
			}
		} else if !strings.Contains(out, c.footer) {
			t.Errorf("limit %d: footer:\n%s", c.limit, out)
		}
	}
}

func TestStatusDoneWithRepo(t *testing.T) {
	// d1,d3,d4,d5 alpha; d2 beta.
	homeDir, stateRoot, now := doneCountFixture(t, "alpha", "beta", "alpha", "alpha", "alpha")
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Repo: "alpha"})
	if got := strings.Join(doneSlugs(out), ","); got != "d1,d3,d4" {
		t.Fatalf("done rows %q:\n%s", got, out)
	}
	if !strings.Contains(out, "+1 older done (--all to show)\n") {
		t.Fatalf("footer counts only alpha rows:\n%s", out)
	}
	out = statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Repo: "beta"})
	if got := strings.Join(doneSlugs(out), ","); got != "d2" || strings.Contains(out, "older done") {
		t.Fatalf("beta:\n%s", out)
	}
}

func TestStatusSinceIgnoresCount(t *testing.T) {
	homeDir, stateRoot, now := doneCountFixture(t)
	old := filepath.Join(home.DoneDir(homeDir), "ancient.md")
	body := "---\nrepo: /src/alpha\nverify: true\ndone: " + now.Add(-30*24*time.Hour).Format(time.RFC3339) + "\n---\n"
	if err := os.WriteFile(old, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := statusOut(t, homeDir, stateRoot, StatusTableOptions{Now: now, Since: 14 * 24 * time.Hour, SinceText: "14d"})
	if got := strings.Join(doneSlugs(out), ","); got != "d1,d2,d3,d4,d5" {
		t.Fatalf("done rows %q:\n%s", got, out)
	}
	if !strings.Contains(out, "+1 done more than 14d ago (--all to show)\n") || strings.Contains(out, "older done") {
		t.Fatalf("footer:\n%s", out)
	}
}
