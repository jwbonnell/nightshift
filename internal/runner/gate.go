package runner

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jwbonnell/nightshift/internal/home"
)

// gateWSDir is the throwaway gate workspace's parent; the clone lives in
// its repo/ subdirectory. gateLogDir holds each gate's output.
func (r *run) gateWSDir() string  { return filepath.Join(r.dir, "gate-ws") }
func (r *run) gateLogDir() string { return filepath.Join(r.dir, "gate") }

// buildGateWorkspace rebuilds ws and clones the source repo at ref into
// ws/repo (--no-local, detached, no remotes). Returns the clone dir.
func buildGateWorkspace(ws, repo, ref string) (string, error) {
	if err := os.RemoveAll(ws); err != nil {
		return "", err
	}
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return "", err
	}
	dir, _, err := cloneAtRef(ws, "repo", repo, ref)
	return dir, err
}

// overlayHoldouts copies every file under holdDir into repoDir at the same
// relative path, never following a symlink on either side and never
// overwriting. Returns the number of files written. A missing holdDir is
// zero files.
func overlayHoldouts(holdDir, repoDir string) (int, error) {
	if _, err := os.Lstat(holdDir); err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	err := filepath.WalkDir(holdDir, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(holdDir, path)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("holdout %s is not a regular file", rel)
		}
		if err := overlayOne(path, repoDir, rel); err != nil {
			return err
		}
		n++
		return nil
	})
	return n, err
}

func overlayOne(src, repoDir, rel string) error {
	parts := strings.Split(rel, string(filepath.Separator))
	cur := repoDir
	for i, p := range parts {
		cur = filepath.Join(cur, p)
		last := i == len(parts)-1
		fi, err := os.Lstat(cur)
		switch {
		case err == nil:
			if fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("holdout path %s crosses a symlink", rel)
			}
			if last {
				return fmt.Errorf("holdout file %s already exists in the repo", rel)
			}
			if !fi.IsDir() {
				return fmt.Errorf("holdout path %s crosses a non-directory", rel)
			}
		case os.IsNotExist(err):
			if !last {
				if err := os.Mkdir(cur, 0o755); err != nil {
					return err
				}
			}
		default:
			return err
		}
	}
	in, err := os.OpenFile(src, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(cur, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("holdout file %s already exists in the repo", rel)
		}
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

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
