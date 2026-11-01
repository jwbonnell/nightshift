package runner

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/jwbonnell/nightshift/internal/home"
)

// holdoutSlugDir is where the owner keeps this spec's holdout files.
func holdoutSlugDir(h, slug string) string {
	return filepath.Join(home.HoldoutDir(h), slug)
}

// countHoldoutFiles counts non-directory entries under the slug's holdout
// dir; a missing dir is zero.
func countHoldoutFiles(h, slug string) int {
	n := 0
	_ = filepath.WalkDir(holdoutSlugDir(h, slug), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// checkHoldoutOutsideRepo refuses a holdout dir that resolves inside the
// spec's repo, where the agent's clone would carry it.
func checkHoldoutOutsideRepo(h, slug, repo string) error {
	dir := holdoutSlugDir(h, slug)
	holdReal, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return nil // missing: nothing to protect
	}
	repoReal, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return nil
	}
	rel, err := filepath.Rel(repoReal, holdReal)
	if err != nil {
		return nil
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("holdout dir %s must be outside the repo", dir)
	}
	return nil
}
