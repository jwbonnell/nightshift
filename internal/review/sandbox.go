package review

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// cleanupTimeout bounds Stop and Remove: both derive their own context
// from context.Background() rather than reusing the caller's ctx, so
// cleanup still happens even after the run's own context has been
// cancelled or has expired.
const cleanupTimeout = 60 * time.Second

// Sandbox abstracts the single read-only review sandbox nightshift pr
// runs its reviewer agent in — distinct from, and far less privileged
// than, `nightshift run`'s agent/ctl/verify sandboxes: it never mounts an
// agent's clone, only a change's exported tree, and never touches git.
type Sandbox interface {
	Available() bool
	Create(ctx context.Context, workDir, sbxAgent string, env []string) error
	AllowNetwork(ctx context.Context, host string, env []string) error
	Exec(ctx context.Context, dir string, argv []string, stdin string, env []string, stderr io.Writer) (stdout string, err error)
	Stop(env []string) error
	Remove(env []string) error
}

// sbxSandbox is the real Sandbox, driving the sbx CLI in one
// "nightshift-pr-<RunID>" sandbox.
type sbxSandbox struct {
	bin, name string
	created   bool
}

func newSbxSandbox(bin, runID string) *sbxSandbox {
	return &sbxSandbox{bin: bin, name: "nightshift-pr-" + runID}
}

func (s *sbxSandbox) Available() bool {
	_, err := exec.LookPath(s.bin)
	return err == nil
}

// Create provisions the sandbox mounting workDir with sbxAgent's
// template ("claude" or "opencode"), cancellable via ctx.
func (s *sbxSandbox) Create(ctx context.Context, workDir, sbxAgent string, env []string) error {
	out, err := execCombined(ctx, nil, env, s.bin, "create", "--name", s.name, sbxAgent, workDir)
	if err != nil {
		return fmt.Errorf("sbx create %s: %w: %s", s.name, err, strings.TrimSpace(out))
	}
	s.created = true
	return nil
}

// AllowNetwork allows host as egress for the sandbox (P12.3), one host
// per call.
func (s *sbxSandbox) AllowNetwork(ctx context.Context, host string, env []string) error {
	out, err := execCombined(ctx, nil, env, s.bin, "policy", "allow", "network", "--sandbox", s.name, host)
	if err != nil {
		return fmt.Errorf("sbx policy allow network %s (%s): %w: %s", host, s.name, err, strings.TrimSpace(out))
	}
	return nil
}

// Exec runs argv in the sandbox with stdin as the reviewer's prompt,
// stderr routed to the given writer, stdout captured and returned with
// any leading sbx banner stripped. A context deadline reports as
// context.DeadlineExceeded specifically, so callers can compare with ==.
func (s *sbxSandbox) Exec(ctx context.Context, dir string, argv []string, stdin string, env []string, stderr io.Writer) (string, error) {
	var stdout bytes.Buffer
	args := append([]string{"exec", "-w", dir, s.name}, argv...)
	err := execSplit(ctx, strings.NewReader(stdin), &stdout, stderr, env, s.bin, args...)
	out := stripSbxBanner(stdout.String())
	if ctx.Err() == context.DeadlineExceeded {
		return out, context.DeadlineExceeded
	}
	return out, err
}

// Stop removes the sandbox under its own cleanup timeout, independent of
// any caller ctx. A no-op when the sandbox was never created.
func (s *sbxSandbox) Stop(env []string) error {
	if !s.created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	out, err := execCombined(ctx, nil, env, s.bin, "rm", "-f", s.name)
	if err != nil {
		return fmt.Errorf("sbx rm -f %s: %w: %s — remove it by hand with `sbx rm -f %s`", s.name, err, strings.TrimSpace(out), s.name)
	}
	s.created = false
	return nil
}

// Remove is Stop's "rm -f", also under its own cleanup timeout, but
// never resets created — the run is ending either way.
func (s *sbxSandbox) Remove(env []string) error {
	if !s.created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := execCombined(ctx, nil, env, s.bin, "rm", "-f", s.name)
	return err
}

// execCombined runs one sbx command with stdout+stderr combined into the
// returned string. env, when non-nil, replaces the child's environment
// entirely (see scrubEnv); nil inherits the current process's
// environment, exec.Cmd's own default. A context deadline reports as
// context.DeadlineExceeded specifically.
func execCombined(ctx context.Context, stdin io.Reader, env []string, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = stdin
	cmd.Env = env
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return buf.String(), context.DeadlineExceeded
	}
	return buf.String(), err
}

// execSplit is execCombined with stdout and stderr captured separately.
func execSplit(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, env []string, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Stdin = stdin
	cmd.Env = env
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	return err
}

// sbxBannerLine matches a leading sandbox-startup banner line sbx may
// prepend to command output.
var sbxBannerLine = regexp.MustCompile(`^Sandbox \S+ (started successfully|created|starting)\.?\s*$`)

// stripSbxBanner drops leading lines matching sbxBannerLine (after
// TrimSpace), stopping at the first non-matching line.
func stripSbxBanner(s string) string {
	for {
		before, after, found := strings.Cut(s, "\n")
		if !found {
			if sbxBannerLine.MatchString(strings.TrimSpace(s)) {
				return ""
			}
			return s
		}
		if sbxBannerLine.MatchString(strings.TrimSpace(before)) {
			s = after
			continue
		}
		return s
	}
}

// scrubEnv drops entries from environ whose key has prefix GIT_CONFIG,
// NIGHTSHIFT_GITLAB_ or NIGHTSHIFT_GITHUB_, or whose value contains
// gitlabToken or githubToken (when non-empty) — so a forge token can
// never reach the review sandbox's environment, however it got into
// os.Environ(). Order is otherwise preserved.
func scrubEnv(environ []string, gitlabToken, githubToken string) []string {
	out := make([]string, 0, len(environ))
	for _, e := range environ {
		key, val, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "GIT_CONFIG") ||
			strings.HasPrefix(key, "NIGHTSHIFT_GITLAB_") ||
			strings.HasPrefix(key, "NIGHTSHIFT_GITHUB_") {
			continue
		}
		if gitlabToken != "" && strings.Contains(val, gitlabToken) {
			continue
		}
		if githubToken != "" && strings.Contains(val, githubToken) {
			continue
		}
		out = append(out, e)
	}
	return out
}
