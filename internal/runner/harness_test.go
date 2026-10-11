package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/home"
)

// stubSandbox is an in-process Sandbox for loop tests: Git passes through
// to the real clone on disk (via host git, standing in for the control
// sandbox), while agent/verify behavior is entirely driven by the agent/
// verify hooks so tests can script exactly what "the agent" does each
// iteration without a real claude or sbx.
type stubSandbox struct {
	avail    bool
	mountDir string
	egress   []string
	iter     int
	models   []string

	agentN, verifyN, stopN, resetVerifyN int
	removed                              bool
	removeN                              int
	removeErr                            error
	agentAlive                           bool
	verifyRaced                          bool

	// prompts captures every RunAgent call's prompt, in order, for tests
	// that check the prompt bytes are identical regardless of which agent
	// is running them.
	prompts []string

	// plannerNoWrite, when true, suppresses RunAgent's default behavior of
	// appending a line to PROGRESS.md on a planning prompt — tests that
	// need to exercise a planner that makes no progress set this.
	plannerNoWrite bool

	agent  func(ctx context.Context, iter int, dir string) error
	verify func(iter int, dir string) error

	// output, if set, replaces the fixed "AGENT RAW OUTPUT" line written to
	// logw — tests use it to script the agent's raw stdout, e.g. a
	// --output-format json result line.
	output func(iter int) string

	// gitHook, if set, rewrites Git's real output (e.g. to simulate a
	// corrupted sha); banner, if set, is prefixed to every Git result
	// (e.g. to simulate an sbx startup banner line).
	gitHook func(args []string, out string) string
	banner  string

	// failGit, if set and it returns true for a given argv, makes Git
	// fail with a synthetic error instead of running the real command —
	// simulating e.g. a hard reset failing in the control sandbox.
	failGit func(args []string) bool

	// gitBin, if set, is the absolute path Git execs instead of looking up
	// "git" on PATH — used by tests that put a logging wrapper first on
	// PATH (to catch host git ever running with the agent's clone as its
	// cwd) and need the simulated -ctl sandbox's own git calls, which
	// legitimately use the clone as cwd, to bypass that wrapper.
	gitBin string
}

func newStubSandbox() *stubSandbox { return &stubSandbox{avail: true} }

