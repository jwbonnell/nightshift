package runner

import (
	"context"
	"io"
	"testing"

	"github.com/jwbonnell/nightshift/internal/agent"
)

// mustAgent builds an agent.Agent for a test, failing the test on error
// (which should never happen for a selector the test itself constructed).
func mustAgent(t *testing.T, sel agent.Selector, s agent.Settings) agent.Agent {
	t.Helper()
	ag, err := agent.New(sel, s)
	if err != nil {
		t.Fatalf("agent.New(%+v): %v", sel, err)
	}
	return ag
}

// TestMixedAgentsPlanSandbox covers a claude planner paired with an
// opencode coder: a second agent sandbox (nightshift-<slug>-plan) must be
// created with the planner's template, used for planning RunAgent calls,
// stopped alongside the main agent sandbox, and removed with it too.
func TestMixedAgentsPlanSandbox(t *testing.T) {
	claudeAg := mustAgent(t, agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: "claude"})
	opencodeAg := mustAgent(t, agent.Selector{Agent: "opencode", Model: "openai/gpt-5"}, agent.Settings{OpencodeBin: "opencode"})

	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"

	sb := newSbxSandboxAgents(bin, "demo", opencodeAg, claudeAg)
	if err := sb.Create(mountDir, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	planInv := agent.Invocation{Role: agent.RolePlan, Model: "opus"}
	if err := sb.RunAgent(context.Background(), claudeAg, planInv, "plan prompt", io.Discard); err != nil {
		t.Fatalf("plan RunAgent: %v", err)
	}
	codeInv := agent.Invocation{Role: agent.RoleCode, Model: "openai/gpt-5"}
	if err := sb.RunAgent(context.Background(), opencodeAg, codeInv, "code prompt", io.Discard); err != nil {
		t.Fatalf("code RunAgent: %v", err)
	}

	if err := sb.StopAgent(); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	const codePermission = `{"edit":"allow","bash":{"*":"allow","git push*":"deny","git remote*":"deny","git config*":"deny"},"webfetch":"allow","external_directory":"deny"}`
	got := readLog(t, logPath)
	want := []string{
		"rm -f nightshift-demo nightshift-demo-plan nightshift-demo-ctl nightshift-demo-verify",
		"create --name nightshift-demo opencode " + mountDir,
		"create --name nightshift-demo-plan claude " + mountDir,
		"create --name nightshift-demo-ctl claude " + mountDir,
		"exec -w " + mountDir + " nightshift-demo-plan claude -p --model opus --output-format stream-json --verbose --permission-mode bypassPermissions",
		"exec -w " + mountDir + " nightshift-demo env OPENCODE_PERMISSION=" + codePermission + " opencode run --format json --auto -m openai/gpt-5",
		"stop nightshift-demo",
		"stop nightshift-demo-plan",
		"rm -f nightshift-demo nightshift-demo-plan nightshift-demo-ctl nightshift-demo-verify",
	}
	if !equalLines(got, want) {
		t.Fatalf("argv =\n%v\nwant\n%v", got, want)
	}
}

// TestMixedAgentsPlanCreateFailureRemovesAgent covers D11.8 extended to
// the -plan sandbox: if creating it fails, the already-created main
// agent sandbox must be cleaned up before Create returns.
func TestMixedAgentsPlanCreateFailureRemovesAgent(t *testing.T) {
	claudeAg := mustAgent(t, agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: "claude"})
	opencodeAg := mustAgent(t, agent.Selector{Agent: "opencode", Model: "openai/gpt-5"}, agent.Settings{OpencodeBin: "opencode"})

	bin, logPath := newFakeSbx(t)
	t.Setenv("FAKE_SBX_FAIL_SUBSTR", "create --name nightshift-demo-plan")

	sb := newSbxSandboxAgents(bin, "demo", opencodeAg, claudeAg)
	if err := sb.Create("/mnt/demo/repo", nil); err == nil {
		t.Fatalf("expected Create to fail when the -plan sandbox create fails")
	}

	got := readLog(t, logPath)
	want := []string{
		"rm -f nightshift-demo nightshift-demo-plan nightshift-demo-ctl nightshift-demo-verify",
		"create --name nightshift-demo opencode /mnt/demo/repo",
		"create --name nightshift-demo-plan claude /mnt/demo/repo",
		"rm -f nightshift-demo",
	}
	if !equalLines(got, want) {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

// TestSbxImageArgv covers D2 (spec `image:`): with sb.image set, the
// agent, -plan and verify sandboxes are created with "--template <image>"
// before their agent template, but the -ctl sandbox never gets it.
func TestSbxImageArgv(t *testing.T) {
	claudeAg := mustAgent(t, agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: "claude"})
	opencodeAg := mustAgent(t, agent.Selector{Agent: "opencode", Model: "openai/gpt-5"}, agent.Settings{OpencodeBin: "opencode"})

	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"

	sb := newSbxSandboxAgents(bin, "demo", opencodeAg, claudeAg)
	sb.image = "foo:1"
	if err := sb.Create(mountDir, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := sb.ResetVerify(); err != nil {
		t.Fatalf("ResetVerify: %v", err)
	}

	got := readLog(t, logPath)
	want := []string{
		"rm -f nightshift-demo nightshift-demo-plan nightshift-demo-ctl nightshift-demo-verify",
		"create --name nightshift-demo --template foo:1 opencode " + mountDir,
		"create --name nightshift-demo-plan --template foo:1 claude " + mountDir,
		"create --name nightshift-demo-ctl claude " + mountDir,
		"rm -f nightshift-demo-verify",
		"create --name nightshift-demo-verify --template foo:1 claude " + mountDir,
	}
	if !equalLines(got, want) {
		t.Fatalf("argv =\n%v\nwant\n%v (ctl must never get --template)", got, want)
	}
}

// TestSameAgentOneSandbox mirrors TestClaudeArgvGolden's sandbox
// sequence exactly, but builds the sbxSandbox via newSbxSandboxAgents
// (as runner wiring now always does) instead of newSbxSandbox directly.
// With the same agent for both roles, the resulting argv log must still
// equal the golden file byte-for-byte: no -plan sandbox, same templates.
func TestSameAgentOneSandbox(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"
	egress := []string{"api.example.com"}

	codeAg := mustAgent(t, agent.Selector{Agent: "claude", Model: "sonnet"}, agent.Settings{ClaudeBin: "claude"})
	planAg := mustAgent(t, agent.Selector{Agent: "claude", Model: "opus"}, agent.Settings{ClaudeBin: "claude"})

	sb := newSbxSandboxAgents(bin, "demo", codeAg, planAg)
	if err := sb.Create(mountDir, egress); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := goldenRunAgent(sb, true, "opus", "plan the thing"); err != nil {
		t.Fatalf("planner RunAgent: %v", err)
	}
	if err := goldenRunAgent(sb, false, "sonnet", "do the thing"); err != nil {
		t.Fatalf("coder RunAgent: %v", err)
	}

	if err := sb.StopAgent(); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	if err := sb.ResetVerify(); err != nil {
		t.Fatalf("ResetVerify: %v", err)
	}
	if _, err := sb.RunVerify(context.Background(), "echo hi"); err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	got := readLog(t, logPath)
	want := readGoldenSandboxLines(t)
	if !equalLines(got, want) {
		t.Fatalf("argv log mismatch:\ngot:\n%v\nwant:\n%v", got, want)
	}
}
