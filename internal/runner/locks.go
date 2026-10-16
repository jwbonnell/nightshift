package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/lock"
)

func specLockPath(stateRoot, slug string) string {
	return filepath.Join(stateRoot, ".locks", "spec-"+slug+".lock")
}

func slotLockPath(stateRoot string, n int) string {
	return filepath.Join(stateRoot, ".locks", fmt.Sprintf("slot-%d.lock", n))
}

// repoHash is a stable 12-hex-char id for repoPath's git-common-dir, used
// to name its repo lock file. Worktrees and symlinked repo paths that
// share a git dir canonicalize to the same hash.
func repoHash(repoPath string) (string, error) {
	gitDir, err := gitx.GitDir(repoPath, "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("resolve git common dir for %s: %w", repoPath, err)
	}
	sum := sha256.Sum256([]byte(gitDir))
	return hex.EncodeToString(sum[:])[:12], nil
}

var repoLockWait = 2 * time.Minute

// withRepoLock serializes local ref writes against repoPath across
// concurrent nightshift processes.
func withRepoLock(stateRoot, repoPath, label string, fn func() error) error {
	hash, err := repoHash(repoPath)
	if err != nil {
		return err
	}
	lockPath := filepath.Join(stateRoot, ".locks", "repo-"+hash+".lock")
	ctx, cancel := context.WithTimeout(context.Background(), repoLockWait)
	defer cancel()
	l, err := lock.Acquire(ctx, lockPath, label)
	if err != nil {
		return fmt.Errorf("repo lock for %s: %w", repoPath, err)
	}
	defer l.Release()
	return fn()
}

// acquireSlot tries each of the limit concurrency slots in order,
// returning the first it holds. A held slot is skipped; nil, nil means
// every slot is currently held.
func acquireSlot(stateRoot string, limit int, slug string) (*lock.Lock, error) {
	for i := 1; i <= limit; i++ {
		l, err := lock.TryAcquire(slotLockPath(stateRoot, i), slug)
		if err == nil {
			return l, nil
		}
		var held *lock.HeldError
		if errors.As(err, &held) {
			continue
		}
		return nil, err
	}
	return nil, nil
}

// runningSlots returns the labels of every currently-held slot, in slot
// order.
func runningSlots(stateRoot string, limit int) []string {
	var labels []string
	for i := 1; i <= limit; i++ {
		info, held := lock.Probe(slotLockPath(stateRoot, i))
		if held {
			labels = append(labels, info.Label)
		}
	}
	return labels
}

func joinOrNone(list []string) string {
	if len(list) == 0 {
		return "(none? race)"
	}
	return strings.Join(list, ", ")
}

// printHeldLocks is read-only and never creates <stateRoot>/.locks.
func printHeldLocks(w io.Writer, stateRoot string) {
	entries, err := os.ReadDir(filepath.Join(stateRoot, ".locks"))
	if err != nil {
		fmt.Fprint(w, "held locks:  none\n")
		return
	}
	var lines []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if (!strings.HasPrefix(name, "spec-") && !strings.HasPrefix(name, "slot-")) || !strings.HasSuffix(name, ".lock") {
			continue
		}
		info, held := lock.Probe(filepath.Join(stateRoot, ".locks", name))
		if !held {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s: pid %d (%s, since %s)", name, info.PID, info.Label, info.Start.Format(time.RFC3339)))
	}
	if len(lines) == 0 {
		fmt.Fprint(w, "held locks:  none\n")
		return
	}
	fmt.Fprint(w, "held locks:\n"+strings.Join(lines, "\n")+"\n")
}
