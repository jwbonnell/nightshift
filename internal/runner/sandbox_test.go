package runner

import (
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
  cat > "$FAKE_SBX_STDIN_FILE"
fi
if [ -n "$FAKE_SBX_LOG" ]; then
  printf '%s\n' "$*" >> "$FAKE_SBX_LOG"
fi
if [ -n "$FAKE_SBX_SLEEP" ]; then
  # exec replaces this shell with sleep, so a context-cancel SIGKILL from
  # the Go side kills it directly instead of leaving it as an orphaned
  # grandchild still holding the output pipe open.
  exec sleep "$FAKE_SBX_SLEEP"
fi
if [ -n "$FAKE_SBX_STDOUT" ]; then
  printf '%s' "$FAKE_SBX_STDOUT"
fi
if [ -n "$FAKE_SBX_STDERR" ]; then
  printf '%s' "$FAKE_SBX_STDERR" >&2
fi
exit_code="${FAKE_SBX_EXIT:-0}"
if [ -n "$FAKE_SBX_FAIL_SUBSTR" ]; then
  case "$*" in
    *"$FAKE_SBX_FAIL_SUBSTR"*) exit_code=1 ;;
  esac
fi
exit "$exit_code"
`

// newFakeSbx writes the fake sbx script to a fresh temp dir and returns
// its absolute path. Tests configure its behavior via the FAKE_SBX_* env
// vars (t.Setenv), and read its recorded argv via logPath.
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

func TestSbxArgv(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"
	sb := newSbxSandbox(bin, "claude", "demo")

	if err := sb.Create(mountDir, []string{"api.example.com", "cdn.example.com"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := []string{
		"create --name nightshift-demo claude " + mountDir,
		"policy allow network --sandbox nightshift-demo api.example.com",
		"policy allow network --sandbox nightshift-demo cdn.example.com",
		"create --name nightshift-demo-ctl claude " + mountDir,
		"policy allow network --sandbox nightshift-demo-ctl api.example.com",
		"policy allow network --sandbox nightshift-demo-ctl cdn.example.com",
	}
	got := readLog(t, logPath)
	if !equalLines(got, want) {
		t.Fatalf("Create argv:\ngot:  %q\nwant: %q", got, want)
	}
	os.Remove(logPath)

	if err := sb.StopAgent(); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	wantStop := []string{"stop nightshift-demo"}
	if got := readLog(t, logPath); !equalLines(got, wantStop) {
		t.Fatalf("StopAgent argv = %q, want %q", got, wantStop)
	}
	os.Remove(logPath)

	if _, err := sb.Git(context.Background(), "status", "--porcelain"); err != nil {
		t.Fatalf("Git: %v", err)
	}
	wantGit := []string{"exec -w " + mountDir + " nightshift-demo-ctl git -c core.fsmonitor= -c core.hooksPath=/dev/null status --porcelain"}
	if got := readLog(t, logPath); !equalLines(got, wantGit) {
		t.Fatalf("Git argv = %q, want %q", got, wantGit)
	}
	os.Remove(logPath)

	if err := sb.ResetVerify(); err != nil {
		t.Fatalf("ResetVerify: %v", err)
	}
	wantReset := []string{
		"rm -f nightshift-demo-verify",
		"create --name nightshift-demo-verify claude " + mountDir,
		"policy allow network --sandbox nightshift-demo-verify api.example.com",
		"policy allow network --sandbox nightshift-demo-verify cdn.example.com",
	}
	if got := readLog(t, logPath); !equalLines(got, wantReset) {
		t.Fatalf("ResetVerify argv = %q, want %q", got, wantReset)
	}
	os.Remove(logPath)

	if _, err := sb.RunVerify(context.Background(), "echo hi"); err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	wantVerify := []string{"exec -w " + mountDir + " nightshift-demo-verify sh -c echo hi"}
	if got := readLog(t, logPath); !equalLines(got, wantVerify) {
		t.Fatalf("RunVerify argv = %q, want %q", got, wantVerify)
	}
	os.Remove(logPath)

	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	wantRemove := []string{"rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify"}
	if got := readLog(t, logPath); !equalLines(got, wantRemove) {
		t.Fatalf("Remove argv = %q, want %q", got, wantRemove)
	}
}

func TestRunAgentArgvAndStdin(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"
	sb := newSbxSandbox(bin, "claude", "demo")
	sb.mountDir = mountDir

	stdinFile := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("FAKE_SBX_STDIN_FILE", stdinFile)

	var logw strings.Builder
	err := sb.RunAgent(context.Background(), "sonnet", "the prompt text", &logw)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	wantArgv := []string{"exec -w " + mountDir + " nightshift-demo claude -p --model sonnet --output-format json --permission-mode bypassPermissions"}
	if got := readLog(t, logPath); !equalLines(got, wantArgv) {
		t.Fatalf("RunAgent argv = %q, want %q", got, wantArgv)
	}

	gotStdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	if string(gotStdin) != "the prompt text" {
		t.Fatalf("stdin = %q, want %q", gotStdin, "the prompt text")
	}
}

func TestCreateCtlFailureRemovesAgent(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	t.Setenv("FAKE_SBX_FAIL_SUBSTR", "nightshift-demo-ctl")

	sb := newSbxSandbox(bin, "claude", "demo")
	err := sb.Create("/mnt/demo/repo", nil)
	if err == nil {
		t.Fatalf("expected Create to fail when the ctl sandbox create fails")
	}

	got := readLog(t, logPath)
	want := []string{
		"create --name nightshift-demo claude /mnt/demo/repo",
		"create --name nightshift-demo-ctl claude /mnt/demo/repo",
		"rm -f nightshift-demo",
	}
	if !equalLines(got, want) {
		t.Fatalf("argv = %q, want %q (D11.8: agent sandbox must be cleaned up)", got, want)
	}
}

func TestGitTrimsOnlyTrailingWhitespace(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_STDOUT", "  leading space kept, trailing gone  \n\t")

	sb := newSbxSandbox(bin, "claude", "demo")
	sb.mountDir = "/mnt/demo/repo"

	got, err := sb.Git(context.Background(), "diff", "--stat")
	if err != nil {
		t.Fatalf("Git: %v", err)
	}
	want := "  leading space kept, trailing gone"
	if got != want {
		t.Fatalf("Git output = %q, want %q", got, want)
	}
}

func TestGitErrorIncludesStderr(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_EXIT", "1")
	t.Setenv("FAKE_SBX_STDERR", "  boom  \n")

	sb := newSbxSandbox(bin, "claude", "demo")
	sb.mountDir = "/mnt/demo/repo"

	_, err := sb.Git(context.Background(), "status")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to mention stderr content", err)
	}
}

func TestRemoveNoopWhenNeverCreated(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	sb := newSbxSandbox(bin, "claude", "demo")

	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := readLog(t, logPath); got != nil {
		t.Fatalf("Remove ran sbx when nothing was ever created: %q", got)
	}
}

func TestAvailable(t *testing.T) {
	bin, _ := newFakeSbx(t)
	sb := newSbxSandbox(bin, "claude", "demo")
	if !sb.Available() {
		t.Fatalf("Available() = false for a real executable")
	}

	sb2 := newSbxSandbox(filepath.Join(t.TempDir(), "does-not-exist"), "claude", "demo")
	if sb2.Available() {
		t.Fatalf("Available() = true for a nonexistent binary")
	}
}

func TestExecStreamDeadlineExceeded(t *testing.T) {
	bin, _ := newFakeSbx(t)
	t.Setenv("FAKE_SBX_SLEEP", "5")

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := execStream(ctx, nil, io.Discard, bin, "slow")
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
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
