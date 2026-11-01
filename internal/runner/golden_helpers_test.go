package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/agent"
)

// goldenRunAgent drives one RunAgent call, building the agent.Agent and
// agent.Invocation RunAgent now takes instead of a bare model string —
// this helper is the only place TestClaudeArgvGolden depends on that
// signature, so a later RunAgent change only means editing this function,
// not the golden test or its fixture.
func goldenRunAgent(sb *sbxSandbox, planning bool, model, prompt string) error {
	role := agent.RoleCode
	if planning {
		role = agent.RolePlan
	}
	ag, err := agent.New(agent.Selector{Agent: "claude"}, agent.Settings{ClaudeBin: "claude"})
	if err != nil {
		return err
	}
	inv := agent.Invocation{Role: role, Model: model}
	return sb.RunAgent(context.Background(), ag, inv, prompt, io.Discard)
}

// goldenReview drives sbxReviewer.Review through today's signature
// (model as a bare string). Task 2 moves sbxReviewer onto agent.Agent;
// this helper isolates that change from the golden test.
func goldenReview(rv sbxReviewer, ws, model, prompt string) (string, error) {
	return rv.Review(context.Background(), ws, model, prompt)
}

// readGoldenSandboxLines returns just the sandbox-sequence portion of
// the pinned claude-argv golden file — everything through the second
// "rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify",
// before the review sandbox's lines. TestSameAgentOneSandbox uses it to
// confirm newSbxSandboxAgents produces byte-identical argv to
// newSbxSandbox when both roles resolve to the same agent.
func readGoldenSandboxLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "golden", "claude-argv.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	const sandboxLines = 13
	if len(lines) < sandboxLines {
		t.Fatalf("golden file has only %d lines, want at least %d", len(lines), sandboxLines)
	}
	return lines[:sandboxLines]
}
