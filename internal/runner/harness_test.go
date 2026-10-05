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
	agentAlive                           bool
	verifyRaced                          bool

	agent  func(ctx context.Context, iter int, dir string) error
	verify func(iter int, dir string) error

	// gitHook, if set, rewrites Git's real output (e.g. to simulate a
	// corrupted sha); banner, if set, is prefixed to every Git result
	// (e.g. to simulate an sbx startup banner line).
	gitHook func(args []string, out string) string
	banner  string
}

func newStubSandbox() *stubSandbox { return &stubSandbox{avail: true} }

func (s *stubSandbox) Available() bool { return s.avail }

func (s *stubSandbox) Create(mountDir string, egress []string) error {
	s.mountDir = mountDir
	s.egress = egress
	return nil
}

func (s *stubSandbox) RunAgent(ctx context.Context, model, prompt string, logw io.Writer) error {
	s.iter++
	s.agentN++
	s.models = append(s.models, model)
	if logw != nil {
		fmt.Fprintf(logw, "AGENT RAW OUTPUT iter=%d model=%s\n", s.iter, model)
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
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = s.mountDir
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
	return nil
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

func writeUncommitted(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
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
