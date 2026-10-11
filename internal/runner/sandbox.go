package runner

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
)

// childWaitDelay bounds how long execStream/execSplit wait for a child's
// pipes to close after the child itself has exited, once the process group
// it was placed in (setProcGroup) has been killed. Without this, a
// grandchild that inherited the pipe could hang Wait forever.
const childWaitDelay = 10 * time.Second

// cleanupTimeout bounds StopAgent and Remove. Both run on the interrupt
// path after Options.Ctx is already cancelled, so they derive their own
// context from context.Background() rather than ctx — deriving from the
// cancelled ctx would make them no-ops exactly when cleanup matters most.
const cleanupTimeout = 60 * time.Second

// Sandbox abstracts the three-sandbox (agent/ctl/verify) Docker Sandboxes
// backend so tests can substitute a stub. See reference §3.4.1 for the
// exact contract each method must honor:
//   - Available reports whether the backend is usable at all.
//   - Create provisions the agent and control sandboxes mounting exactly
//     mountDir, with egress limited to the baseline plus the given hosts.
//   - RunAgent runs one headless agent iteration, prompt on stdin, trace
//     to logw. ctx bounds it.
//   - StopAgent kills the agent's processes. Called after EVERY RunAgent,
//     including a timed-out one, BEFORE any verify or git op.
//   - Git runs git against the mounted clone in the CONTROL sandbox.
//   - ResetVerify recreates the verify sandbox from a clean image, before
//     EVERY RunVerify.
//   - RunVerify runs the verify command in the verify sandbox.
//   - Remove tears everything down.
type Sandbox interface {
	Available() bool
	Create(mountDir string, egress []string) error
	RunAgent(ctx context.Context, ag agent.Agent, inv agent.Invocation, prompt string, logw io.Writer) error
	StopAgent() error
	Git(ctx context.Context, args ...string) (string, error)
	ResetVerify() error
	RunVerify(ctx context.Context, verifyCmd string) (output string, err error)
	Remove() error
}

// sbxSandbox is the real Sandbox backend, driving the sbx CLI. All three
// sandboxes (agent/ctl/verify) use the sbx template named "claude" and
// mount the same clone dir, at the same absolute path, as the working dir.
type sbxSandbox struct {
	bin, claudeBin, agentName, planName, ctlName, verifyName, mountDir string
	codeTemplate, planTemplate                                         string
	egress                                                             []string
	created                                                            bool
}

// newSbxSandboxAgents names the agent sandbox nightshift-<slug> per D1,
// created with code's SbxAgent() template. When plan names a different
// agent than code, a second agent sandbox nightshift-<slug>-plan is
// added, created with plan's SbxAgent() template; with the same agent for
// both roles, planName stays "" and there is exactly one agent sandbox,
// as today.
func newSbxSandboxAgents(bin, slug string, code, plan agent.Agent) *sbxSandbox {
	name := "nightshift-" + slug
	sb := &sbxSandbox{
		bin:          bin,
		agentName:    name,
		ctlName:      name + "-ctl",
		verifyName:   name + "-verify",
		codeTemplate: code.SbxAgent(),
	}
	if plan.Name() != code.Name() {
		sb.planName = name + "-plan"
		sb.planTemplate = plan.SbxAgent()
	}
	return sb
}

// newSbxSandbox names the three sandboxes per D1: nightshift-<slug>
// (agent), -ctl (control), -verify (verify). It means claude for both the
// code and plan roles, so there is never a -plan sandbox.
func newSbxSandbox(bin, claudeBin, slug string) *sbxSandbox {
	claude, _ := agent.New(agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: claudeBin})
	sb := newSbxSandboxAgents(bin, slug, claude, claude)
	sb.claudeBin = claudeBin
	return sb
}

func (s *sbxSandbox) Available() bool {
	_, err := exec.LookPath(s.bin)
	return err == nil
}

// mk creates sandbox name from template mounting s.mountDir, then allows
// egress to each of s.egress, one host per call.
func (s *sbxSandbox) mk(name, template string) error {
	out, err := execStream(context.Background(), nil, io.Discard, s.bin, "create", "--name", name, template, s.mountDir)
	if err != nil {
		return fmt.Errorf("sbx create %s: %v: %s", name, err, out)
	}
	for _, host := range s.egress {
		out, err := execStream(context.Background(), nil, io.Discard, s.bin, "policy", "allow", "network", "--sandbox", name, host)
		if err != nil {
			return fmt.Errorf("sbx policy allow %s (%s): %v: %s", host, name, err, out)
		}
	}
	return nil
}

// rmNames lists the agent sandbox(es) plus ctl and verify, in the fixed
// order every rm -f and Remove call uses: agent, plan (if any), ctl,
// verify.
func (s *sbxSandbox) rmNames() []string {
	names := []string{s.agentName}
	if s.planName != "" {
		names = append(names, s.planName)
	}
	return append(names, s.ctlName, s.verifyName)
}

// Create provisions the agent sandbox(es) and the control sandbox; the
// verify sandbox is created lazily by ResetVerify. The names are
// runner-owned and all run state lives in the mounted clone, so Create
// always removes any leftover sandboxes under these names first (one
// best-effort "rm -f") — idempotent recovery from an unclean prior exit,
// and safer too: a leftover agent sandbox may have been modified by its
// previous agent.
// D11.8: if creating the plan or control sandbox fails after an earlier
// sandbox was already created, that earlier sandbox is removed before
// returning the error (the reference leaked it).
func (s *sbxSandbox) Create(mountDir string, egress []string) error {
	s.mountDir = mountDir
	s.egress = egress

	_, _ = execStream(context.Background(), nil, io.Discard, s.bin, append([]string{"rm", "-f"}, s.rmNames()...)...)

	if err := s.mk(s.agentName, s.codeTemplate); err != nil {
		return err
	}
	if s.planName != "" {
		if err := s.mk(s.planName, s.planTemplate); err != nil {
			_, _ = execStream(context.Background(), nil, io.Discard, s.bin, "rm", "-f", s.agentName)
			return err
		}
	}
	if err := s.mk(s.ctlName, "claude"); err != nil {
		rm := []string{"rm", "-f", s.agentName}
		if s.planName != "" {
			rm = append(rm, s.planName)
		}
		_, _ = execStream(context.Background(), nil, io.Discard, s.bin, rm...)
		return err
	}
	s.created = true
	return nil
}

