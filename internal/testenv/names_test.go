package testenv

import (
	"bytes"
	"fmt"
	"io/fs"
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

	var files []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if d.Name() == ".git" || (rel == "vendor" && d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "-C", root, "check-ignore", "--stdin", "-z")
	cmd.Stdin = strings.NewReader(strings.Join(files, "\x00") + "\x00")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	ignored := map[string]bool{}
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 1 {
			t.Fatalf("git check-ignore: %v", err)
		}
	}
	for _, p := range strings.Split(stdout.String(), "\x00") {
		if p != "" {
			ignored[p] = true
		}
	}
	var keep []string
	for _, f := range files {
		if !ignored[f] {
			keep = append(keep, f)
		}
	}
	for _, msg := range findOriginNames(root, keep) {
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
