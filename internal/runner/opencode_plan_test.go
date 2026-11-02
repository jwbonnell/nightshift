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

// noisyOpencodeOutput is a copy of internal/agent's success_noisy.jsonl: a
// successful opencode run whose tool calls and text mention rate limits and
// 5xx-looking numbers.
const noisyOpencodeOutput = `{"type":"text","part":{"text":"Checking rateLimit handling and rate-limited paths; quota and overloaded noted."}}
{"type":"tool_use","tool":"grep","input":{"pattern":"rateLimit"},"output":"loop.go:512: // rate-limited, HTTP 503 overloaded quota"}
{"type":"text","part":{"text":"See line 512 for HTTP 503 handling."}}
{"type":"tool_use","tool":"bash","state":{"output":"rate_limit quota overloaded line 512 HTTP 503"}}
{"type":"text","part":{"text":"Plan written to PROGRESS.md."}}
{"type":"step_finish","part":{"cost":0.02}}
`

func TestOpencodeNoisyPlanSucceeds(t *testing.T) {
	t.Setenv("NIGHTSHIFT_OPENCODE_CONFIG", "")
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.output = func(int) string { return noisyOpencodeOutput }

	var slept []time.Duration
	opts := Options{
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
		Notifier: noopNotifierForTest{}, PlanOnly: true,
		Sleep: func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		},
	}
	r := newTestRun(t, repo, "planner_model: opencode/openai/gpt-5\n", opts, sb)

	status := r.loop()
	if status != StatusPlanned {
		t.Fatalf("status=%v, want planned", status)
	}
	if len(slept) != 0 {
		t.Fatalf("Sleep called %v, want no retry", slept)
	}
	if sb.agentN != 1 {
		t.Fatalf("agentN = %d, want 1", sb.agentN)
	}
	log, err := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(log), "agent error") {
		t.Fatalf("runner.log has agent error:\n%s", log)
	}
}
