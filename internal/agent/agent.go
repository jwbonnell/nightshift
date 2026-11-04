package agent

import "fmt"

// Role is which part of a nightshift run an Invocation is for.
type Role string

const (
	RolePlan   Role = "plan"
	RoleCode   Role = "code"
	RoleReview Role = "review"
)

// Invocation is everything an Agent needs to build one headless call's
// argv, independent of how it's actually transported (sbx exec, etc).
type Invocation struct {
	Role     Role
	Model    string
	ReadOnly bool // reviewers: read and search tools only
	MaxTurns int  // 0 = default; claude only

	// Hardened, claude read-only only: the pinned nightshift pr argv
	// (carried over from the original mr-review tool), instead of nightshift
	// review's own read-only argv. Ignored by opencode and by a non-
	// read-only invocation.
	Hardened bool
}

// Result is the information callers need from an agent's raw output,
// whatever that agent's own wire format looks like.
type Result struct {
	Found, IsError bool
	APIStatus      int
	Subtype        string // claude's "subtype" field; "" for opencode or when absent
	Message        string // first line of the final text, cut to 200 chars
	Text           string // the full final answer text
	CostUSD        float64
	CostKnown      bool
	transient      bool // set by opencode's Parse only
}

// Agent is the one interface every place nightshift runs a model goes
// through: the planner, the coding agent, and the review/pr reviewers.
type Agent interface {
	Name() string                 // "claude" | "opencode"
	SbxAgent() string             // agent argument to `sbx create`
	Argv(inv Invocation) []string // never contains the prompt
	Parse(out string) Result
	RateLimited(r Result) bool
	Transient(r Result) bool
}

// Settings configures the binaries and config each Agent implementation
// needs. Fields unused by a given Agent are ignored.
type Settings struct {
	ClaudeBin      string
	OpencodeBin    string
	OpencodeConfig string // compacted JSON content, or "" if unset
}

// New builds the Agent implementation sel.Agent names.
func New(sel Selector, s Settings) (Agent, error) {
	switch sel.Agent {
	case "claude":
		bin := s.ClaudeBin
		if bin == "" {
			bin = "claude"
		}
		return claudeAgent{bin: bin}, nil
	case "opencode":
		bin := s.OpencodeBin
		if bin == "" {
			bin = "opencode"
		}
		return opencodeAgent{bin: bin, config: s.OpencodeConfig}, nil
	default:
		return nil, fmt.Errorf("unknown agent %q", sel.Agent)
	}
}
