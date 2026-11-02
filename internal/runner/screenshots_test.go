package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseScreenshotsDir(t *testing.T) {
	cases := []struct {
		name, in, dir string
		ok            bool
	}{
		{"trailing slash", "screenshots: build/screens/\n", "build/screens", true},
		{"no trailing slash", "screenshots: build/screens\n", "build/screens", true},
		{"no space", "screenshots:out\n", "out", true},
		{"after other lines", "# hi\nverify: x\nscreenshots: a/b/\nmore\n", "a/b", true},
		{"crlf", "screenshots: a/b/\r\nx\r\n", "a/b", true},
		{"first wins", "screenshots: one\nscreenshots: two\n", "one", true},
		{"empty input", "", "", false},
		{"no match", "verify: x\n", "", false},
		{"bare at eof", "screenshots:", "", false},
		{"bare then text", "screenshots:\nfoo\n", "", false},
		{"bare trailing spaces then text", "screenshots:  \nfoo\n", "", false},
		{"mid-line", "foo screenshots: x\n", "", false},
		{"absolute", "screenshots: /etc\n", "", false},
		{"dotdot", "screenshots: ../x\n", "", false},
		{"only dotdot", "screenshots: ..\n", "", false},
		{"dot", "screenshots: ./\n", "", false},
		{"slash only", "screenshots: /\n", "", false},
	}
	for _, c := range cases {
		dir, ok := parseScreenshotsDir([]byte(c.in))
		if dir != c.dir || ok != c.ok {
			t.Errorf("%s: got (%q, %v), want (%q, %v)", c.name, dir, ok, c.dir, c.ok)
		}
	}
}

func TestRepoScreenshotsDir(t *testing.T) {
	if _, ok := repoScreenshotsDir(""); ok {
		t.Error("empty repo must be not declared")
	}
	dir := t.TempDir()
	if _, ok := repoScreenshotsDir(dir); ok {
		t.Error("missing file must be not declared")
	}
	if err := os.WriteFile(filepath.Join(dir, ".nightshift.md"), []byte("screenshots: a/b/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d, ok := repoScreenshotsDir(dir); !ok || d != "a/b" {
		t.Errorf("got (%q, %v)", d, ok)
	}
	// A symlinked .nightshift.md is never followed.
	link := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, ".nightshift.md"), filepath.Join(link, ".nightshift.md")); err != nil {
		t.Fatal(err)
	}
	if _, ok := repoScreenshotsDir(link); ok {
		t.Error("symlinked .nightshift.md must be not declared")
	}
}