// stripGitIdentityEnv drops GIT_AUTHOR_NAME/EMAIL and GIT_COMMITTER_NAME/
// EMAIL from env, leaving everything else (including GIT_CONFIG_GLOBAL/
// GIT_CONFIG_NOSYSTEM) untouched.
func stripGitIdentityEnv(env []string) []string {
	drop := map[string]bool{
		"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
		"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	}
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if drop[k] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (s *stubSandbox) Available() bool { return s.avail }

func (s *stubSandbox) Create(mountDir string, egress []string) error {
	s.mountDir = mountDir
	s.egress = egress
	return nil
}

func (s *stubSandbox) RunAgent(ctx context.Context, ag agent.Agent, inv agent.Invocation, prompt string, logw io.Writer) error {
	s.iter++
	s.agentN++
	s.models = append(s.models, inv.Model)
	s.prompts = append(s.prompts, prompt)
	if logw != nil {
		if s.output != nil {
			fmt.Fprint(logw, s.output(s.iter))
		} else {
			fmt.Fprintf(logw, "AGENT RAW OUTPUT iter=%d model=%s\n", s.iter, inv.Model)
		}
	}

	if strings.HasPrefix(prompt, planPreamble) && !s.plannerNoWrite {
		appendStubPlanLine(s.mountDir)
	}

	var err error
	if s.agent != nil {
		err = s.agent(ctx, s.iter, s.mountDir)
	}
	if err == nil {
		select {
		case <-ctx.Done():
			err = ctx.Err()
		default:
		}
	}
	s.agentAlive = errors.Is(err, context.DeadlineExceeded)
	return err
}

func (s *stubSandbox) StopAgent() error {
	s.stopN++
	s.agentAlive = false
	return nil
}

// Git shells out directly (not through gitx.Run, which TrimSpaces both
// ends) so it matches the real sbxSandbox.Git's D11.7 trimming exactly:
// only trailing whitespace is removed, preserving a leading space callers
// like the result note's diffstat want to keep.
func (s *stubSandbox) Git(ctx context.Context, args ...string) (string, error) {
	if s.failGit != nil && s.failGit(args) {
		return "simulated git failure", errors.New("simulated git failure")
	}
	bin := "git"
	if s.gitBin != "" {
		bin = s.gitBin
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = s.mountDir
	// The real -ctl sandbox is a separate container with its own env, not
	// the host nightshift process's — so a commit made through it (e.g. a
	// dependency merge) gets its identity from the clone's own git config
	// (set by cloneRepo), never from TestMain's hermetic GIT_AUTHOR_NAME/
	// GIT_COMMITTER_NAME env vars. Strip just those four so this stub
	// matches that.
	cmd.Env = stripGitIdentityEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	result := strings.TrimRight(string(out), " \t\r\n")
	if s.gitHook != nil {
		result = s.gitHook(args, result)
	}
	if s.banner != "" {
		result = s.banner + result
	}
	return result, err
}

func (s *stubSandbox) ResetVerify() error {
	s.resetVerifyN++
	return nil
}

func (s *stubSandbox) RunVerify(ctx context.Context, verifyCmd string) (string, error) {
	s.verifyN++
	if s.agentAlive {
		s.verifyRaced = true
	}
	var err error
	if s.verify != nil {
		err = s.verify(s.iter, s.mountDir)
	}
	return "verify output\n", err
}

func (s *stubSandbox) Remove() error {
	s.removed = true
	s.removeN++
	return s.removeErr
}

// newTestRun resolves a fresh "demo" spec against repo and runs setup(),
// then wires sb in as its Sandbox via Create, ready for r.loop().
func newTestRun(t *testing.T, repo, extraFrontmatter string, opts Options, sb *stubSandbox) *run {
	t.Helper()
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", extraFrontmatter, repo)

	opts.Home = homeDir
	opts.SpecArg = "demo"
	opts.StateRoot = home.RunsDir(homeDir)
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}

	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	r, err := setup(opts, sp)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() {
		if r.logFile != nil {
			r.logFile.Close()
		}
		if r.agentLog != nil {
			r.agentLog.Close()
		}
	})

	if err := sb.Create(r.clone, sp.Egress); err != nil {
		t.Fatalf("sb.Create: %v", err)
	}
	r.sb = sb
	return r
}

func commitFile(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mustGit(t, dir, "add", "-A")
	mustGit(t, dir, "commit", "-q", "-m", msg)
}

// commitFileForced is commitFile for a file excluded via
// .git/info/exclude (PROGRESS.md, QUESTION.md, .spec-complete) — `git add
// -A` alone would silently skip it, so tests simulating a misbehaving
// agent that commits scaffold files anyway need `-f`.
func commitFileForced(t *testing.T, dir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	mustGit(t, dir, "add", "-f", name)
	mustGit(t, dir, "commit", "-q", "-m", msg)
}

func writeUncommitted(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// appendStubPlanLine simulates a well-behaved planner writing to
// PROGRESS.md; it has no *testing.T since it's called from RunAgent, not
// a test body directly, and a stub agent that fails to write here isn't a
// test failure in itself — it's exactly what plannerNoWrite tests want.
func appendStubPlanLine(dir string) {
	f, err := os.OpenFile(filepath.Join(dir, "PROGRESS.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	// Dated like a real appendProgress line (D11.9) so tests that scan
	// PROGRESS.md for "- <timestamp> ..." lines don't choke on a shorter
	// stub-only line.
	fmt.Fprintln(f, "- 0000-00-00 00:00 stub plan")
}

func appendProgressRaw(t *testing.T, dir, text string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "PROGRESS.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open PROGRESS.md: %v", err)
	}
	defer f.Close()
	fmt.Fprintln(f, text)
}

func touchComplete(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".spec-complete"), nil, 0o644); err != nil {
		t.Fatalf("touch .spec-complete: %v", err)
	}
}

func writeQuestion(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "QUESTION.md"), []byte(text), 0o644); err != nil {
		t.Fatalf("write QUESTION.md: %v", err)
	}
}
