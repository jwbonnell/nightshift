package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestReviewWorkspaceAtRef(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "feature.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "feature")
	head := mustGit(t, repo, "rev-parse", "HEAD")

	ws := filepath.Join(t.TempDir(), "review-ws")
	got, err := buildReviewWorkspace(context.Background(), ws, repo, base, "main", "", "", "")
	if err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}
	if got != head {
		t.Fatalf("returned head = %s, want %s", got, head)
	}
	gotHead := mustGit(t, filepath.Join(ws, "repo"), "rev-parse", "HEAD")
	if gotHead != head {
		t.Fatalf("ws/repo HEAD = %s, want %s", gotHead, head)
	}
	if remotes := mustGit(t, filepath.Join(ws, "repo"), "remote"); remotes != "" {
		t.Fatalf("ws/repo has remotes: %q", remotes)
	}
}

// TestReviewWorkspaceAtPendingRef covers the sync-pending case: the
// reviewed ref isn't reachable from any branch, so `git clone --no-local`
// (which only copies refs/heads and tags) won't carry its commit, and
// buildReviewWorkspace must fall back to an explicit fetch for it.
func TestReviewWorkspaceAtPendingRef(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")

	mustGit(t, repo, "checkout", "--detach", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "pending.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write pending.go: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "pending")
	head := mustGit(t, repo, "rev-parse", "HEAD")
	mustGit(t, repo, "update-ref", "refs/nightshift/demo", head)
	mustGit(t, repo, "checkout", "main")

	ws := filepath.Join(t.TempDir(), "review-ws")
	got, err := buildReviewWorkspace(context.Background(), ws, repo, base, "refs/nightshift/demo", "", "", "")
	if err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}
	if got != head {
		t.Fatalf("returned head = %s, want %s", got, head)
	}
	gotHead := mustGit(t, filepath.Join(ws, "repo"), "rev-parse", "HEAD")
	if gotHead != head {
		t.Fatalf("ws/repo HEAD = %s, want %s", gotHead, head)
	}
}

