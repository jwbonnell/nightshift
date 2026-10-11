package agent

import (
	"encoding/json"
	"regexp"
	"strings"
)

// opencodeAgent runs opencode headlessly via `opencode run`, the route to
// every provider besides a Claude subscription: other model vendors,
// open-weight and self-hosted models, or Anthropic by API key.
type opencodeAgent struct {
	bin    string
	config string // compacted OPENCODE_CONFIG_CONTENT JSON, or "" if unset
}

func (opencodeAgent) Name() string { return "opencode" }

func (opencodeAgent) SbxAgent() string { return "opencode" }

// codePermission and reviewPermission are opencode's OPENCODE_PERMISSION
// JSON for, respectively, the plan/code roles and a read-only reviewer.
// Both are hand-written (not json.Marshal'd) so the key order matches the
// spec exactly, byte for byte. webfetch is "allow" in codePermission
// because today's claude coding argv runs with --permission-mode
// bypassPermissions, which allows web fetch too.
const (
	codePermission   = `{"edit":"allow","bash":{"*":"allow","git push*":"deny","git remote*":"deny","git config*":"deny"},"webfetch":"allow","external_directory":"deny"}`
	reviewPermission = `{"edit":"deny","bash":"deny","webfetch":"deny","websearch":"deny","task":"deny","external_directory":"deny"}`
)

// Argv returns `env OPENCODE_PERMISSION=... [OPENCODE_CONFIG_CONTENT=...]
// <bin> run --format json --auto -m <provider/model>`. The prompt never
// appears here: it goes on stdin, same as claude.
func (a opencodeAgent) Argv(inv Invocation) []string {
	perm := codePermission
	if inv.ReadOnly {
		perm = reviewPermission
	}
	argv := []string{"env", "OPENCODE_PERMISSION=" + perm}
	if a.config != "" {
		argv = append(argv, "OPENCODE_CONFIG_CONTENT="+a.config)
	}
	return append(argv, a.bin, "run", "--format", "json", "--auto", "-m", inv.Model)
}

// transientRe matches the signals that mean "try again later", whether
// they arrive as a structured error event or as a raw stderr line
// opencode never wrapped in JSON.
var transientRe = regexp.MustCompile(`(?i)\b(429|5\d\d)\b|rate.?limit|quota|overloaded`)

// opencodeEvent is the subset of opencode's newline-delimited JSON event
// schema Parse understands. The schema isn't formally documented, so
// anything with an unrecognized "type" (or that isn't valid JSON at all,
// e.g. a raw stderr line) is ignored by the switch in Parse.
type opencodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Text *string  `json:"text"`
		Cost *float64 `json:"cost"`
	} `json:"part"`
	Status int `json:"status"`
}

// Parse reads out line by line. Text collects "text" events' part.text
// seen after the last "tool_use" event (an earlier text, followed by a
// tool call and then more text, keeps only the later text); CostUSD sums
// "step_finish" events' part.cost; an "error" event sets IsError (and
// APIStatus, if it carries a numeric status). Found is true once any
// known event type has been seen. Separately, since providers don't
// report errors consistently, a transientRe match anywhere in the raw
// output (including non-JSON stderr lines) also sets IsError, so
// Transient — which only sees the Result, not the raw text — can still
// detect it.
func (opencodeAgent) Parse(out string) Result {
	var res Result
	var texts []string
	var costSum float64
	var costKnown bool

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var ev opencodeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "text":
			res.Found = true
			if ev.Part.Text != nil {
				texts = append(texts, *ev.Part.Text)
			}
		case "tool_use":
			res.Found = true
			texts = nil
		case "step_finish":
			res.Found = true
			if ev.Part.Cost != nil {
				costSum += *ev.Part.Cost
				costKnown = true
			}
		case "error":
			res.Found = true
			res.IsError = true
			if ev.Status != 0 {
				res.APIStatus = ev.Status
			}
		}
	}

	if transientRe.MatchString(out) {
		res.IsError = true
	}

	res.Text = strings.Join(texts, "\n")
	res.Message = firstLineRunes(res.Text, 200)
	res.CostUSD = costSum
	res.CostKnown = costKnown
	return res
}

// RateLimited is always false for opencode: providers don't report a
// reset time consistently, so opencode never writes the usage-limit
// marker the way claude's session-limit message does.
func (opencodeAgent) RateLimited(Result) bool { return false }

// Transient reports whether r reflects a condition worth retrying:
// either a structured "error" event or a raw stderr line matching
// transientRe, both of which Parse folds into IsError.
func (opencodeAgent) Transient(r Result) bool { return r.IsError }
