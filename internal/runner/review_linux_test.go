package runner

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestReviewWorkspaceNotHardlinked confirms buildReviewWorkspace's clone
// used --no-local: every regular file under ws/repo/.git/objects has
// nlink == 1, meaning it does not share an inode with the source repo's
// own object store.
func TestReviewWorkspaceNotHardlinked(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")

	ws := filepath.Join(t.TempDir(), "review-ws")
	if _, err := buildReviewWorkspace(context.Background(), ws, repo, base, "main", "", "", ""); err != nil {
		t.Fatalf("buildReviewWorkspace: %v", err)
	}

	objects := filepath.Join(ws, "repo", ".git", "objects")
	checked := false
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
	if !checked {
		t.Fatalf("no object files found under .git/objects to check")
	}
}
