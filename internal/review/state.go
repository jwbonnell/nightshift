package review

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

// ensureState creates <Home>/pr's three persistent subdirectories —
// repos (bare clones), work (per-review scratch trees) and logs — each
// 0o700, mirroring the reference's StateDir layout (P5).
func ensureState(homeDir string) error {
	prDir := home.PRDir(homeDir)
	for _, name := range []string{"repos", "work", "logs"} {
		d := filepath.Join(prDir, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create state dir %s: %w", d, err)
		}
	}
	return nil
}

// clearWork wipes any leftover per-review scratch trees from a previous
// run (os.RemoveAll never follows a symlink into deleting something
// else) and recreates an empty work dir, 0o700.
func clearWork(homeDir string) error {
	work := filepath.Join(home.PRDir(homeDir), "work")
	if err := os.RemoveAll(work); err != nil {
		return fmt.Errorf("clear leftover work dir %s: %w — remove it by hand and re-run", work, err)
	}
	if err := os.MkdirAll(work, 0o700); err != nil {
		return fmt.Errorf("recreate work dir %s: %w", work, err)
	}
	return nil
}

// acquirePRLock acquires <Home>/pr/.lock (internal/lock, label "pr"),
// translating a held lock into P5's own message — just the holder's pid,
// since the caller already knows this is nightshift pr's own lock.
func acquirePRLock(homeDir string) (*lock.Lock, error) {
	l, err := lock.TryAcquire(filepath.Join(home.PRDir(homeDir), ".lock"), "pr")
	if err != nil {
		var held *lock.HeldError
		if errors.As(err, &held) {
			return nil, fmt.Errorf("another nightshift pr run is in progress (pid %d) — wait for it to finish", held.Holder.PID)
		}
		return nil, err
	}
	return l, nil
}

// appendFailure appends one line to <Home>/pr/logs/failures.log
// recording a failed review, so it's retried on the next run (no note is
// ever written for a failure). A failure to write the log itself is
// printed to stderr and never returned — a logging failure must never
// fail the review that's already failing.
func appendFailure(homeDir string, now time.Time, slug string, reviewErr error, stderr io.Writer) {
	logsDir := filepath.Join(home.PRDir(homeDir), "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "nightshift pr: log failure: %v\n", err)
		return
	}
	f, err := os.OpenFile(filepath.Join(logsDir, "failures.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "nightshift pr: log failure: %v\n", err)
		return
	}
	defer f.Close()
	line := fmt.Sprintf("%s %s %s\n", now.UTC().Format(time.RFC3339), slug, strings.ReplaceAll(reviewErr.Error(), "\n", " "))
	if _, err := f.WriteString(line); err != nil {
		fmt.Fprintf(stderr, "nightshift pr: log failure: %v\n", err)
	}
}

// nopWriteCloser adapts an io.Writer (namely io.Discard) to
// io.WriteCloser for agentLogWriter's failure path.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// agentLogWriter opens <Home>/pr/logs/agent.log for append (0600) and
// writes a "=== <UTC RFC3339> <label> ===" header, for use as one
// review's agent stderr — the P1 rename of the reference's claude.log,
// generalized to any agent. On any open error it falls back to a no-op
// writer, so a logging failure never blocks a review; the caller closes
// the returned writer after the review.
func agentLogWriter(homeDir, label string, now time.Time) io.WriteCloser {
	logsDir := filepath.Join(home.PRDir(homeDir), "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		return nopWriteCloser{io.Discard}
	}
	f, err := os.OpenFile(filepath.Join(logsDir, "agent.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nopWriteCloser{io.Discard}
	}
	fmt.Fprintf(f, "=== %s %s ===\n", now.UTC().Format(time.RFC3339), label)
	return f
}
