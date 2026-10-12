package review

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

func TestEnsureState(t *testing.T) {
	homeDir := t.TempDir()
	if err := ensureState(homeDir); err != nil {
		t.Fatalf("ensureState: %v", err)
	}
	for _, name := range []string{"repos", "work", "logs"} {
		d := filepath.Join(home.PRDir(homeDir), name)
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("stat %s: %v", d, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", d)
		}
		if info.Mode().Perm() != 0o700 {
			t.Errorf("%s mode = %o, want 0700", d, info.Mode().Perm())
		}
	}

	// Calling it again on an already-populated state dir must not error.
	if err := ensureState(homeDir); err != nil {
		t.Fatalf("ensureState (second call): %v", err)
	}
}

func TestClearWork(t *testing.T) {
	homeDir := t.TempDir()
	if err := ensureState(homeDir); err != nil {
		t.Fatalf("ensureState: %v", err)
	}
	work := filepath.Join(home.PRDir(homeDir), "work")
	leftover := filepath.Join(work, "stale-slug", "tree", "file.txt")
	if err := os.MkdirAll(filepath.Dir(leftover), 0o700); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}
	if err := os.WriteFile(leftover, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}

	if err := clearWork(homeDir); err != nil {
		t.Fatalf("clearWork: %v", err)
	}

	entries, err := os.ReadDir(work)
	if err != nil {
		t.Fatalf("read work dir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("work dir not empty after clearWork: %v", entries)
	}
	info, err := os.Stat(work)
	if err != nil {
		t.Fatalf("stat work dir: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf("work dir mode = %o, want 0700", info.Mode().Perm())
	}
}

func TestClearWorkWithNoLeftovers(t *testing.T) {
	homeDir := t.TempDir()
	if err := clearWork(homeDir); err != nil {
		t.Fatalf("clearWork on a fresh home: %v", err)
	}
}

func TestAppendFailure(t *testing.T) {
	homeDir := t.TempDir()
	now := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

	var stderr bytes.Buffer
	appendFailure(homeDir, now, "team-app-mr12", errors.New("clone: boom\nmore detail"), &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr on a healthy write: %q", stderr.String())
	}

	logPath := filepath.Join(home.PRDir(homeDir), "logs", "failures.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read failures.log: %v", err)
	}
	line := strings.TrimRight(string(data), "\n")
	want := "2026-10-12T09:30:00Z team-app-mr12 clone: boom more detail"
	if line != want {
		t.Errorf("failures.log line = %q, want %q", line, want)
	}

	info, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("stat failures.log: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("failures.log mode = %o, want 0600", info.Mode().Perm())
	}

	// A second failure appends rather than overwriting.
	appendFailure(homeDir, now, "other-mr3", errors.New("fetch: nope"), &stderr)
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read failures.log: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("failures.log lines = %v, want 2", lines)
	}
}

func TestAppendFailureLogErrorGoesToStderr(t *testing.T) {
	homeDir := t.TempDir()
	// Make <Home>/pr/logs unwritable-as-a-dir by putting a file there
	// instead, so MkdirAll(logs, ...) fails.
	prDir := home.PRDir(homeDir)
	if err := os.MkdirAll(prDir, 0o700); err != nil {
		t.Fatalf("seed pr dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prDir, "logs"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed logs file: %v", err)
	}

	var stderr bytes.Buffer
	appendFailure(homeDir, time.Now(), "slug", errors.New("boom"), &stderr)
	if !strings.Contains(stderr.String(), "nightshift pr: log failure:") {
		t.Errorf("stderr = %q, want a log failure message", stderr.String())
	}
}

func TestAgentLogWriter(t *testing.T) {
	homeDir := t.TempDir()
	now := time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

	w := agentLogWriter(homeDir, "team/app!12", now)
	if _, err := w.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(home.PRDir(homeDir), "logs", "agent.log"))
	if err != nil {
		t.Fatalf("read agent.log: %v", err)
	}
	want := "=== 2026-10-12T09:30:00Z team/app!12 ===\nhello\n"
	if string(data) != want {
		t.Errorf("agent.log = %q, want %q", data, want)
	}
}

