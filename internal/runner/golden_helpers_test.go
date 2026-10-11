package runner

import (
	"context"
	"io"
)

// goldenRunAgent drives one RunAgent call through today's signature
// (model as a bare string). internal/agent (Task 2) changes RunAgent to
// take an agent.Agent and an agent.Invocation instead, so this helper is
// the only place TestClaudeArgvGolden depends on that signature: moving
// RunAgent only means editing this function, not the golden test or its
// fixture. planning is unused today (there's only one agent sandbox
// until Task 5's -plan split) but keeps the call site shaped like the
// post-migration one.
func goldenRunAgent(sb *sbxSandbox, planning bool, model, prompt string) error {
	_ = planning
	return sb.RunAgent(context.Background(), model, prompt, io.Discard)
}

// goldenReview drives sbxReviewer.Review through today's signature
// (model as a bare string). Task 2 moves sbxReviewer onto agent.Agent;
// this helper isolates that change from the golden test.
func goldenReview(rv sbxReviewer, ws, model, prompt string) (string, error) {
	return rv.Review(context.Background(), ws, model, prompt)
}
