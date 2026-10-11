package runner

import (
	"context"
	"io"

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
