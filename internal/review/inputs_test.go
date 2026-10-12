package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
)

func TestExportIgnoreCannotHide(t *testing.T) {
	ctx := context.Background()
	remote, _, _ := makeRemote(t, "refs/merge-requests/%d/head", 1)
	g := gitlabEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/1/head:refs/cr/1", "main", 1); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}

	// Head only has a.txt; add a second commit on top with an
	// export-ignore'd file, fetch that as the real head to export.
	work := t.TempDir()
	testGit(t, work, "clone", "-q", repo, ".")
	if err := os.WriteFile(filepath.Join(work, ".gitattributes"), []byte("secret.txt export-ignore\n"), 0o644); err != nil {
		t.Fatalf("write .gitattributes: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "secret.txt"), []byte("shh\n"), 0o644); err != nil {
		t.Fatalf("write secret.txt: %v", err)
	}
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "-q", "-m", "add export-ignore'd secret")
	newHead := testGit(t, work, "rev-parse", "HEAD")
	testGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/tmp")

	treeDir := filepath.Join(t.TempDir(), "tree")
	if err := exportTree(ctx, g, repo, newHead, treeDir); err != nil {
		t.Fatalf("exportTree: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(treeDir, "secret.txt"))
	if err != nil {
		t.Fatalf("secret.txt missing from export despite export-ignore: %v", err)
	}
	if string(got) != "shh\n" {
		t.Fatalf("secret.txt content = %q, want %q", got, "shh\n")
	}
	if _, err := os.Stat(filepath.Join(treeDir, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git should not exist in the exported tree (stat err: %v)", err)
	}
}

func TestChangeSuppliedReviewDirReplaced(t *testing.T) {
	ctx := context.Background()
	remote, _, baseSHA := makeRemote(t, "refs/merge-requests/%d/head", 1)
	g := gitlabEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/1/head:refs/cr/1", "main", 1); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}
	head, err := resolveHead(ctx, g, repo, "", 1)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}

	// Commit a change-supplied .pr-review/changes.diff on top of head.
	work := t.TempDir()
	testGit(t, work, "clone", "-q", repo, ".")
	testGit(t, work, "checkout", "-q", head)
	if err := os.MkdirAll(filepath.Join(work, reviewDirName), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, reviewDirName, "changes.diff"), []byte("fake\n"), 0o644); err != nil {
		t.Fatalf("write fake changes.diff: %v", err)
	}
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "-q", "-m", "ship a fake review dir")
	newHead := testGit(t, work, "rev-parse", "HEAD")
	testGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/tmp")

	treeDir := filepath.Join(t.TempDir(), "tree")
	if err := exportTree(ctx, g, repo, newHead, treeDir); err != nil {
		t.Fatalf("exportTree: %v", err)
	}

	inputs, err := prepareReviewDir(ctx, g, repo, baseSHA, newHead, treeDir)
	if err != nil {
		t.Fatalf("prepareReviewDir: %v", err)
	}
	if !strings.HasPrefix(inputs.Diff, "diff --git") {
		t.Fatalf("Diff doesn't start with 'diff --git': %q", inputs.Diff)
	}
	if !strings.Contains(inputs.Diff, "diff --git a/"+reviewDirName+"/changes.diff") {
		t.Fatalf("Diff doesn't mention the change-supplied %s/changes.diff: %q", reviewDirName, inputs.Diff)
	}
	if inputs.Commits == "" {
		t.Fatalf("Commits is empty")
	}

	got, err := os.ReadFile(filepath.Join(treeDir, reviewDirName, "changes.diff"))
	if err != nil {
		t.Fatalf("read real changes.diff: %v", err)
	}
	if strings.TrimSpace(string(got)) == "fake" {
		t.Fatalf("changes.diff still has the change-supplied fake content")
	}
}

func TestDiffExcludesLockfiles(t *testing.T) {
	ctx := context.Background()
	remote, _, baseSHA := makeRemote(t, "refs/merge-requests/%d/head", 1)
	g := gitlabEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/1/head:refs/cr/1", "main", 1); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}
	head, err := resolveHead(ctx, g, repo, "", 1)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}

	work := t.TempDir()
	testGit(t, work, "clone", "-q", repo, ".")
	testGit(t, work, "checkout", "-q", head)
	if err := os.WriteFile(filepath.Join(work, "package-lock.json"), []byte(`{"generated":true}`+"\n"), 0o644); err != nil {
		t.Fatalf("write package-lock.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(work, "real.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatalf("write real.go: %v", err)
	}
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "-q", "-m", "add lockfile and real file")
	newHead := testGit(t, work, "rev-parse", "HEAD")
	testGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/tmp")

	diff, err := changesDiff(ctx, g, repo, baseSHA, newHead)
	if err != nil {
		t.Fatalf("changesDiff: %v", err)
	}
	if strings.Contains(diff, "package-lock.json") {
		t.Fatalf("diff should exclude package-lock.json: %s", diff)
	}
	if !strings.Contains(diff, "real.go") {
		t.Fatalf("diff should include real.go: %s", diff)
	}

	numstat, err := numstatText(ctx, g, repo, baseSHA, newHead)
	if err != nil {
		t.Fatalf("numstatText: %v", err)
	}
	if strings.Contains(numstat, "package-lock.json") {
		t.Fatalf("numstat should exclude package-lock.json: %s", numstat)
	}
	if !strings.Contains(numstat, "real.go") {
		t.Fatalf("numstat should include real.go: %s", numstat)
	}
}

func TestCountChangedLines(t *testing.T) {
	got := countChangedLines("3\t1\tfoo.go\n-\t-\tbinary.png\n0\t5\tbar.go\n")
	if got != 9 {
		t.Fatalf("countChangedLines = %d, want 9", got)
	}
	if got := countChangedLines(""); got != 0 {
		t.Fatalf("countChangedLines(\"\") = %d, want 0", got)
	}
}

func TestCommitLogNoMerges(t *testing.T) {
	ctx := context.Background()
	remote, headSHA, baseSHA := makeRemote(t, "refs/merge-requests/%d/head", 1)
	g := gitlabEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/1/head:refs/cr/1", "main", 1); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}

	log, err := commitLog(ctx, g, repo, baseSHA, headSHA)
	if err != nil {
		t.Fatalf("commitLog: %v", err)
	}
	if !strings.Contains(log, "add a") {
		t.Fatalf("commitLog = %q, want it to contain %q", log, "add a")
	}
}
