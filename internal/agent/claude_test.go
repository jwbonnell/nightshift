package agent

import (
	"strings"
	"testing"
)

const rateLimitLine = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"terminal_reason":"api_error","result":"You've hit your session limit · resets 7am (UTC)","num_turns":8}`

func TestClaudeParseRateLimit(t *testing.T) {
	res := claudeAgent{}.Parse(rateLimitLine)
	if !res.Found {
		t.Fatal("expected Found")
	}
	if !res.IsError {
		t.Error("expected IsError")
	}
	if res.APIStatus != 429 {
		t.Errorf("APIStatus = %d, want 429", res.APIStatus)
	}
	if want := "You've hit your session limit · resets 7am (UTC)"; res.Message != want {
		t.Errorf("Message = %q, want %q", res.Message, want)
	}
	if !(claudeAgent{}).RateLimited(res) {
		t.Error("expected RateLimited")
	}
}

func TestClaudeParseBannerThenJSON(t *testing.T) {
	out := "Sandbox nightshift-abc123 started successfully\n" + rateLimitLine + "\n"
	res := claudeAgent{}.Parse(out)
	if !res.Found || res.APIStatus != 429 {
		t.Errorf("got %+v", res)
	}
}

func TestClaudeParseSuccess(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":3}`
	res := claudeAgent{}.Parse(line)
	if !res.Found {
		t.Fatal("expected Found")
	}
	if res.IsError {
		t.Error("expected !IsError")
	}
	if res.Message != "done" || res.Text != "done" {
		t.Errorf("Message/Text = %q/%q, want %q", res.Message, res.Text, "done")
	}
	if res.CostKnown {
		t.Error("expected CostKnown = false when total_cost_usd is absent")
	}
	if (claudeAgent{}).RateLimited(res) {
		t.Error("expected !RateLimited")
	}
	if (claudeAgent{}).Transient(res) {
		t.Error("expected Transient = false for claude, always")
	}
}

func TestClaudeParseCost(t *testing.T) {
	line := `{"type":"result","is_error":false,"result":"done","total_cost_usd":1.25}`
	res := claudeAgent{}.Parse(line)
	if !res.CostKnown || res.CostUSD != 1.25 {
		t.Errorf("CostUSD/CostKnown = %v/%v, want 1.25/true", res.CostUSD, res.CostKnown)
	}
}

func TestClaudeParseLastLineWins(t *testing.T) {
	first := `{"type":"result","is_error":false,"result":"first"}`
	second := `{"type":"result","is_error":true,"api_error_status":500,"result":"second"}`
	res := claudeAgent{}.Parse(first + "\n" + second)
	if !res.IsError || res.APIStatus != 500 || res.Message != "second" {
		t.Errorf("got %+v, want the second line", res)
	}
}

func TestClaudeParseGarbageOrEmpty(t *testing.T) {
	for _, out := range []string{"", "not json at all", "garbage\nmore garbage\n{not valid json}", `{"foo":"bar"}`} {
		if res := (claudeAgent{}).Parse(out); res.Found {
			t.Errorf("Parse(%q) = %+v, want Found=false", out, res)
		}
	}
}

func TestClaudeParseLenientNoTypeField(t *testing.T) {
	// The reviewer's fakes (and some real reviewer wire responses) omit
	// "type" entirely; Parse must still find the result.
	res := claudeAgent{}.Parse(`{"result":"VERDICT: approve"}`)
	if !res.Found || res.IsError || res.Text != "VERDICT: approve" {
		t.Errorf("got %+v", res)
	}

	errRes := claudeAgent{}.Parse(`{"result":"oops","is_error":true}`)
	if !errRes.Found || !errRes.IsError || errRes.Text != "oops" {
		t.Errorf("got %+v", errRes)
	}
}

func TestClaudeParseBoundedLargeOutput(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(strings.Repeat("x", 100<<10))
	sb.WriteString("\n")
	sb.WriteString(rateLimitLine)
	sb.WriteString("\n")

	// Simulate a tail buffer that only keeps the last 64KiB, same bound
	// the runner's tailBuffer applies before calling Parse.
	full := sb.String()
	const max = 64 << 10
	tail := full
	if len(tail) > max {
		tail = tail[len(tail)-max:]
	}

	res := claudeAgent{}.Parse(tail)
	if !res.Found || res.APIStatus != 429 {
		t.Errorf("got %+v, want the rate-limit line to survive truncation", res)
	}
}

func TestFirstLineRunes(t *testing.T) {
	if got := firstLineRunes("hello\nworld", 200); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
	long := strings.Repeat("a", 250)
	if got := firstLineRunes(long, 200); len([]rune(got)) != 200 {
		t.Errorf("got length %d, want 200", len([]rune(got)))
	}
}

func TestClaudeArgv(t *testing.T) {
	a := claudeAgent{bin: "claude"}

	code := a.Argv(Invocation{Role: RoleCode, Model: "sonnet"})
	wantCode := []string{"claude", "-p", "--model", "sonnet", "--output-format", "json", "--permission-mode", "bypassPermissions"}
	if !equalStrings(code, wantCode) {
		t.Errorf("code Argv = %q, want %q", code, wantCode)
	}

	review := a.Argv(Invocation{Role: RoleReview, Model: "opus", ReadOnly: true})
	wantReview := []string{"claude", "-p", "--model", "opus", "--tools", "Read,Grep,Glob", "--disallowedTools", "mcp__*", "--output-format", "json"}
	if !equalStrings(review, wantReview) {
		t.Errorf("review Argv = %q, want %q", review, wantReview)
	}
}

func equalStrings(got, want []string) bool {
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
