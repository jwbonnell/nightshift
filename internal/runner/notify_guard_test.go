package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// runWithNotifyGuard (called from TestMain) puts a no-op notify-send first
// on PATH for the whole package, so a test that leaves Options.Notifier nil
// can never pop a real desktop notification on the developer's machine.
// Tests that exercise real notifier resolution (notify_test.go) prepend
// their own recording fake with t.Setenv, which still takes precedence.
func runWithNotifyGuard(m *testing.M) int {
	dir, err := os.MkdirTemp("", "nightshift-notify-guard-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "notify guard: %v\n", err)
		return 1
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "notify-send"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "notify guard: %v\n", err)
		return 1
	}
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return m.Run()
}
