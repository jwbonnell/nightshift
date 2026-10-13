// Package agent abstracts the model that runs a nightshift role (plan,
// code, review) behind one small interface, so every place nightshift
// runs a model — the planner, the coding agent, and the review/pr
// reviewers — goes through the same selector syntax and the same Agent
// interface. std-lib only: see TestPackageBoundaries.
package agent

import (
	"fmt"
	"regexp"
	"strings"
)

// Selector identifies which Agent implementation runs a role and which
// model it runs. ParseSelector is the only place that interprets the
// strings roles accept from frontmatter, env, config and flags.
type Selector struct {
	Agent string // "claude" or "opencode"
	Model string // claude: a bare model name; opencode: "<provider>/<model>"
}

// ParseSelector parses one role's model selector.
//
// A value with no "/" is a bare Claude model, so "opus" is the same as
// "claude/opus". "claude/<model>" passes <model> to --model unchanged.
// "opencode/<provider>/<model>" requires both parts non-empty; <model>
// may itself contain "/" or ":".
func ParseSelector(s string) (Selector, error) {
	if s == "" {
		return Selector{}, fmt.Errorf("empty agent selector")
	}
	head, rest, found := strings.Cut(s, "/")
	if !found {
		return Selector{Agent: "claude", Model: s}, nil
	}
	switch head {
	case "claude":
		return Selector{Agent: "claude", Model: rest}, nil
	case "opencode":
		provider, model, found := strings.Cut(rest, "/")
		if !found || provider == "" || model == "" {
			return Selector{}, fmt.Errorf("%q: opencode needs <provider>/<model>", s)
		}
		return Selector{Agent: "opencode", Model: provider + "/" + model}, nil
	default:
		return Selector{}, fmt.Errorf("unknown agent %q in %q (want claude/<model> or opencode/<provider>/<model>)", head, s)
	}
}

// String gives the canonical "agent/model" form.
func (sel Selector) String() string {
	return sel.Agent + "/" + sel.Model
}

// Display gives the bare model for claude, and String() otherwise. Every
// place nightshift prints or records a model uses Display(), so default
// (all-claude) output is unchanged by selectors.
func (sel Selector) Display() string {
	if sel.Agent == "claude" {
		return sel.Model
	}
	return sel.String()
}

// ProviderHosts returns the extra hosts a sandbox running sel's model
// must reach beyond sbx's baseline allowlist. Only OpenCode Zen
// (opencode/opencode/<model>) needs one today: opencode.ai.
func ProviderHosts(sel Selector) []string {
	if sel.Agent != "opencode" {
		return nil
	}
	provider, _, _ := strings.Cut(sel.Model, "/")
	if provider != "opencode" {
		return nil
	}
	return []string{"opencode.ai"}
}

var egressHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)

// ValidEgressHost reports whether h is an acceptable extra-egress entry:
// a bare host, or host:port.
func ValidEgressHost(h string) bool {
	return egressHostRe.MatchString(h)
}
