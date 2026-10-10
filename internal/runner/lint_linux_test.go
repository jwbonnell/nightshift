package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

// TestLintTempCloneNotHardlinked confirms check 8's temp clone used
// --no-local: every regular file under .git/objects has nlink == 1 at
// the moment Verify is called, meaning it does not share an inode with
// the source repo's own object store (a hardlinked object would let a
// hostile verify command corrupt the source repo in place).
func TestLintTempCloneNotHardlinked(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

	checked := false
	verifier := stubVerifier{fn: func(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
		objects := filepath.Join(dir, ".git", "objects")
		err := filepath.Walk(objects, func(path string, fi os.FileInfo, walkErr error) error {
			if walkErr != nil || fi.IsDir() {
				return walkErr
			}
			checked = true
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				t.Fatalf("no syscall.Stat_t for %s", path)
			}
			if st.Nlink != 1 {
				t.Fatalf("%s has nlink %d, want 1 (object is hardlinked to the source repo)", path, st.Nlink)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk objects: %v", err)
		}
		return "ok", nil
	}}

	var buf bytes.Buffer
	if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Verifier: verifier, Stdout: &buf}); err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !checked {
		t.Fatalf("no object files found under .git/objects to check")
	}
}
