package review

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jwbonnell/nightshift/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.HermeticGit()
	os.Exit(runWithNotifyGuard(m))
}

// runWithNotifyGuard puts a no-op notify-send first on PATH for the whole
// package, so a test that leaves a Notifier nil can never pop a real
// desktop notification on the developer's machine — same guard as
// internal/runner's.
func runWithNotifyGuard(m *testing.M) int {
	dir, err := os.MkdirTemp("", "nightshift-review-notify-guard-")
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
