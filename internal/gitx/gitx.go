// Package gitx runs git against the repos nightshift touches: the clones
// it creates, their source repos, and (optionally) a git-tracked home.
package gitx

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/lock"
)

// Run runs git with args in dir and returns its combined output with
// surrounding whitespace trimmed.
func Run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// IsRepo reports whether dir is inside a git work tree.
func IsRepo(dir string) bool {
	_, err := Run(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// GitDir resolves dir's git directory for the given rev-parse flag (e.g.
// "--git-dir" or "--git-common-dir") to an absolute, symlink-resolved path.
func GitDir(dir, flag string) (string, error) {
	out, err := Run(dir, "rev-parse", flag)
	if err != nil {
		return "", fmt.Errorf("git rev-parse %s: %v: %s", flag, err, out)
	}
	p := out
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("resolve git dir %s: %w", p, err)
	}
	return resolved, nil
}

// retryableLockErr matches git's own transient lock-contention errors,
// which clear on their own once the other process finishes.
var retryableLockErr = regexp.MustCompile(`index\.lock|cannot lock ref|packed-refs\.lock|Unable to create .*\.lock|fetching ref .* failed: reference already exists`)

const retryMaxAttempts = 5

var retryBaseBackoff = 200 * time.Millisecond

// RunRetry runs git like Run, retrying up to 5 attempts total (with
// backoff doubling from 200ms) when the output looks like a transient
// lock-contention error from another git process.
func RunRetry(dir string, args ...string) (string, error) {
	backoff := retryBaseBackoff
	var out string
	var err error
	for attempt := 0; attempt < retryMaxAttempts; attempt++ {
		out, err = Run(dir, args...)
		if err == nil || !retryableLockErr.MatchString(out) {
			return out, err
		}
		if attempt == retryMaxAttempts-1 {
			break
		}
		time.Sleep(backoff)
		backoff *= 2
	}
	return out, err
}

// Commit stages exactly paths (relative to dir) and commits them with msg,
// no-oping if nothing is staged once added. Concurrent nightshift
// processes committing in the same repo are serialized via a lock file at
// <git-dir>/nightshift-commit.lock.
func Commit(dir, msg string, paths ...string) error {
	gitDir, err := GitDir(dir, "--git-dir")
	if err != nil {
		return fmt.Errorf("resolve git dir for %s: %w", dir, err)
	}
	lockPath := filepath.Join(gitDir, "nightshift-commit.lock")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	l, err := lock.Acquire(ctx, lockPath, "gitx-commit")
	if err != nil {
		return fmt.Errorf("commit lock for %s: %w", dir, err)
	}
	defer l.Release()

	addArgs := append([]string{"add", "-A", "--"}, paths...)
	if out, err := Run(dir, addArgs...); err != nil {
		return fmt.Errorf("git add: %v: %s", err, out)
	}

	diffArgs := append([]string{"diff", "--cached", "--quiet", "--"}, paths...)
	if _, err := Run(dir, diffArgs...); err == nil {
		return nil
	}

	commitArgs := append([]string{"commit", "-m", msg, "--"}, paths...)
	if out, err := Run(dir, commitArgs...); err != nil {
		return fmt.Errorf("git commit: %v: %s", err, out)
	}
	return nil
}
