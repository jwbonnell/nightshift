package runner

import (
	"encoding/json"
	"strings"
)

// agentResult is the information loop() needs from the agent's structured
// result line (one JSON object per `claude -p --output-format json`
// invocation, found among possible sbx banner lines and other noise).
type agentResult struct {
	Found     bool
	IsError   bool
	APIStatus int
	Message   string
}

// agentResultLine is the subset of the agent's JSON result object loop()
// cares about.
type agentResultLine struct {
	Type           string `json:"type"`
	IsError        bool   `json:"is_error"`
	APIErrorStatus int    `json:"api_error_status"`
	Result         string `json:"result"`
}

// parseAgentResult scans out from the end for the last line that parses as
// a JSON object with "type":"result". Non-JSON lines (sbx banners, etc.)
// are ignored. Message is the first line of that object's "result" field,
// cut to 200 runes.
func parseAgentResult(out string) agentResult {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var rl agentResultLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			continue
		}
		if rl.Type != "result" {
			continue
		}
		return agentResult{
			Found:     true,
			IsError:   rl.IsError,
			APIStatus: rl.APIErrorStatus,
			Message:   firstLineRunes(rl.Result, 200),
		}
	}
	return agentResult{}
}

// firstLineRunes returns s's first line, cut to at most n runes.
func firstLineRunes(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// tailBuffer is an io.Writer that keeps only the last max bytes written to
// it, so a long-running agent's output can be parsed for its final result
// line without holding the whole stream in memory.
type tailBuffer struct {
	max int
	buf []byte
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
