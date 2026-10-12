package review

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const fakeSbxScript = `#!/bin/sh
if [ -n "$FAKE_SBX_STDIN_FILE" ]; then
  cat >> "$FAKE_SBX_STDIN_FILE"
fi
if [ -n "$FAKE_SBX_LOG" ]; then
  printf '%s\n' "$*" >> "$FAKE_SBX_LOG"
fi
if [ -n "$FAKE_SBX_SLEEP" ]; then
  exec sleep "$FAKE_SBX_SLEEP"
fi
if [ -n "$FAKE_SBX_STDOUT" ]; then
  printf '%s' "$FAKE_SBX_STDOUT"
fi
if [ -n "$FAKE_SBX_STDERR" ]; then
  printf '%s' "$FAKE_SBX_STDERR" >&2
fi
exit "${FAKE_SBX_EXIT:-0}"
`

// newFakeSbx writes the fake sbx script to a fresh temp dir and returns
// its absolute path. Tests configure its behavior via the FAKE_SBX_* env
// vars (t.Setenv) and read its recorded argv via logPath.
func newFakeSbx(t *testing.T) (bin, logPath string) {
	t.Helper()
	dir := t.TempDir()
	bin = filepath.Join(dir, "sbx")
	if err := os.WriteFile(bin, []byte(fakeSbxScript), 0o755); err != nil {
		t.Fatalf("write fake sbx: %v", err)
	}
	logPath = filepath.Join(dir, "log")
	t.Setenv("FAKE_SBX_LOG", logPath)
	return bin, logPath
}

func readLog(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read log: %v", err)
	}
	s := strings.TrimRight(string(data), "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func equalLines(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestSbxSandboxArgv(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	sb := newSbxSandbox(bin, "run123")

	if err := sb.Create(context.Background(), "/mnt/pr/work", "claude", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantCreate := []string{"create --name nightshift-pr-run123 claude /mnt/pr/work"}
	if got := readLog(t, logPath); !equalLines(got, wantCreate) {
		t.Fatalf("Create argv = %q, want %q", got, wantCreate)
	}
	os.Remove(logPath)

	if err := sb.AllowNetwork(context.Background(), "opencode.ai", nil); err != nil {
		t.Fatalf("AllowNetwork: %v", err)
	}
	wantAllow := []string{"policy allow network --sandbox nightshift-pr-run123 opencode.ai"}
	if got := readLog(t, logPath); !equalLines(got, wantAllow) {
		t.Fatalf("AllowNetwork argv = %q, want %q", got, wantAllow)
	}
	os.Remove(logPath)

	stdinFile := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("FAKE_SBX_STDIN_FILE", stdinFile)
	t.Setenv("FAKE_SBX_STDOUT", "Sandbox nightshift-pr-run123 started successfully\nVERDICT: LGTM\n")
	var stderr bytes.Buffer
	out, err := sb.Exec(context.Background(), "/mnt/pr/work/slug/tree", []string{"claude", "-p"}, "the prompt", nil, &stderr)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if out != "VERDICT: LGTM\n" {
		t.Fatalf("Exec stdout = %q, want banner stripped", out)
	}
	wantExec := []string{"exec -w /mnt/pr/work/slug/tree nightshift-pr-run123 claude -p"}
	if got := readLog(t, logPath); !equalLines(got, wantExec) {
		t.Fatalf("Exec argv = %q, want %q", got, wantExec)
	}
	gotStdin, rerr := os.ReadFile(stdinFile)
	if rerr != nil {
		t.Fatalf("read stdin capture: %v", rerr)
	}
	if string(gotStdin) != "the prompt" {
		t.Fatalf("stdin = %q, want %q", gotStdin, "the prompt")
	}
	t.Setenv("FAKE_SBX_STDIN_FILE", "")
	t.Setenv("FAKE_SBX_STDOUT", "")
	os.Remove(logPath)

	if err := sb.Stop(nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	wantStop := []string{"rm -f nightshift-pr-run123"}
	if got := readLog(t, logPath); !equalLines(got, wantStop) {
		t.Fatalf("Stop argv = %q, want %q", got, wantStop)
	}
	os.Remove(logPath)

	// Stop already reset created to false, so a subsequent Remove must
	// be a no-op — mirrors a run that stopped on timeout and then still
	// runs its deferred Remove.
	if err := sb.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := readLog(t, logPath); got != nil {
		t.Fatalf("Remove ran sbx after Stop already cleared created: %q", got)
	}
}

func TestSbxSandboxRemoveAfterCreate(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	sb := newSbxSandbox(bin, "run123")

	if err := sb.Create(context.Background(), "/mnt/pr/work", "claude", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	os.Remove(logPath)

	if err := sb.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	want := []string{"rm -f nightshift-pr-run123"}
	if got := readLog(t, logPath); !equalLines(got, want) {
		t.Fatalf("Remove argv = %q, want %q", got, want)
	}
}

func TestSbxSandboxStopRemoveNoopWhenNeverCreated(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	sb := newSbxSandbox(bin, "run123")

	if err := sb.Stop(nil); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := sb.Remove(nil); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := readLog(t, logPath); got != nil {
		t.Fatalf("Stop/Remove ran sbx when nothing was ever created: %q", got)
	}
}

func TestSbxSandboxAvailable(t *testing.T) {
	bin, _ := newFakeSbx(t)
	sb := newSbxSandbox(bin, "run123")
	if !sb.Available() {
		t.Fatal("Available() = false for a real executable")
	}

	sb2 := newSbxSandbox(filepath.Join(t.TempDir(), "does-not-exist"), "run123")
	if sb2.Available() {
		t.Fatal("Available() = true for a nonexistent binary")
	}
}

func TestSbxSandboxExecDeadlineExceeded(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_SLEEP", "5")

	sb := newSbxSandbox(bin, "run123")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := sb.Exec(ctx, "/mnt/pr/work/slug/tree", []string{"claude"}, "", nil, io.Discard)
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("Exec took %s, want it bounded well under the fake sleep", elapsed)
	}
}

func TestSbxSandboxCreateFailureIncludesOutput(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_EXIT", "1")
	t.Setenv("FAKE_SBX_STDERR", "  boom  \n")

	sb := newSbxSandbox(bin, "run123")
	err := sb.Create(context.Background(), "/mnt/pr/work", "claude", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to mention stderr content", err)
	}
}

func TestExecCombinedTimeout(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_SLEEP", "5")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := execCombined(ctx, nil, nil, bin, "slow")
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("execCombined took %s, want it bounded by WaitDelay, not the fake sleep", elapsed)
	}
}

func TestExecWaitDelay(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_SLEEP", "5")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	var stdout, stderr bytes.Buffer
	start := time.Now()
	err := execSplit(ctx, nil, &stdout, &stderr, nil, bin, "slow")
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("execSplit took %s, want it bounded by WaitDelay, not the fake sleep", elapsed)
	}
}

