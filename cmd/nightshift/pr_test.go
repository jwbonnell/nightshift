package main

import (
	"bytes"
	"strings"
	"testing"
)

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

	t.Run("no forge configured is a usage error", func(t *testing.T) {
		homeDir := t.TempDir()
		t.Setenv("NIGHTSHIFT_HOME", homeDir)

		var stdout, stderr bytes.Buffer
		exit := dispatch([]string{"pr", "--pending"}, &stdout, &stderr, strings.NewReader(""))
		if exit != 2 {
			t.Fatalf("exit=%d, want 2; stderr=%s", exit, stderr.String())
		}
		if !strings.Contains(stderr.String(), "no forge configured") {
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