// RunAgent execs in the agent sandbox, unless inv is a planning
// invocation and a distinct -plan sandbox exists, in which case it execs
// there instead.
func (s *sbxSandbox) RunAgent(ctx context.Context, ag agent.Agent, inv agent.Invocation, prompt string, logw io.Writer) error {
	name := s.agentName
	if inv.Role == agent.RolePlan && s.planName != "" {
		name = s.planName
	}
	args := append([]string{"exec", "-w", s.mountDir, name}, ag.Argv(inv)...)
	_, err := execStream(ctx, strings.NewReader(prompt), logw, s.bin, args...)
	return err
}

// StopAgent stops the agent sandbox and, when a distinct -plan sandbox
// exists, stops it too — always both, so cleanup isn't short-circuited
// by the first failing — returning the first error seen.
func (s *sbxSandbox) StopAgent() error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := execStream(ctx, nil, io.Discard, s.bin, "stop", s.agentName)
	if s.planName != "" {
		_, err2 := execStream(ctx, nil, io.Discard, s.bin, "stop", s.planName)
		if err == nil {
			err = err2
		}
	}
	return err
}

// Git runs git against the mounted clone in the control sandbox, with
// fsmonitor and hooks disabled. D11.7: only trailing whitespace is
// trimmed from stdout (the reference TrimSpace'd it, losing leading
// whitespace callers like the result note's diff --stat want to keep).
func (s *sbxSandbox) Git(ctx context.Context, args ...string) (string, error) {
	var stdout, stderr bytes.Buffer
	full := append([]string{"exec", "-w", s.mountDir, s.ctlName, "git", "-c", "core.fsmonitor=", "-c", "core.hooksPath=/dev/null"}, args...)
	err := execSplit(ctx, nil, &stdout, &stderr, s.bin, full...)
	out := strings.TrimRight(stdout.String(), " \t\r\n")
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (s *sbxSandbox) ResetVerify() error {
	_, _ = execStream(context.Background(), nil, io.Discard, s.bin, "rm", "-f", s.verifyName)
	return s.mk(s.verifyName, "claude")
}

func (s *sbxSandbox) RunVerify(ctx context.Context, verifyCmd string) (string, error) {
	return execStream(ctx, nil, io.Discard, s.bin, "exec", "-w", s.mountDir, s.verifyName, "sh", "-c", verifyCmd)
}

func (s *sbxSandbox) Remove() error {
	if !s.created {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := execStream(ctx, nil, io.Discard, s.bin, append([]string{"rm", "-f"}, s.rmNames()...)...)
	return err
}

// sbxBaseVerifier is the real BaseVerifier (lint's check 8), used
// whenever LintOptions.Verifier is nil. Its sandbox name is
// "nightshift-lint-<slug>-verify" — distinct from a running spec's own
// "nightshift-<slug>-verify" — so linting a spec never collides with a
// concurrent run of that same spec.
type sbxBaseVerifier struct {
	bin, name string
}

// Verify creates a fresh sandbox mounting dir, allows egress to each
// host, runs cmd in it, and always removes the sandbox afterward — even
// when create or exec fails — via a deferred cleanup registered right
// after the leading best-effort "rm -f", on its own timeout so it still
// runs if ctx is what failed.
func (v sbxBaseVerifier) Verify(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
	_, _ = execStream(context.Background(), nil, io.Discard, v.bin, "rm", "-f", v.name)
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		_, _ = execStream(cctx, nil, io.Discard, v.bin, "rm", "-f", v.name)
	}()

	if out, err := execStream(context.Background(), nil, io.Discard, v.bin, "create", "--name", v.name, "claude", dir); err != nil {
		return out, fmt.Errorf("create: %w", err)
	}
	for _, host := range egress {
		if out, err := execStream(context.Background(), nil, io.Discard, v.bin, "policy", "allow", "network", "--sandbox", v.name, host); err != nil {
			return out, fmt.Errorf("policy allow %s: %w", host, err)
		}
	}

	out, err := execStream(ctx, nil, io.Discard, v.bin, "exec", "-w", dir, v.name, "sh", "-c", cmd)
	return stripSbxBanner(out), err
}

// execStream runs one sbx command, writing its combined output to both
// out and an internal tail buffer, and returns the tail's full content.
// A context deadline reports as context.DeadlineExceeded specifically (the
// sentinel itself), so callers can compare with ==.
func execStream(ctx context.Context, stdin io.Reader, out io.Writer, bin string, args ...string) (string, error) {
	var tail bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	setProcGroup(cmd)
	cmd.WaitDelay = childWaitDelay
	cmd.Stdin = stdin
	mw := io.MultiWriter(out, &tail)
	cmd.Stdout = mw
	cmd.Stderr = mw
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return tail.String(), context.DeadlineExceeded
	}
	return tail.String(), err
}

// execSplit is execStream with stdout and stderr captured separately.
func execSplit(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, bin string, args ...string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	setProcGroup(cmd)
	cmd.WaitDelay = childWaitDelay
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return context.DeadlineExceeded
	}
	return err
}

// sbxBannerLine matches a leading sandbox-startup banner line that sbx may
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