func TestReviewWorkspaceInputs(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "feature.go"), []byte("package main\n\nfunc Feature() {}\n"), 0o644); err != nil {
		t.Fatalf("write feature.go: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "feature")
	head := mustGit(t, repo, "rev-parse", "HEAD")

	specDir := t.TempDir()
	specPath := filepath.Join(specDir, "demo.md")
	if err := os.WriteFile(specPath, []byte("---\nrepo: x\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	ws := filepath.Join(t.TempDir(), "review-ws")
	summary := reviewSummary("complete", "1", "1", base[:7], head[:7], "spec/demo")
	if _, err := buildReviewWorkspace(context.Background(), ws, repo, base, "main", specPath, "some progress notes", summary); err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}

	inputs := filepath.Join(ws, "inputs")
	entries, err := os.ReadDir(inputs)
	if err != nil {
		t.Fatalf("read inputs: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	want := []string{"diff.patch", "diffstat.txt", "progress.md", "spec.md", "summary.md"}
	got := append([]string{}, names...)
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("inputs/ files = %v, want %v", got, want)
	}

	specGot, err := os.ReadFile(filepath.Join(inputs, "spec.md"))
	if err != nil || string(specGot) != "---\nrepo: x\n---\n# demo\n" {
		t.Fatalf("spec.md = %q, %v", specGot, err)
	}
	progGot, err := os.ReadFile(filepath.Join(inputs, "progress.md"))
	if err != nil || string(progGot) != "some progress notes" {
		t.Fatalf("progress.md = %q, %v", progGot, err)
	}
	sumGot, err := os.ReadFile(filepath.Join(inputs, "summary.md"))
	if err != nil || string(sumGot) != summary {
		t.Fatalf("summary.md = %q, %v", sumGot, err)
	}

	wantDiffstat, err := gitDiffOutput(repo, "diff", "--stat", base+".."+head)
	if err != nil {
		t.Fatalf("source diffstat: %v", err)
	}
	gotDiffstat, err := os.ReadFile(filepath.Join(inputs, "diffstat.txt"))
	if err != nil || !bytes.Equal(gotDiffstat, wantDiffstat) {
		t.Fatalf("diffstat.txt = %q, want %q", gotDiffstat, wantDiffstat)
	}

	wantDiff, err := gitDiffOutput(repo, "diff", base+".."+head)
	if err != nil {
		t.Fatalf("source diff: %v", err)
	}
	gotDiff, err := os.ReadFile(filepath.Join(inputs, "diff.patch"))
	if err != nil || !bytes.Equal(gotDiff, wantDiff) {
		t.Fatalf("diff.patch = %q, want %q", gotDiff, wantDiff)
	}
}

func TestReviewWorkspaceMissingSpecAndProgress(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")

	ws := filepath.Join(t.TempDir(), "review-ws")
	if _, err := buildReviewWorkspace(context.Background(), ws, repo, base, "main", "", "", ""); err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}
	spec, err := os.ReadFile(filepath.Join(ws, "inputs", "spec.md"))
	if err != nil || string(spec) != "(spec file not found)" {
		t.Fatalf("spec.md = %q, %v", spec, err)
	}
	progress, err := os.ReadFile(filepath.Join(ws, "inputs", "progress.md"))
	if err != nil || string(progress) != "(no PROGRESS.md)" {
		t.Fatalf("progress.md = %q, %v", progress, err)
	}
}

func TestReviewDiffTruncated(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")

	big := strings.Repeat("x", 300<<10) + "\n"
	if err := os.WriteFile(filepath.Join(repo, "big.txt"), []byte(big), 0o644); err != nil {
		t.Fatalf("write big.txt: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "big file")
	head := mustGit(t, repo, "rev-parse", "HEAD")

	ws := filepath.Join(t.TempDir(), "review-ws")
	if _, err := buildReviewWorkspace(context.Background(), ws, repo, base, head, "", "", ""); err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}
	patch, err := os.ReadFile(filepath.Join(ws, "inputs", "diff.patch"))
	if err != nil {
		t.Fatalf("read diff.patch: %v", err)
	}
	if len(patch) <= reviewDiffMax {
		t.Fatalf("diff.patch not truncated: len=%d", len(patch))
	}
	const marker = "[diff truncated at 200 KiB — read the changed files under repo/ directly]\n"
	if !strings.HasSuffix(string(patch), marker) {
		t.Fatalf("diff.patch does not end with truncation marker: %q", patch[len(patch)-200:])
	}
}

// gitDiffOutput shells out directly (bypassing gitx.Run's TrimSpace) so
// the comparison matches exactly what buildReviewWorkspace itself writes.
func gitDiffOutput(repo string, args ...string) ([]byte, error) {
	full := append([]string{"-C", repo}, args...)
	return exec.Command("git", full...).Output()
}

// TestReviewDiffFlags confirms the diffs run with --no-ext-diff
// --no-textconv: a textconv driver configured on the source repo must
// not change what lands in inputs/diff.patch.
func TestReviewDiffFlags(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.txt diff=upper\n"), 0o644); err != nil {
		t.Fatalf("write .gitattributes: %v", err)
	}
	mustGit(t, repo, "config", "diff.upper.textconv", "tr a-z A-Z")
	if err := os.WriteFile(filepath.Join(repo, "lower.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatalf("write lower.txt: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "add lower.txt")
	head := mustGit(t, repo, "rev-parse", "HEAD")

	ws := filepath.Join(t.TempDir(), "review-ws")
	if _, err := buildReviewWorkspace(context.Background(), ws, repo, base, head, "", "", ""); err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}
	patch, err := os.ReadFile(filepath.Join(ws, "inputs", "diff.patch"))
	if err != nil {
		t.Fatalf("read diff.patch: %v", err)
	}
	if !strings.Contains(string(patch), "hello world") {
		t.Fatalf("diff.patch missing raw textconv-bypassed text: %q", patch)
	}
	if strings.Contains(string(patch), "HELLO WORLD") {
		t.Fatalf("diff.patch contains textconv-converted text, textconv was not bypassed: %q", patch)
	}
}

// TestReviewDiffHonorsContext confirms the diffstat/diff steps are
// ctx-aware: an already-cancelled context fails buildReviewWorkspace at
// the diffstat step (the clone above it isn't ctx-aware).
func TestReviewDiffHonorsContext(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ws := filepath.Join(t.TempDir(), "review-ws")
	_, err := buildReviewWorkspace(ctx, ws, repo, base, "main", "", "", "")
	if err == nil {
		t.Fatalf("buildReviewWorkspace: want error from a cancelled context")
	}
	if !strings.Contains(err.Error(), "diffstat") {
		t.Fatalf("err = %v, want mention of diffstat", err)
	}
}

// shotsPrompt commits files (path -> content) to a fresh source repo, runs
// one general-lens review over it and returns the prompt plus the baseline
// lens prompt.
func shotsPrompt(t *testing.T, files map[string]string, links map[string]string) (got, base string) {
	t.Helper()
	repo := newSourceRepo(t)
	baseRev := mustGit(t, repo, "rev-parse", "HEAD")
	for name, content := range files {
		full := filepath.Join(repo, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(repo, name)); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "shots")
	home := t.TempDir()
	rv := &seqReviewer{texts: []string{"VERDICT: approve\nSUMMARY: ok."}}
	p := reviewParams{home: home, stateRoot: t.TempDir(), slug: "demo", repo: repo, base: baseRev, ref: "main", model: "opus", lenses: []string{"general"}}
	if _, _, _, err := reviewLenses(context.Background(), rv, p); err != nil {
		t.Fatalf("reviewLenses: %v", err)
	}
	base, err := lensPrompt(home, "general")
	if err != nil {
		t.Fatal(err)
	}
	return rv.prompts[0], base
}

const shotsDecl = "screenshots: build/screens/\n"

func TestReviewScreenshotsListed(t *testing.T) {
	got, base := shotsPrompt(t, map[string]string{
		".nightshift.md":        shotsDecl,
		"build/screens/b.png":   "x",
		"build/screens/a.png":   "x",
		"build/screens/C.PNG":   "x",
		"build/screens/n.txt":   "x",
		"build/screens/z/d.png": "x",
	}, nil)
	want := base + "\n## Screenshots to look at\n\n- repo/build/screens/C.PNG\n- repo/build/screens/a.png\n- repo/build/screens/b.png\n- repo/build/screens/z/d.png\n" +
		"\nOpen each one. Report anything a tester would notice in the first ten seconds that the diff does not explain.\n"
	if !strings.HasSuffix(base, "\n") {
		want = base + "\n" + want[len(base):]
	}
	if got != want {
		t.Fatalf("prompt mismatch\ngot tail: %q\nwant tail: %q", got[len(base):], want[len(base):])
	}
}

func TestReviewScreenshotsCap(t *testing.T) {
	files := map[string]string{".nightshift.md": shotsDecl}
	for i := 0; i < 45; i++ {
		files[filepath.Join("build/screens", "s"+string(rune('a'+i/26))+string(rune('a'+i%26))+".png")] = "x"
	}
	got, _ := shotsPrompt(t, files, nil)
	if n := strings.Count(got, "- repo/build/screens/"); n != 40 {
		t.Fatalf("listed %d, want 40", n)
	}
	if !strings.Contains(got, "\n+5 more\n") {
		t.Fatalf("missing +5 more:\n%s", got)
	}
}

func TestReviewScreenshotsOmitted(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		links map[string]string
	}{
		"not declared": {files: map[string]string{"build/screens/a.png": "x"}},
		"dir missing":  {files: map[string]string{".nightshift.md": shotsDecl}},
		"no pngs":      {files: map[string]string{".nightshift.md": shotsDecl, "build/screens/a.txt": "x"}},
		"symlinked dir": {
			files: map[string]string{".nightshift.md": shotsDecl, "other/a.png": "x"},
			links: map[string]string{"build": "other"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, base := shotsPrompt(t, c.files, c.links)
			if got != base {
				t.Fatalf("prompt changed: %q", got[len(base):])
			}
		})
	}
}
