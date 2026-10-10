package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeNotifySendScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_NOTIFY_LOG"
exit 0
`

// newFakeNotifySend puts a fake notify-send ahead of the real PATH, so
// the real Desktop notifier's exec.LookPath finds it.
func newFakeNotifySend(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "notify-send")
	if err := os.WriteFile(bin, []byte(fakeNotifySendScript), 0o755); err != nil {
		t.Fatalf("write fake notify-send: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestNotifyArgvAndUrgency(t *testing.T) {
	newFakeNotifySend(t)
	logPath := filepath.Join(t.TempDir(), "notify.log")
	t.Setenv("FAKE_NOTIFY_LOG", logPath)
	t.Setenv("NIGHTSHIFT_NOTIFY", "on")

	t.Run("red is critical", func(t *testing.T) {
		repo := newSourceRepo(t)
		t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:01s +0000")
		t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:01s +0000")
		os.Remove(logPath)
		sb := newStubSandbox()
		sb.agent = func(ctx context.Context, iter int, dir string) error {
			if iter == 2 || iter == 3 {
				commitFile(t, dir, "work.go", "bad\n", "bad")
			}
			return nil
		}
		sb.verify = func(iter int, dir string) error {
			if iter == 2 || iter == 3 {
				return errMockVerifyFail
			}
			return nil
		}
		r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb) // Notifier left nil: real resolution
		status := r.loop()
		if status != StatusRed {
			t.Fatalf("status = %v, want Red", status)
		}
		if _, err := r.finish(status); err != nil {
			t.Fatalf("finish: %v", err)
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read notify log: %v", err)
		}
		argv := strings.TrimSpace(string(data))
		if !strings.Contains(argv, "--app-name=nightshift") {
			t.Fatalf("argv missing --app-name=nightshift: %q", argv)
		}
		if !strings.Contains(argv, "--urgency=critical") {
			t.Fatalf("argv missing --urgency=critical: %q", argv)
		}
		if !strings.Contains(argv, "Spec demo: red") {
			t.Fatalf("argv missing the title: %q", argv)
		}
	})

	t.Run("complete is normal", func(t *testing.T) {
		repo := newSourceRepo(t)
		t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:02s +0000")
		t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:02s +0000")
		os.Remove(logPath)
		sb := newStubSandbox()
		sb.agent = completingAgent(t)
		sb.verify = alwaysGreenVerify
		r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)
		status := r.loop()
		if status != StatusComplete {
			t.Fatalf("status = %v, want Complete", status)
		}
		if _, err := r.finish(status); err != nil {
			t.Fatalf("finish: %v", err)
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read notify log: %v", err)
		}
		argv := strings.TrimSpace(string(data))
		if !strings.Contains(argv, "--urgency=normal") {
			t.Fatalf("argv missing --urgency=normal: %q", argv)
		}
	})

	t.Run("title has sync pending when applicable", func(t *testing.T) {
		repo := newSourceRepo(t)
		t.Setenv("GIT_AUTHOR_DATE", "2026-01-01T00:00:03s +0000")
		t.Setenv("GIT_COMMITTER_DATE", "2026-01-01T00:00:03s +0000")
		os.Remove(logPath)
		sb := newStubSandbox()
		sb.agent = completingAgent(t)
		sb.verify = alwaysGreenVerify
		r := newTestRun(t, repo, "", Options{Stdout: io.Discard}, sb)
		status := r.loop()
		if status != StatusComplete {
			t.Fatalf("status = %v, want Complete", status)
		}
		r.st.SyncRef = r.pendingSyncRef() // simulate the branch checked out elsewhere
		if _, err := r.finish(status); err != nil {
			t.Fatalf("finish: %v", err)
		}

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("read notify log: %v", err)
		}
		if !strings.Contains(string(data), "(sync pending)") {
			t.Fatalf("argv missing (sync pending): %q", data)
		}
	})
}

func TestNotifyOffAndMissing(t *testing.T) {
	homeDir := t.TempDir()

	t.Run("off never execs", func(t *testing.T) {
		newFakeNotifySend(t)
		logPath := filepath.Join(t.TempDir(), "log")
		t.Setenv("FAKE_NOTIFY_LOG", logPath)
		t.Setenv("NIGHTSHIFT_NOTIFY", "off")

		n, err := resolveNotifier(Options{Home: homeDir})
		if err != nil {
			t.Fatalf("resolveNotifier: %v", err)
		}
		n.Notify("t", "m", true)
		if _, err := os.Stat(logPath); !os.IsNotExist(err) {
			t.Fatalf("notify-send ran despite NIGHTSHIFT_NOTIFY=off")
		}
	})

	t.Run("missing binary is silent", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("NIGHTSHIFT_NOTIFY", "on")
		n, err := resolveNotifier(Options{Home: homeDir})
		if err != nil {
			t.Fatalf("resolveNotifier: %v", err)
		}
		n.Notify("t", "m", false) // must not panic or error
	})

	t.Run("invalid value is a config error", func(t *testing.T) {
		t.Setenv("NIGHTSHIFT_NOTIFY", "maybe")
		_, err := resolveNotifier(Options{Home: homeDir})
		want := `NIGHTSHIFT_NOTIFY="maybe": want on or off`
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})
}
