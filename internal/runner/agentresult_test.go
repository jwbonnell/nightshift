package runner

import (
	"strings"
	"testing"
)

const rateLimitLine = `{"type":"result","subtype":"success","is_error":true,"api_error_status":429,"terminal_reason":"api_error","result":"You've hit your session limit · resets 7am (UTC)","num_turns":8}`

func TestParseAgentResultRateLimit(t *testing.T) {
	res := parseAgentResult(rateLimitLine)
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
}

func TestParseAgentResultBannerThenJSON(t *testing.T) {
	out := "Sandbox nightshift-abc123 started successfully\n" + rateLimitLine + "\n"
	res := parseAgentResult(out)
	if !res.Found || res.APIStatus != 429 {
		t.Errorf("got %+v", res)
	}
}

func TestParseAgentResultSuccess(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,"result":"done","num_turns":3}`
	res := parseAgentResult(line)
	if !res.Found {
		t.Fatal("expected Found")
	}
	if res.IsError {
		t.Error("expected !IsError")
	}
	if res.Message != "done" {
		t.Errorf("Message = %q, want %q", res.Message, "done")
	}
}

func TestParseAgentResultLastLineWins(t *testing.T) {
	first := `{"type":"result","is_error":false,"result":"first"}`
	second := `{"type":"result","is_error":true,"api_error_status":500,"result":"second"}`
	res := parseAgentResult(first + "\n" + second)
	if !res.IsError || res.APIStatus != 500 || res.Message != "second" {
		t.Errorf("got %+v, want the second line", res)
	}
}

func TestParseAgentResultGarbageOrEmpty(t *testing.T) {
	for _, out := range []string{"", "not json at all", "garbage\nmore garbage\n{not valid json}"} {
		if res := parseAgentResult(out); res.Found {
			t.Errorf("parseAgentResult(%q) = %+v, want Found=false", out, res)
		}
	}
}

func TestParseAgentResultBoundedLargeOutput(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(strings.Repeat("x", 100<<10))
	sb.WriteString("\n")
	sb.WriteString(rateLimitLine)
	sb.WriteString("\n")

	tail := &tailBuffer{max: 64 << 10}
	_, _ = tail.Write([]byte(sb.String()))
	if len(tail.buf) > 64<<10 {
		t.Fatalf("tailBuffer grew to %d bytes, want <= %d", len(tail.buf), 64<<10)
	}
	res := parseAgentResult(tail.String())
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

func TestTailBufferKeepsOnlyLastMaxBytes(t *testing.T) {
	tb := &tailBuffer{max: 10}
	n, err := tb.Write([]byte("0123456789abcdef"))
	if err != nil || n != 16 {
		t.Fatalf("Write returned (%d, %v), want (16, nil)", n, err)
	}
	if tb.String() != "6789abcdef" {
		t.Errorf("tailBuffer = %q, want last 10 bytes", tb.String())
	}
}
