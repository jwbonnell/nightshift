// Package lock implements a cross-process file lock used to serialize
// nightshift's own writers (spec runs, --next selection, repo refs). It is
// std-lib only, so internal/gitx can import it without an import cycle.
package lock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Info describes a lock's holder, as recorded in the lock file.
type Info struct {
	PID   int
	Label string
	Start time.Time
	Boot  int64
	Nonce string
}

// Lock represents a held lock. The zero value is not valid; obtain one via
// TryAcquire or Acquire.
type Lock struct {
	path     string
	contents []byte
}

// Path returns the lock file's path.
func (l *Lock) Path() string { return l.path }

// Release releases the lock if its contents on disk are still exactly what
// this Lock wrote (so it never removes a lock reclaimed by someone else).
// It is a no-op on a nil receiver, and always returns nil.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	removeIfMatch(l.path, l.contents)
	return nil
}

// HeldError reports that a lock is held by another, live process.
type HeldError struct {
	Path   string
	Holder Info
}

func (e *HeldError) Error() string {
	return fmt.Sprintf("%s is held by pid %d (%s, since %s); if that process is gone, remove the file by hand",
		e.Path, e.Holder.PID, e.Holder.Label, e.Holder.Start.Format(time.RFC3339))
}

var _ error = (*HeldError)(nil)

// Tunables, overridable in tests.
var (
	reclaimGuardWait    = 35 * time.Second
	reclaimPollInterval = 50 * time.Millisecond
	guardStaleAge       = 30 * time.Second
	acquireBackoffMin   = 25 * time.Millisecond
	acquireBackoffMax   = 250 * time.Millisecond
	staleUnreadableAge  = 5 * time.Second
)

const maxAcquireAttempts = 100

// Test seams; no-ops in production.
var (
	afterLinkExists     = func(path string) {}
	afterReadErr        = func(path string, err error) {}
	beforeReclaimRemove = func(path string) {}
)

// pidAlive reports whether pid names a live process. A process owned by
// another user still answers the zero-signal probe with EPERM, which counts
// as alive (we can't tell it apart from a live process we can't signal).
var pidAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	serr := p.Signal(syscall.Signal(0))
	return serr == nil || errors.Is(serr, syscall.EPERM)
}

// bootTime wraps hostBootTime so tests can fake the current boot time.
var bootTime = func() (int64, bool) {
	return hostBootTime()
}

func newNonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func encode(info Info) []byte {
	return []byte(fmt.Sprintf("%d\n%s\n%s\n%d\n%s\n",
		info.PID, info.Label, info.Start.Format(time.RFC3339Nano), info.Boot, info.Nonce))
}

func decode(b []byte) (Info, error) {
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	if len(lines) < 5 {
		return Info{}, errors.New("malformed lock contents")
	}
	pid, err := strconv.Atoi(lines[0])
	if err != nil {
		return Info{}, fmt.Errorf("malformed lock pid: %w", err)
	}
	start, err := time.Parse(time.RFC3339Nano, lines[2])
	if err != nil {
		return Info{}, fmt.Errorf("malformed lock start time: %w", err)
	}
	boot, err := strconv.ParseInt(lines[3], 10, 64)
	if err != nil {
		return Info{}, fmt.Errorf("malformed lock boot time: %w", err)
	}
	return Info{PID: pid, Label: lines[1], Start: start, Boot: boot, Nonce: lines[4]}, nil
}

// Read reads and decodes the lock file at path, also returning the raw
// bytes read (even when decoding fails, so callers can compare them).
func Read(path string) (Info, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Info{}, nil, err
	}
	info, err := decode(raw)
	return info, raw, err
}

// publishBytes atomically creates path with exactly contents, failing with
// an os.IsExist error if path already exists. Readers never observe a
// partially-written file.
func publishBytes(path string, contents []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-lock-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	_, werr := tmp.Write(contents)
	cerr := tmp.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}

	return os.Link(tmpPath, path)
}

// removeIfMatch removes path only if its current contents are exactly
// expected, so a lock is never removed out from under a new holder.
func removeIfMatch(path string, expected []byte) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	if !bytes.Equal(raw, expected) {
		return false
	}
	return os.Remove(path) == nil
}

// lockChanged reports whether path's current contents differ from judged
// (including the case where path no longer exists).
func lockChanged(path string, judged []byte) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return os.IsNotExist(err)
	}
	return !bytes.Equal(raw, judged)
}

