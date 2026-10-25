package agent

import (
	"os"
	"path/filepath"
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
		t.Error("expected !Transient for a successful result")
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
	wantCode := []string{"claude", "-p", "--model", "sonnet", "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions"}
	if !equalStrings(code, wantCode) {
		t.Errorf("code Argv = %q, want %q", code, wantCode)
	}

	review := a.Argv(Invocation{Role: RoleReview, Model: "opus", ReadOnly: true})
	wantReview := []string{"claude", "-p", "--model", "opus", "--tools", "Read,Grep,Glob", "--disallowedTools", "mcp__*", "--output-format", "json"}
	if !equalStrings(review, wantReview) {
		t.Errorf("review Argv = %q, want %q", review, wantReview)
	}
}

// TestClaudeArgvHardened pins nightshift pr's argv (P12.2) byte for byte
// against docs/reference/pensieve-mr-review.md §4.6, distinct from
// nightshift review's own read-only argv (TestClaudeArgv, left unedited).
func TestClaudeArgvHardened(t *testing.T) {
	a := claudeAgent{bin: "claude"}

	got := a.Argv(Invocation{Role: RoleReview, Model: "sonnet", ReadOnly: true, MaxTurns: 30, Hardened: true})
	want := []string{
		"claude", "-p",
		"--output-format", "json",
		"--model", "sonnet",
		"--max-turns", "30",
		"--tools", "Read,Grep,Glob",
		"--permission-mode", "dontAsk",
		"--no-session-persistence",
		"--setting-sources", "user",
		"--disable-slash-commands",
	}
	if !equalStrings(got, want) {
		t.Errorf("hardened Argv = %q, want %q", got, want)
	}

	// Hardened without ReadOnly is ignored: a coding invocation stays the
	// ordinary bypassPermissions argv.
	code := a.Argv(Invocation{Role: RoleCode, Model: "sonnet", Hardened: true})
	wantCode := []string{"claude", "-p", "--model", "sonnet", "--output-format", "stream-json", "--verbose", "--permission-mode", "bypassPermissions"}
	if !equalStrings(code, wantCode) {
		t.Errorf("hardened-but-not-read-only Argv = %q, want %q", code, wantCode)
	}
}

// TestClaudeParseSubtype asserts Parse fills Result.Subtype from the
// wire "subtype" field (P8.5), empty when absent.
func TestClaudeParseSubtype(t *testing.T) {
	res := claudeAgent{}.Parse(`{"type":"result","subtype":"success","is_error":true,"result":"boom"}`)
	if res.Subtype != "success" {
		t.Errorf("Subtype = %q, want %q", res.Subtype, "success")
	}

	res2 := claudeAgent{}.Parse(`{"result":"no subtype here"}`)
	if res2.Subtype != "" {
		t.Errorf("Subtype = %q, want empty", res2.Subtype)
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

// TestClaudeTransient carries over nightshift-limit-wait's
// TestAgentResultTransient cases from internal/runner.
func TestClaudeTransient(t *testing.T) {
	cases := []struct {
		name string
		res  Result
		want bool
	}{
		{"529", Result{IsError: true, APIStatus: 529, Message: "Overloaded"}, true},
		{"503", Result{IsError: true, APIStatus: 503}, true},
		{"overloaded message, no status", Result{IsError: true, Message: "the API is overloaded, try again"}, true},
		{"429 is rate-limited, not transient", Result{IsError: true, APIStatus: 429, Message: "overloaded"}, false},
		{"rate-limit message, not transient", Result{IsError: true, Message: "You've hit your session limit · resets 7am (UTC)"}, false},
		{"plain failure", Result{IsError: true, APIStatus: 400, Message: "bad request"}, false},
		{"success", Result{IsError: false}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := (claudeAgent{}).Transient(c.res); got != c.want {
				t.Errorf("Transient() = %v, want %v", got, c.want)
			}
		})
	}
}

func readClaudeFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "claude", name))
	if err != nil {
		t.Fatalf("read testdata %s: %v", name, err)
	}
	return string(b)
}

func TestClaudeParseStreamJSON(t *testing.T) {
	res := claudeAgent{}.Parse(readClaudeFixture(t, "stream.jsonl"))
	if !res.Found || res.IsError || res.Text != "All done." || !res.CostKnown || res.CostUSD != 0.42 {
		t.Errorf("Parse = %+v", res)
	}
}

func TestClaudeStreamRateLimited(t *testing.T) {
	a := claudeAgent{}
	res := a.Parse(readClaudeFixture(t, "stream429.jsonl"))
	if !res.Found || !a.RateLimited(res) || a.Transient(res) {
		t.Errorf("429 stream: res=%+v rl=%v transient=%v", res, a.RateLimited(res), a.Transient(res))
	}
}

func TestClaudeStreamTransient(t *testing.T) {
	a := claudeAgent{}
	res := a.Parse(readClaudeFixture(t, "stream529.jsonl"))
	if !res.Found || a.RateLimited(res) || !a.Transient(res) {
		t.Errorf("529 stream: res=%+v rl=%v transient=%v", res, a.RateLimited(res), a.Transient(res))
	}
}

func TestClaudeStreamNoResult(t *testing.T) {
	if res := (claudeAgent{}).Parse(readClaudeFixture(t, "stream_noresult.jsonl")); res.Found {
		t.Errorf("Found = true for a stream with no result: %+v", res)
	}
}

func TestClaudeReviewArgvUnchanged(t *testing.T) {
	a := claudeAgent{bin: "claude"}
	got := a.Argv(Invocation{Role: RoleReview, Model: "opus", ReadOnly: true})
	want := []string{"claude", "-p", "--model", "opus", "--tools", "Read,Grep,Glob", "--disallowedTools", "mcp__*", "--output-format", "json"}
	if !equalStrings(got, want) {
		t.Errorf("read-only Argv = %q, want %q", got, want)
	}
	got = a.Argv(Invocation{Role: RoleReview, Model: "sonnet", ReadOnly: true, MaxTurns: 30, Hardened: true})
	want = []string{"claude", "-p", "--output-format", "json", "--model", "sonnet", "--max-turns", "30",
		"--tools", "Read,Grep,Glob", "--permission-mode", "dontAsk", "--no-session-persistence",
		"--setting-sources", "user", "--disable-slash-commands"}
	if !equalStrings(got, want) {
		t.Errorf("hardened Argv = %q, want %q", got, want)
	}
}
