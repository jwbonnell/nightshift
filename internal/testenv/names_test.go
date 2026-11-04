package testenv

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	HermeticGit()
	os.Exit(m.Run())
}

// forbiddenNames are built from pieces so this file doesn't trip itself.
var forbiddenNames = []string{"pen" + "sieve", "spec" + "-run", "spec" + "run"}

// findOriginNames reports "file:line: mentions word" for every line of the
// given files (slash-relative to root) containing a forbidden name, and
// "file: file name mentions word" for every path that does. Binary files
// (NUL in the first 8KB) are not scanned.
func findOriginNames(root string, files []string) []string {
	var out []string
	for _, rel := range files {
		lp := strings.ToLower(rel)
		for _, w := range forbiddenNames {
			if strings.Contains(lp, w) {
				out = append(out, fmt.Sprintf("%s: file name mentions %q", rel, w))
			}
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			out = append(out, fmt.Sprintf("%s: %v", rel, err))
			continue
		}
		head := data
		if len(head) > 8192 {
			head = head[:8192]
		}
		if bytes.IndexByte(head, 0) >= 0 {
			continue
		}
		for i, line := range strings.Split(strings.ToLower(string(data)), "\n") {
			for _, w := range forbiddenNames {
				if strings.Contains(line, w) {
					out = append(out, fmt.Sprintf("%s:%d: mentions %q", rel, i+1, w))
				}
			}
		}
	}
	return out
}

// trackedFiles returns the slash-relative paths git tracks under root, minus
// anything under vendor/. It skips the test outside a git work tree.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	if err := exec.Command("git", "-C", root, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		t.Skip("not a git work tree")
	}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	var files []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || strings.HasPrefix(p, "vendor/") {
			continue
		}
		files = append(files, p)
	}
	return files
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTrackedFilesFilters(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, dir, ".gitignore", "bin/\n")
	writeFile(t, dir, "a.txt", "a\n")
	writeFile(t, dir, "vendor/x.txt", "x\n")
	gitIn(t, dir, "add", "-f", "a.txt", "vendor/x.txt")
	writeFile(t, dir, "REVIEW.md", "r\n")
	writeFile(t, dir, "bin/y", "y\n")
	got := trackedFiles(t, dir)
	if len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("trackedFiles = %q, want [a.txt]", got)
	}
}

func TestNoOriginToolNamesIgnoresUntracked(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	writeFile(t, dir, "note.md", "mentions "+"Pen"+"Sieve"+"\n")
	if got := findOriginNames(dir, trackedFiles(t, dir)); len(got) != 0 {
		t.Errorf("untracked file produced findings: %q", got)
	}
	gitIn(t, dir, "add", "note.md")
	if got := findOriginNames(dir, trackedFiles(t, dir)); len(got) != 1 {
		t.Errorf("tracked file findings = %q, want 1", got)
	}
}

// TestNoOriginToolNames guards against the names of the tools nightshift was
// reimplemented from coming back into any tracked, non-vendored file.
func TestNoOriginToolNames(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := wd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("go.mod not found above " + wd)
		}
		root = parent
	}

	files := trackedFiles(t, root)
	for _, msg := range findOriginNames(root, files) {
		t.Error(msg)
	}
}

func TestNoOriginToolNamesDetects(t *testing.T) {
	dir := t.TempDir()
	a := "Pen" + "Sieve"
	b := "spec" + "run" + ".go"
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("ok\nsee "+a+" here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, b), []byte("clean\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(findOriginNames(dir, []string{"a.txt", b}), "\n")
	if !strings.Contains(got, "a.txt:2:") {
		t.Errorf("missing a.txt:2 in %q", got)
	}
	if !strings.Contains(got, b+": file name mentions") {
		t.Errorf("missing file name report in %q", got)
	}
}