// isStale reports whether info's process is gone, or belongs to a
// different boot of the machine (PIDs are reused across reboots).
func isStale(info Info) bool {
	if !pidAlive(info.PID) {
		return true
	}
	cur, ok := bootTime()
	if ok && info.Boot != 0 {
		diff := info.Boot - cur
		if diff < 0 {
			diff = -diff
		}
		if diff > 60 {
			return true
		}
	}
	return false
}

// TryAcquire attempts to create the lock file at path once, reclaiming it
// if its holder is stale. It never blocks waiting for a live holder; use
// Acquire for that.
func TryAcquire(path, label string) (*Lock, error) {
	info := Info{PID: os.Getpid(), Label: label, Start: time.Now(), Nonce: newNonce()}
	if b, ok := bootTime(); ok {
		info.Boot = b
	}
	contents := encode(info)

	for attempt := 0; attempt < maxAcquireAttempts; attempt++ {
		err := publishBytes(path, contents)
		if err == nil {
			return &Lock{path: path, contents: contents}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		afterLinkExists(path)

		existing, raw, derr := Read(path)
		if derr != nil {
			afterReadErr(path, derr)
			if os.IsNotExist(derr) {
				continue
			}
			fi, serr := os.Stat(path)
			if serr != nil {
				continue
			}
			if time.Since(fi.ModTime()) <= staleUnreadableAge {
				return nil, fmt.Errorf("lock %s has unreadable contents and is not yet stale", path)
			}
			if reclaimStale(path, raw) || lockChanged(path, raw) {
				continue
			}
			return nil, fmt.Errorf("could not reclaim unreadable stale lock %s", path)
		}

		if !isStale(existing) {
			return nil, &HeldError{Path: path, Holder: existing}
		}
		if reclaimStale(path, raw) {
			continue
		}
		if lockChanged(path, raw) {
			continue
		}
		return nil, fmt.Errorf("could not acquire lock %s: reclaim contention", path)
	}
	return nil, fmt.Errorf("could not acquire lock %s: gave up after %d attempts (lock kept changing)", path, maxAcquireAttempts)
}

// reclaimStale removes path, but only while holding a short-lived guard
// file (path+".reclaim") that serializes concurrent reclaimers, and only if
// path's contents are still exactly staleBytes when the guard is won.
func reclaimStale(path string, staleBytes []byte) bool {
	guardPath := path + ".reclaim"
	my := Info{PID: os.Getpid(), Label: "reclaim", Start: time.Now(), Nonce: newNonce()}
	if b, ok := bootTime(); ok {
		my.Boot = b
	}
	myContents := encode(my)
	deadline := time.Now().Add(reclaimGuardWait)

	for {
		err := publishBytes(guardPath, myContents)
		if err == nil {
			beforeReclaimRemove(path)
			removed := removeIfMatch(path, staleBytes)
			removeIfMatch(guardPath, myContents)
			return removed
		}
		if !os.IsExist(err) {
			return false
		}

		gInfo, gRaw, derr := Read(guardPath)
		if derr != nil {
			fi, serr := os.Stat(guardPath)
			if serr != nil {
				continue // guard vanished; try to win it again
			}
			if time.Since(fi.ModTime()) > guardStaleAge {
				os.Remove(guardPath)
				continue
			}
		} else if isStale(gInfo) || time.Since(gInfo.Start) > guardStaleAge {
			removeIfMatch(guardPath, gRaw)
			continue
		}

		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(reclaimPollInterval)
	}
}

// Acquire blocks, retrying with exponential backoff, until it holds the
// lock at path or ctx is done. On ctx expiring it returns the last error
// TryAcquire produced (typically a *HeldError, so errors.As still works).
func Acquire(ctx context.Context, path, label string) (*Lock, error) {
	backoff := acquireBackoffMin
	var lastErr error
	for {
		l, err := TryAcquire(path, label)
		if err == nil {
			return l, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return nil, lastErr
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > acquireBackoffMax {
			backoff = acquireBackoffMax
		}
	}
}

// Probe reports the lock's recorded holder and whether it still looks live,
// without creating or modifying anything. An unreadable or missing lock
// file counts as not held.
func Probe(path string) (Info, bool) {
	info, _, err := Read(path)
	if err != nil {
		return Info{}, false
	}
	return info, !isStale(info)
}
