package runner

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"syscall"
)

// countHardlinks walks clone/.git/objects, checking that every regular
// file has a link count of 1. A clone made without --no-local hardlinks
// its objects to the source repo's — see cloneRepo's comment on why that
// is dangerous once the agent sandbox mounts the clone read-write.
func countHardlinks(clone string) checkLine {
	dir := filepath.Join(clone, ".git", "objects")
	var hardlinked int
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
			hardlinked++
		}
		return nil
	})
	if err != nil {
		return checkLine{"FAIL", "clone-objects: " + err.Error()}
	}
	if hardlinked == 0 {
		return checkLine{"ok", "clone-objects"}
	}
	return checkLine{"FAIL", fmt.Sprintf("clone-objects: %d hardlinked file(s)", hardlinked)}
}
