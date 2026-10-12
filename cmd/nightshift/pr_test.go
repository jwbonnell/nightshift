package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/review"
)

// assertDirEmpty confirms a no-forge-configured failure left dir untouched:
// resolveHome never creates anything, so the directory must still be empty.
func assertDirEmpty(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	if len(entries) != 0 {
		t.Fatalf("dir %s not empty: %v", dir, entries)
	}
}

// TestCLIPR covers nightshift pr's own usage/flag-parsing errors (SPEC.md
// P4): no args, -h, --forge with --pending, and --list with --dry-run.
// The top-level usage string gaining both "pr" lines (D4) is also
// asserted here since it's the same CLI surface.
func TestCLIPR(t *testing.T) {
	t.Run("no args is a usage error", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)
		t.Setenv("NIGHTSHIFT_GITHUB_TOKEN", "tok")

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 2 {
			t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
		}
		if !strings.Contains(stderr.String(), "usage: nightshift pr <pr-or-mr-url>") {
			t.Fatalf("stderr missing usage block: %q", stderr.String())
		}
	})

	t.Run("-h exits 0 and prints usage to stderr", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr", "-h"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 0 {
			t.Fatalf("exit=%d, want 0; stderr=%s", exit, stderr.String())
		}
		if !strings.Contains(stderr.String(), "usage: nightshift pr <pr-or-mr-url>") {
			t.Fatalf("stderr missing usage block: %q", stderr.String())
		}
		if stdout.String() != "" {
			t.Fatalf("stdout = %q, want empty (usage goes to stderr)", stdout.String())
		}
	})

	t.Run("--forge with --pending is a usage error", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)
		t.Setenv("NIGHTSHIFT_GITHUB_TOKEN", "tok")

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr", "--pending", "--forge", "github"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 2 {
			t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
		}
		if !strings.Contains(stderr.String(), "nightshift pr: --forge only applies to <repo> <n>") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("--list --dry-run is a usage error", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)
		t.Setenv("NIGHTSHIFT_GITHUB_TOKEN", "tok")

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr", "--pending", "--list", "--dry-run"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 2 {
			t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
		}
		if !strings.Contains(stderr.String(), "--list or --dry-run") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("top-level usage mentions both pr lines", func(t *testing.T) {
		if !strings.Contains(usage, "nightshift pr <url>|<repo> <n> [flags]") {
			t.Fatalf("usage missing the explicit-target pr line:\n%s", usage)
		}
		if !strings.Contains(usage, "nightshift pr --pending [flags]") {
			t.Fatalf("usage missing the --pending pr line:\n%s", usage)
		}
	})
}

// TestNoForgeConfigured asserts that with no forge configured, both
// --pending and an explicit target fail with the exact P4 message before
// review.Run is ever reached, and leave the home directory untouched.
func TestNoForgeConfigured(t *testing.T) {
	const wantLine = "nightshift pr: no forge configured — set NIGHTSHIFT_GITLAB_URL + NIGHTSHIFT_GITLAB_TOKEN_FILE and/or NIGHTSHIFT_GITHUB_TOKEN_FILE"

	orig := prRunFn
	prRunFn = func(review.Options) int {
		t.Fatal("prRunFn called despite no forge configured")
		return 0
	}
	t.Cleanup(func() { prRunFn = orig })

	t.Run("--pending", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr", "--pending"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 2 {
			t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
		}
		if got := stderr.String(); !strings.Contains(got, wantLine) {
			t.Fatalf("stderr = %q, want to contain %q", got, wantLine)
		}
		assertDirEmpty(t, homeDir)
	})

	t.Run("explicit target", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr", "o/r#1"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 2 {
			t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
		}
		if got := stderr.String(); !strings.Contains(got, wantLine) {
			t.Fatalf("stderr = %q, want to contain %q", got, wantLine)
		}
		assertDirEmpty(t, homeDir)
	})
}

// TestPRSelectorErrors asserts that a bad NIGHTSHIFT_PR_MODEL fails
// dispatch before review.Run is ever reached: exit 2, the exact P4 prefix,
// no lock file, no forge API call, and nothing new under the home.
func TestPRSelectorErrors(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	homeDir := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", homeDir)
	t.Setenv("NIGHTSHIFT_GITHUB_TOKEN", "tok")
	t.Setenv("NIGHTSHIFT_GITHUB_URL", server.URL)
	t.Setenv("NIGHTSHIFT_PR_MODEL", "bogus/thing/here/extra")

	orig := prRunFn
	prRunFn = func(review.Options) int {
		t.Fatal("prRunFn called despite a bad selector")
		return 0
	}
	t.Cleanup(func() { prRunFn = orig })

	var stdout, stderr bytes.Buffer
	exit := dispatch([]string{"pr", "o/r#1"}, &stdout, &stderr, strings.NewReader(""))
	if exit != 2 {
		t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
	}
	wantPrefix := "nightshift pr: NIGHTSHIFT_PR_MODEL (from env): "
	if got := stderr.String(); !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("stderr = %q, want prefix %q", got, wantPrefix)
	}
	if hits != 0 {
		t.Errorf("server hits = %d, want 0", hits)
	}
	if _, err := os.Stat(filepath.Join(home.PRDir(homeDir), ".lock")); !os.IsNotExist(err) {
		t.Errorf("lock file exists: %v", err)
	}
	assertDirEmpty(t, homeDir)
}
