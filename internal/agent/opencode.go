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

// transientRe matches the signals that mean "try again later". It is only
// ever run over an error event's message or over a raw (non-JSON) stderr
// line, never over text or tool output inside JSON events.
var transientRe = regexp.MustCompile(`(?i)\b(429|5\d\d)\b|rate.?limit|quota|overloaded`)

// transientStatus reports whether an HTTP status is worth retrying.
func transientStatus(s int) bool { return s == 429 || (s >= 500 && s <= 599) }

// opencodeEvent is the subset of opencode's newline-delimited JSON event
// schema Parse understands. The schema isn't formally documented, so
// anything with an unrecognized "type" is ignored by the switch in Parse.
type opencodeEvent struct {
	Type string `json:"type"`
	Part struct {
		Text *string  `json:"text"`
		Cost *float64 `json:"cost"`
	} `json:"part"`
	Status  int    `json:"status"`
	Message string `json:"message"`
}

// Parse reads out line by line. Text collects "text" events' part.text
// seen after the last "tool_use" event; CostUSD sums "step_finish" events'
// part.cost. Errors come only from error signals: an "error" event, or a
// raw line (not JSON) that matches transientRe. Text and tool output inside
// JSON events are never matched. Transient is derived from the Result, so
// the transient evidence is kept where Transient can see it: APIStatus is
// the first transient error's status (else the first nonzero status), and
// Message is the chosen error's own message (the first transient error,
// else the first error) even when there is text, so the runner logs say
// what went wrong and Transient can match transientRe against it.
func (opencodeAgent) Parse(out string) Result {
	var res Result
	var texts []string
	var costSum float64
	var costKnown bool

	type errSig struct {
		msg       string
		status    int
		transient bool
	}
	var errs []errSig

	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev opencodeEvent
		if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
			if transientRe.MatchString(line) {
				errs = append(errs, errSig{firstLineRunes(line, 200), 0, true})
			}
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
			errs = append(errs, errSig{
				msg:       firstLineRunes(ev.Message, 200),
				status:    ev.Status,
				transient: transientStatus(ev.Status) || transientRe.MatchString(ev.Message),
			})
		}
	}

	res.Text = strings.Join(texts, "\n")
	res.Message = firstLineRunes(res.Text, 200)
	res.CostUSD = costSum
	res.CostKnown = costKnown

	if len(errs) > 0 {
		res.IsError = true
		chosen := errs[0]
		for _, e := range errs {
			if e.transient {
				chosen = e
				break
			}
		}
		for _, e := range errs {
			if e.status != 0 && (e == chosen || !chosen.transient) {
				res.APIStatus = e.status
				break
			}
		}
		if chosen.msg != "" {
			res.Message = chosen.msg
		}
	}
	return res
}

// RateLimited is always false for opencode: providers don't report a
// reset time consistently, so opencode never writes the usage-limit
// marker the way claude's session-limit message does.
func (opencodeAgent) RateLimited(Result) bool { return false }

// Transient reports whether r is an error worth retrying: status 429 or
// 5xx, or a rate-limit/quota/overload message. Parse stores the transient
// error's status and message in r for exactly this check.
func (opencodeAgent) Transient(r Result) bool {
	return r.IsError && (transientStatus(r.APIStatus) || transientRe.MatchString(r.Message))
}