func TestAgentLogWriterFallsBackOnOpenError(t *testing.T) {
	homeDir := t.TempDir()
	prDir := home.PRDir(homeDir)
	if err := os.MkdirAll(prDir, 0o700); err != nil {
		t.Fatalf("seed pr dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(prDir, "logs"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed logs file: %v", err)
	}

	w := agentLogWriter(homeDir, "label", time.Now())
	if _, err := w.Write([]byte("anything")); err != nil {
		t.Fatalf("write to fallback writer should never error: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close fallback writer should never error: %v", err)
	}
}

func TestPRLockHeld(t *testing.T) {
	t.Run("acquirePRLock", func(t *testing.T) {
		homeDir := t.TempDir()

		l, err := acquirePRLock(homeDir)
		if err != nil {
			t.Fatalf("acquirePRLock (first): %v", err)
		}
		defer l.Release()

		_, err = acquirePRLock(homeDir)
		if err == nil {
			t.Fatal("acquirePRLock (second, while held) succeeded, want an error")
		}
		wantPrefix := "another nightshift pr run is in progress (pid "
		if !strings.HasPrefix(err.Error(), wantPrefix) || !strings.HasSuffix(err.Error(), ") — wait for it to finish") {
			t.Errorf("error = %q, want prefix %q and the P5 suffix", err.Error(), wantPrefix)
		}

		var held *lock.HeldError
		if errors.As(err, &held) {
			t.Errorf("acquirePRLock must translate *lock.HeldError into its own message, not leak it: %v", err)
		}
	})

	t.Run("Run-level", func(t *testing.T) {
		homeDir := t.TempDir()

		l, err := acquirePRLock(homeDir)
		if err != nil {
			t.Fatalf("acquirePRLock: %v", err)
		}
		defer l.Release()

		leftover := filepath.Join(home.PRDir(homeDir), "work", "leftover")
		if err := os.MkdirAll(filepath.Dir(leftover), 0o700); err != nil {
			t.Fatalf("mkdir work: %v", err)
		}
		if err := os.WriteFile(leftover, []byte("stale"), 0o600); err != nil {
			t.Fatalf("seed leftover: %v", err)
		}

		client := &fakeClient{kind: forge.KindGitLab}
		sb := &stubSandbox{available: true}
		opts := baseOptions(t, homeDir, client, sb)
		opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
		var stderr strings.Builder
		opts.Stderr = &stderr

		if rc := Run(opts); rc != 2 {
			t.Fatalf("Run() = %d, want 2 (stderr: %s)", rc, stderr.String())
		}
		wantPid := fmt.Sprintf("(pid %d)", os.Getpid())
		if !strings.Contains(stderr.String(), wantPid) {
			t.Errorf("stderr = %q, want to contain %q", stderr.String(), wantPid)
		}
		if _, err := os.Stat(leftover); err != nil {
			t.Errorf("leftover = %v, want it to still exist (clearWork must never run while the lock is held)", err)
		}
		if client.getCalls != 0 || client.getRepoCalls != 0 || client.currentCalls != 0 {
			t.Errorf("client calls = get:%d getRepo:%d current:%d, want all 0", client.getCalls, client.getRepoCalls, client.currentCalls)
		}
		if len(sb.createCalls) != 0 {
			t.Errorf("sandbox created %d time(s), want 0", len(sb.createCalls))
		}
	})
}

func TestPRLockReleasedAndReacquirable(t *testing.T) {
	homeDir := t.TempDir()

	l, err := acquirePRLock(homeDir)
	if err != nil {
		t.Fatalf("acquirePRLock: %v", err)
	}
	if err := l.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}

	l2, err := acquirePRLock(homeDir)
	if err != nil {
		t.Fatalf("acquirePRLock after release: %v", err)
	}
	defer l2.Release()
}
