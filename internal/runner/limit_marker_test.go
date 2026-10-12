package runner

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

// TestLimitMarkerBlocksStart: an active marker whose wait cannot fit the
// default window blocks run <spec> entirely — no sandbox, no result note,
// run.json left exactly as it was, and both locks released.
func TestLimitMarkerBlocksStart(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(10 * time.Hour) // wake is far beyond the default 6h MaxWait
	if err := writeLimitMarker(stateRoot, limitHit{ResetAt: resetAt}, base, "other-spec", time.Hour); err != nil {
		t.Fatalf("writeLimitMarker: %v", err)
	}

	// Seed a prior run.json so "keeps its previous status" is actually
	// exercised, not vacuously true because no state dir exists yet.
	stateDir := filepath.Join(stateRoot, "demo")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	priorJSON := []byte(`{"slug":"demo","status":"capped"}` + "\n")
	if err := os.WriteFile(filepath.Join(stateDir, "run.json"), priorJSON, 0o644); err != nil {
		t.Fatalf("seed run.json: %v", err)
	}

	sb := newStubSandbox()
	var stdout, stderr bytes.Buffer
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader(""),
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatalf("unexpected sleep %s — the limit cannot be waited out", d)
			return nil
		},
	}
	opts.clock = func() time.Time { return base }

	status, err := Run(opts)
	if err != nil || status != StatusRateLimited {
		t.Fatalf("status=%v err=%v, want RateLimited/nil", status, err)
	}
	if got := ExitCode(status); got != 9 {
		t.Fatalf("ExitCode = %d, want 9", got)
	}
	if sb.mountDir != "" {
		t.Fatalf("sandbox Create was called: mountDir=%q", sb.mountDir)
	}
	wantMsg := "usage limit active until " + resetAt.Local().Format("Mon 15:04") + " (recorded by other-spec); not starting"
	if !strings.Contains(stderr.String(), wantMsg) {
		t.Fatalf("stderr = %q, want to contain %q", stderr.String(), wantMsg)
	}

	if entries, err := os.ReadDir(home.ResultsDir(homeDir)); err == nil && len(entries) != 0 {
		t.Fatalf("expected no result note, got %v", entries)
	}

	gotJSON, err := os.ReadFile(filepath.Join(stateDir, "run.json"))
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	if !bytes.Equal(gotJSON, priorJSON) {
		t.Fatalf("run.json changed: got %s, want unchanged %s", gotJSON, priorJSON)
	}

	if _, held := lock.Probe(specLockPath(stateRoot, "demo")); held {
		t.Fatalf("spec lock still held after a blocked start")
	}
	if _, held := lock.Probe(slotLockPath(stateRoot, 1)); held {
		t.Fatalf("slot lock still held after a blocked start")
	}
}

// TestLimitMarkerWaitsBeforeStart: an active marker whose wait fits the
// window makes run <spec> sleep first, then proceed — the sandbox is only
// created after the sleep.
func TestLimitMarkerWaitsBeforeStart(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(10 * time.Minute)
	if err := writeLimitMarker(stateRoot, limitHit{ResetAt: resetAt}, base, "other-spec", time.Hour); err != nil {
		t.Fatalf("writeLimitMarker: %v", err)
	}

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	var slept []time.Duration
	createdBeforeSleep := false
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Sleep: func(ctx context.Context, d time.Duration) error {
			if sb.mountDir != "" {
				createdBeforeSleep = true
			}
			slept = append(slept, d)
			return nil
		},
	}
	opts.clock = func() time.Time { return base }

	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}
	if createdBeforeSleep {
		t.Fatalf("sandbox was created before the usage-limit wait")
	}
	if len(slept) == 0 || slept[0] != 12*time.Minute {
		t.Fatalf("slept = %v, want first wait = 12m (resetAt+2m)", slept)
	}
	if sb.mountDir == "" {
		t.Fatalf("sandbox Create was never called after the wait")
	}
}

// TestLimitMarkerExpiredRemoved: a marker whose reset_at has already
// passed is deleted and ignored — run <spec> proceeds without waiting,
// and the stale file is gone afterward.
func TestLimitMarkerExpiredRemoved(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	expired := base.Add(-time.Hour)
	if err := writeLimitMarker(stateRoot, limitHit{ResetAt: expired}, expired.Add(-time.Minute), "stale-spec", time.Hour); err != nil {
		t.Fatalf("writeLimitMarker: %v", err)
	}

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Sleep: func(ctx context.Context, d time.Duration) error {
			t.Fatalf("unexpected sleep %s — the marker is expired", d)
			return nil
		},
	}
	opts.clock = func() time.Time { return base }

	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}
	if sb.mountDir == "" {
		t.Fatalf("sandbox Create was never called")
	}
	if _, err := os.Stat(limitMarkerPath(stateRoot)); !os.IsNotExist(err) {
		t.Fatalf("expired marker was not removed: err=%v", err)
	}
}

// TestDryRunShowsUsageLimit: --dry-run reports an active marker's reset
// time (or "none") and never deletes it, even an expired one.
func TestDryRunShowsUsageLimit(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	base := time.Date(2026, 1, 5, 10, 0, 0, 0, time.UTC)
	resetAt := base.Add(2 * time.Hour)
	if err := writeLimitMarker(stateRoot, limitHit{ResetAt: resetAt}, base, "demo", time.Hour); err != nil {
		t.Fatalf("writeLimitMarker: %v", err)
	}

	var stdout, stderr bytes.Buffer
	opts := Options{SpecArg: "demo", Home: homeDir, DryRun: true, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	opts.clock = func() time.Time { return base }

	status, err := Run(opts)
	if err != nil || status != statusDryRun {
		t.Fatalf("status=%v err=%v, want dry-run/nil", status, err)
	}

	want := "usage limit: active until " + resetAt.Local().Format("Mon 15:04")
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("dry-run output missing %q:\n%s", want, stdout.String())
	}

	if _, err := os.Stat(limitMarkerPath(stateRoot)); err != nil {
		t.Fatalf("dry-run deleted the marker: %v", err)
	}
}

// TestDryRunShowsNoUsageLimit: with no marker at all, --dry-run reports
// "none".
func TestDryRunShowsNoUsageLimit(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	var stdout, stderr bytes.Buffer
	opts := Options{SpecArg: "demo", Home: homeDir, DryRun: true, Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	status, err := Run(opts)
	if err != nil || status != statusDryRun {
		t.Fatalf("status=%v err=%v, want dry-run/nil", status, err)
	}
	if !strings.Contains(stdout.String(), "usage limit: none") {
		t.Fatalf("dry-run output missing %q:\n%s", "usage limit: none", stdout.String())
	}
}
