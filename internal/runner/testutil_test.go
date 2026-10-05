package runner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

// newSourceRepo creates a repo with one commit on branch "main".
func newSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.email", "a@b.c")
	mustGit(t, dir, "config", "user.name", "a")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", "initial")
	return dir
}

// cloneRepo clones src into a fresh directory, giving the clone a normal
// origin remote (and origin/HEAD) as a real `git clone` would.
func cloneRepo(t *testing.T, src string) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "clone")
	if out, err := gitx.Run(t.TempDir(), "clone", "-q", src, dst); err != nil {
		t.Fatalf("clone: %v: %s", err, out)
	}
	return dst
}

// writeReadySpec writes a minimal spec with repo/verify plus any extra
// frontmatter lines directly into <homeDir>/specs/ready/<slug>.md.
func writeReadySpec(t *testing.T, homeDir, slug, extraFrontmatter, repo string) string {
	t.Helper()
	if err := os.MkdirAll(home.ReadyDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir ready: %v", err)
	}
	content := "---\nrepo: " + repo + "\nverify: true\n" + extraFrontmatter + "---\n\n# spec\n"
	path := filepath.Join(home.ReadyDir(homeDir), slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

// writeDraftSpec is the draft/ equivalent of writeReadySpec.
func writeDraftSpec(t *testing.T, homeDir, slug, extraFrontmatter, repo string) string {
	t.Helper()
	if err := os.MkdirAll(home.DraftDir(homeDir), 0o755); err != nil {
		t.Fatalf("mkdir draft: %v", err)
	}
	content := "---\nrepo: " + repo + "\nverify: true\n" + extraFrontmatter + "---\n\n# spec\n"
	path := filepath.Join(home.DraftDir(homeDir), slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}
