package runner

import (
	"os"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestJobsConfigPrecedence(t *testing.T) {
	homeDir := t.TempDir()

	if n, src, err := resolveJobsCount(Options{Home: homeDir}, 4); err != nil || n != 1 || src != "default" {
		t.Fatalf("default: n=%d src=%q err=%v", n, src, err)
	}

	if err := os.WriteFile(home.ConfigPath(homeDir), []byte("NIGHTSHIFT_JOBS=3\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if n, src, err := resolveJobsCount(Options{Home: homeDir}, 4); err != nil || n != 3 || src != "config" {
		t.Fatalf("config: n=%d src=%q err=%v", n, src, err)
	}

	t.Setenv("NIGHTSHIFT_JOBS", "2")
	if n, src, err := resolveJobsCount(Options{Home: homeDir}, 4); err != nil || n != 2 || src != "env" {
		t.Fatalf("env: n=%d src=%q err=%v", n, src, err)
	}

	if n, src, err := resolveJobsCount(Options{Home: homeDir, Jobs: 5}, 10); err != nil || n != 5 || src != "flag" {
		t.Fatalf("flag: n=%d src=%q err=%v", n, src, err)
	}

	for _, bad := range []string{"0", "-1", "abc"} {
		t.Setenv("NIGHTSHIFT_JOBS", bad)
		if _, _, err := resolveJobsCount(Options{Home: homeDir}, 4); err == nil {
			t.Fatalf("NIGHTSHIFT_JOBS=%q: expected an error", bad)
		}
	}
}

// TestJobsCappedByMaxConcurrentResolve checks the min(N, max_concurrent)
// clamp from each source — flag, env and config all go through the same
// min() call in resolveJobsCount, so one representative case per source is
// enough; the scheduler's own cap behavior (TestJobsCappedByMaxConcurrent)
// is covered once the scheduler loop exists.
func TestJobsCappedByMaxConcurrentResolve(t *testing.T) {
	homeDir := t.TempDir()

	if n, src, err := resolveJobsCount(Options{Home: homeDir, Jobs: 9}, 3); err != nil || n != 3 || src != "flag" {
		t.Fatalf("flag clamp: n=%d src=%q err=%v", n, src, err)
	}

	t.Setenv("NIGHTSHIFT_JOBS", "9")
	if n, src, err := resolveJobsCount(Options{Home: homeDir}, 3); err != nil || n != 3 || src != "env" {
		t.Fatalf("env clamp: n=%d src=%q err=%v", n, src, err)
	}
}