func TestStripSbxBanner(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no banner", "abc123\n", "abc123\n"},
		{"single banner line", "Sandbox demo started successfully.\nabc123", "abc123"},
		{"created banner", "Sandbox demo created\nabc123", "abc123"},
		{"starting banner, extra whitespace", "  Sandbox demo starting.  \nabc123", "abc123"},
		{"multiple banner lines", "Sandbox demo created\nSandbox demo started successfully.\nabc123", "abc123"},
		{"only a banner, no trailing content", "Sandbox demo started successfully.", ""},
		{"non-banner first line is untouched", "abc123\nSandbox demo created", "abc123\nSandbox demo created"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripSbxBanner(c.in); got != c.want {
				t.Fatalf("stripSbxBanner(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestScrubEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=foo",
		"NIGHTSHIFT_GITLAB_TOKEN=glsecret",
		"NIGHTSHIFT_GITLAB_URL=https://gitlab.example.com",
		"NIGHTSHIFT_GITHUB_TOKEN=ghsecret",
		"NIGHTSHIFT_GITHUB_URL=https://github.com",
		"NIGHTSHIFT_PR_MODEL=sonnet",
		"SOME_OTHER_VAR=contains glsecret in its value",
		"ANOTHER_VAR=contains ghsecret too",
		"HOME=/home/agent",
	}
	got := scrubEnv(in, "glsecret", "ghsecret")

	want := []string{
		"PATH=/usr/bin",
		"NIGHTSHIFT_PR_MODEL=sonnet",
		"HOME=/home/agent",
	}
	if !equalLines(got, want) {
		t.Fatalf("scrubEnv = %q, want %q", got, want)
	}
}

func TestScrubEnvEmptyTokensDontMatchEverything(t *testing.T) {
	in := []string{"FOO=bar", "BAZ="}
	got := scrubEnv(in, "", "")
	if !equalLines(got, in) {
		t.Fatalf("scrubEnv with empty tokens = %q, want unchanged %q", got, in)
	}
}
