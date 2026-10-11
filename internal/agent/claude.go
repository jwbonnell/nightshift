package agent

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// claudeAgent runs Claude Code headlessly via `claude -p`, the default
// nightshift agent and the only one allowed to use Claude subscription
// credentials.
type claudeAgent struct {
	bin string
}

func (claudeAgent) Name() string { return "claude" }

func (claudeAgent) SbxAgent() string { return "claude" }

// Argv returns today's exact argv for each role: a coding/planning
// invocation runs with bypassPermissions, a read-only one (reviewers)
// with a fixed read/search toolset and no permission-bypass flag.
func (a claudeAgent) Argv(inv Invocation) []string {
	argv := []string{a.bin, "-p", "--model", inv.Model}
	if inv.ReadOnly {
		argv = append(argv, "--tools", "Read,Grep,Glob", "--disallowedTools", "mcp__*", "--output-format", "json")
	} else {
		argv = append(argv, "--output-format", "json", "--permission-mode", "bypassPermissions")
	}
	if inv.MaxTurns > 0 {
		argv = append(argv, "--max-turns", strconv.Itoa(inv.MaxTurns))
	}
	return argv
}

// rateLimitRe matches Claude's session-limit result message, e.g. "You've
// hit your session limit · resets 7am (UTC)".
var rateLimitRe = regexp.MustCompile(`(?i)hit your .*limit`)

// resultLine is the subset of claude's `--output-format json` result
// object Parse cares about. Type and Result are pointers so a line with
// neither (e.g. a bare reviewer reply, which the real `claude` CLI never
// omits "type" from but the existing reviewer tests' fakes do) can still
// be told apart from one that's genuinely missing a result.
type resultLine struct {
	Type           *string  `json:"type"`
	IsError        bool     `json:"is_error"`
	APIErrorStatus int      `json:"api_error_status"`
	Result         *string  `json:"result"`
	CostUSD        *float64 `json:"total_cost_usd"`
}

// Parse scans out from the end for the last line that parses as a JSON
// object carrying a result: either "type":"result" (the real claude CLI),
// or, leniently, any object with no "type" field but a "result" key
// present. Non-JSON lines (sbx banners, etc.) are ignored. Message is the
// first line of the result text, cut to 200 runes; Text is the full
// field.
func (claudeAgent) Parse(out string) Result {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var rl resultLine
		if err := json.Unmarshal([]byte(line), &rl); err != nil {
			continue
		}
		isResult := (rl.Type != nil && *rl.Type == "result") || (rl.Type == nil && rl.Result != nil)
		if !isResult || rl.Result == nil {
			continue
		}
		res := Result{
			Found:     true,
			IsError:   rl.IsError,
			APIStatus: rl.APIErrorStatus,
			Message:   firstLineRunes(*rl.Result, 200),
			Text:      *rl.Result,
		}
		if rl.CostUSD != nil {
			res.CostUSD = *rl.CostUSD
			res.CostKnown = true
		}
		return res
	}
	return Result{}
}

// RateLimited reports whether r represents an agent call cut off by
// Claude's usage limit: an HTTP 429, or a result message naming the limit
// directly.
func (claudeAgent) RateLimited(r Result) bool {
	return r.APIStatus == 429 || rateLimitRe.MatchString(r.Message)
}

// Transient is always false for claude: the Ralph loop treats a timed-out
// or errored iteration the same as any other failed one (retried, or
// counted against the stall/cap limits), with no agent-level distinction.
func (claudeAgent) Transient(Result) bool { return false }

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
