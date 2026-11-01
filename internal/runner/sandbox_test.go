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

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/home"
)

const fakeSbxScript = `#!/bin/sh
if [ -n "$FAKE_SBX_STDIN_FILE" ]; then
  # Appended, not overwritten: a caller that issues several sbx
  # invocations per method call (e.g. sbxReviewer's rm/create/exec/rm)
  # would otherwise have its one real stdin (on the exec step) clobbered
  # by the surrounding steps' empty (nil) stdin.
  cat >> "$FAKE_SBX_STDIN_FILE"
fi
if [ -n "$FAKE_SBX_LOG" ]; then
  printf '%s\n' "$*" >> "$FAKE_SBX_LOG"
fi
if [ -n "$FAKE_SBX_SLEEP" ] && { [ -z "$FAKE_SBX_SLEEP_ON" ] || [ "$1" = "$FAKE_SBX_SLEEP_ON" ]; }; then
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
if [ -n "$FAKE_SBX_STATE" ] && [ "$exit_code" = 0 ]; then
  case "$1" in
    create)
      name="$3"
      if [ -e "$FAKE_SBX_STATE/$name" ]; then
        printf "error: sandbox '%s' already exists; connect to the existing sandbox\n" "$name" >&2
        exit 1
      fi
      : > "$FAKE_SBX_STATE/$name" ;;
    rm)
      shift
      [ "$1" = "-f" ] && shift
      missing=0
      for n in "$@"; do
        [ -e "$FAKE_SBX_STATE/$n" ] || missing=1
        rm -f "$FAKE_SBX_STATE/$n"
      done
      # Opt-in: like real sbx, rm of a name that doesn't exist still removes
      # the others but exits 1.
      if [ -n "$FAKE_SBX_RM_STRICT" ] && [ "$missing" = 1 ]; then exit 1; fi ;;
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

	stateDir := filepath.Join(dir, "state")
	if err := os.Mkdir(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir fake sbx state: %v", err)
	}
	t.Setenv("FAKE_SBX_STATE", stateDir)

	return bin, logPath
}

// fakeSbxTouchMarker simulates a sandbox left over from a prior process:
// the fake sbx script's "create" will fail for name until "rm -f" clears it.
func fakeSbxTouchMarker(t *testing.T, name string) {
	t.Helper()
	stateDir := os.Getenv("FAKE_SBX_STATE")
	if stateDir == "" {
		t.Fatalf("FAKE_SBX_STATE not set; call newFakeSbx first")
	}
	if err := os.WriteFile(filepath.Join(stateDir, name), nil, 0o644); err != nil {
		t.Fatalf("touch marker %s: %v", name, err)
	}
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
		"rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify nightshift-demo-gate",
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

	ag, err := agent.New(agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: "claude"})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	inv := agent.Invocation{Role: agent.RoleCode, Model: "sonnet"}

	var logw strings.Builder
	err = sb.RunAgent(context.Background(), ag, inv, "the prompt text", &logw)
	if err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	wantArgv := []string{"exec -w " + mountDir + " nightshift-demo claude -p --model sonnet --output-format stream-json --verbose --permission-mode bypassPermissions"}
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
		"rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify nightshift-demo-gate",
		"create --name nightshift-demo claude /mnt/demo/repo",
		"create --name nightshift-demo-ctl claude /mnt/demo/repo",
		"rm -f nightshift-demo",
	}
	if !equalLines(got, want) {
		t.Fatalf("argv = %q, want %q (D11.8: agent sandbox must be cleaned up)", got, want)
	}
}

func TestCreateRemovesLeftoverSandboxes(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	sb := newSbxSandbox(bin, "claude", "demo")

	fakeSbxTouchMarker(t, sb.agentName)
	fakeSbxTouchMarker(t, sb.ctlName)
	fakeSbxTouchMarker(t, sb.verifyName)

	if err := sb.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := readLog(t, logPath)
	want := []string{
		"rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify nightshift-demo-gate",
		"create --name nightshift-demo claude /mnt/demo/repo",
		"create --name nightshift-demo-ctl claude /mnt/demo/repo",
	}
	if !equalLines(got, want) {
		t.Fatalf("argv = %q, want %q (leading rm -f must clear leftover markers before both creates)", got, want)
	}
}

func TestCreateTwiceWithoutRemove(t *testing.T) {
	bin, _ := newFakeSbx(t)

	sb1 := newSbxSandbox(bin, "claude", "demo")
	if err := sb1.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Simulates a process that died before calling Remove: a second
	// sbxSandbox for the same slug must still be able to Create.
	sb2 := newSbxSandbox(bin, "claude", "demo")
	if err := sb2.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("second Create: %v", err)
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

func TestSbxBaseVerifierArgv(t *testing.T) {
	t.Run("exec fails, cleanup still runs", func(t *testing.T) {
		bin, logPath := newFakeSbx(t)
		t.Setenv("FAKE_SBX_FAIL_SUBSTR", "exec -w")

		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "egress: a.example,b.example\n", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", SbxBin: bin, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails == 0 {
			t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "FAIL  verify-on-base:") {
			t.Fatalf("output missing FAIL line:\n%s", buf.String())
		}

		name := "nightshift-lint-demo-verify"
		want := []string{
			"rm -f " + name,
			"create --name " + name + " claude",
			"policy allow network --sandbox " + name + " a.example",
			"policy allow network --sandbox " + name + " b.example",
			"exec",
			"rm -f " + name,
		}
		got := readLog(t, logPath)
		if len(got) != len(want) {
			t.Fatalf("argv = %q, want %d lines matching %q", got, len(want), want)
		}
		for i, w := range want {
			if !strings.HasPrefix(got[i], w) {
				t.Fatalf("argv[%d] = %q, want prefix %q (full argv %q)", i, got[i], w, got)
			}
		}
	})

	t.Run("passing", func(t *testing.T) {
		bin, logPath := newFakeSbx(t)

		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", SbxBin: bin, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails != 0 {
			t.Fatalf("res = %+v, want zero fails\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "ok    verify-on-base\n") {
			t.Fatalf("output missing ok line:\n%s", buf.String())
		}

		name := "nightshift-lint-demo-verify"
		got := readLog(t, logPath)
		want := []string{"rm -f " + name}
		if len(got) == 0 || !strings.HasPrefix(got[0], want[0]) {
			t.Fatalf("argv = %q, want to start with %q", got, want[0])
		}
		if last := got[len(got)-1]; last != "rm -f "+name {
			t.Fatalf("last argv entry = %q, want final cleanup %q", last, "rm -f "+name)
		}
	})

	t.Run("with image", func(t *testing.T) {
		bin, logPath := newFakeSbx(t)

		repo := newSourceRepo(t)
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "image: foo:1\n", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", SbxBin: bin, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails != 0 {
			t.Fatalf("res = %+v, want zero fails\n%s", res, buf.String())
		}

		name := "nightshift-lint-demo-verify"
		got := readLog(t, logPath)
		want := "create --name " + name + " --template foo:1 claude"
		found := false
		for _, line := range got {
			if strings.HasPrefix(line, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("argv = %q, want a line with prefix %q", got, want)
		}
	})
}

func TestSbxBaseVerifierMissingBinary(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", SbxBin: "/nonexistent/sbx", Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails == 0 {
		t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
	}
	want := "FAIL  verify-on-base: /nonexistent/sbx not found on PATH\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, buf.String())
	}

	lintRoot := filepath.Join(home.RunsDir(homeDir), ".lint")
	entries, err := os.ReadDir(lintRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read .lint dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf(".lint dir has entries %v, want none (clone must never happen when the sbx binary is missing)", entries)
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

func TestRemoveSkipsUncreatedVerify(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	t.Setenv("FAKE_SBX_RM_STRICT", "1")
	sb := newSbxSandbox(bin, "claude", "demo")
	if err := sb.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := readLog(t, logPath)
	last := got[len(got)-1]
	if last != "rm -f nightshift-demo nightshift-demo-ctl" || strings.Contains(last, "-verify") {
		t.Fatalf("Remove argv = %q", last)
	}
}

func TestRemoveIncludesCreatedVerify(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	t.Setenv("FAKE_SBX_RM_STRICT", "1")
	sb := newSbxSandbox(bin, "claude", "demo")
	if err := sb.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sb.ResetVerify(); err != nil {
		t.Fatalf("ResetVerify: %v", err)
	}
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := readLog(t, logPath)
	want := "rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify"
	if last := got[len(got)-1]; last != want {
		t.Fatalf("Remove argv = %q, want %q", last, want)
	}
}

func TestRemoveAfterFailedVerifyRecreate(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	t.Setenv("FAKE_SBX_RM_STRICT", "1")
	sb := newSbxSandbox(bin, "claude", "demo")
	if err := sb.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sb.ResetVerify(); err != nil {
		t.Fatalf("ResetVerify: %v", err)
	}
	t.Setenv("FAKE_SBX_FAIL_SUBSTR", "create --name nightshift-demo-verify")
	if err := sb.ResetVerify(); err == nil {
		t.Fatal("second ResetVerify: want error")
	}
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := readLog(t, logPath)
	last := got[len(got)-1]
	if last != "rm -f nightshift-demo nightshift-demo-ctl" || strings.Contains(last, "-verify") {
		t.Fatalf("Remove argv = %q", last)
	}
}

func TestCreateSweepsAllNames(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	sb := newSbxSandbox(bin, "claude", "demo")
	if err := sb.Create("/mnt/demo/repo", nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	want := "rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify nightshift-demo-gate"
	if got := readLog(t, logPath); got[0] != want {
		t.Fatalf("sweep = %q, want %q", got[0], want)
	}

	t.Run("with plan sandbox", func(t *testing.T) {
		claudeAg := mustAgent(t, agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: "claude"})
		opencodeAg := mustAgent(t, agent.Selector{Agent: "opencode", Model: "openai/gpt-5"}, agent.Settings{OpencodeBin: "opencode"})
		bin, logPath := newFakeSbx(t)
		sb := newSbxSandboxAgents(bin, "demo", opencodeAg, claudeAg)
		if err := sb.Create("/mnt/demo/repo", nil); err != nil {
			t.Fatalf("Create: %v", err)
		}
		want := "rm -f nightshift-demo nightshift-demo-plan nightshift-demo-ctl nightshift-demo-verify nightshift-demo-gate"
		if got := readLog(t, logPath); got[0] != want {
			t.Fatalf("sweep = %q, want %q", got[0], want)
		}
	})
}
