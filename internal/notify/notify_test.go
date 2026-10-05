package notify

import (
	"os"
	"path/filepath"
	"testing"
)

const fakeNotifySendScript = `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_NOTIFY_LOG"
exit 0
`

func newFakeNotifySend(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "notify-send")
	if err := os.WriteFile(bin, []byte(fakeNotifySendScript), 0o755); err != nil {
		t.Fatalf("write fake notify-send: %v", err)
	}
	logPath = filepath.Join(dir, "log")
	t.Setenv("FAKE_NOTIFY_LOG", logPath)
	return bin, logPath
}

func TestDesktopArgvAndUrgency(t *testing.T) {
	bin, logPath := newFakeNotifySend(t)

	Desktop{Bin: bin}.Notify("Spec demo: red", "something failed", true)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	want := "--app-name=nightshift --urgency=critical Spec demo: red something failed\n"
	if string(data) != want {
		t.Fatalf("argv = %q, want %q", data, want)
	}

	os.Remove(logPath)
	Desktop{Bin: bin}.Notify("Spec demo: complete", "all good", false)
	data, err = os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	want = "--app-name=nightshift --urgency=normal Spec demo: complete all good\n"
	if string(data) != want {
		t.Fatalf("argv = %q, want %q", data, want)
	}
}

func TestDesktopMissingBinaryIsSilent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	// Must not panic or error; there is nothing observable to assert
	// beyond "this returns".
	Desktop{Bin: missing}.Notify("title", "msg", false)
}

func TestNoopSendsNothing(t *testing.T) {
	bin, logPath := newFakeNotifySend(t)
	_ = bin // Noop never even looks at a binary.
	Noop{}.Notify("title", "msg", true)
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("Noop executed something: log file exists")
	}
}
