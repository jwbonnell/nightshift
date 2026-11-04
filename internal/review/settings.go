// Package review implements `nightshift pr`: a read-only, sandboxed
// pre-review of one GitHub pull request or GitLab merge request, or every
// open one awaiting the user's review. It is the forge-generalized
// (P1/P2) successor of pensieve-mr-review. Its original design doc isn't
// published here; docs/pr.md describes the behavior, and SPEC.md's
// "Add `nightshift pr`" section lists every deliberate deviation from the
// original.
package review

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/usagelimit"
)

// FlagValues carries nightshift pr's CLI flag values into ResolveSettings.
// A zero value for a given field means "not set on the command line" and
// falls through to env/config/default (P3).
type FlagValues struct {
	Model    string
	MaxTurns int
	Timeout  string
	MaxLines int
}

// Settings is nightshift pr's fully resolved configuration (P3).
type Settings struct {
	Home string

	Model    string // the raw selector string, as resolved
	ModelSrc string // flag | env | config | default
	Selector agent.Selector

	MaxTurns int
	Timeout  time.Duration
	MaxLines int

	PromptFile string // raw NIGHTSHIFT_PR_PROMPT; "" if unset
	Mirror     string // expanded "~/"; "" = off
	Egress     []string

	SbxBin         string
	ClaudeBin      string
	OpencodeBin    string
	OpencodeConfig string // compacted JSON content; "" if unset or not needed

	GitLabURL      string
	GitLabToken    string
	GitLabTokenErr error
	GitHubURL      string
	GitHubToken    string
	GitHubTokenErr error

	Notify string // "on" | "off"

	LimitFallback time.Duration

	Warnings []string // token-file mode warnings; the caller prints each once
}

// Configured reports whether a forge has what it needs to be used: GitLab
// additionally needs a non-empty URL. A forge whose token resolution
// failed (GitLabTokenErr/GitHubTokenErr != nil) never counts as
// configured.
func (s Settings) Configured(k forge.Kind) bool {
	switch k {
	case forge.KindGitLab:
		return s.GitLabTokenErr == nil && s.GitLabToken != "" && s.GitLabURL != ""
	case forge.KindGitHub:
		return s.GitHubTokenErr == nil && s.GitHubToken != ""
	default:
		return false
	}
}

// pick applies flag > env > config > default, same precedence as the
// reference's own pick helper, and reports which tier won.
func pick(flagV string, envKey string, getenv func(string) string, cfg map[string]string, def string) (val, src string) {
	if flagV != "" {
		return flagV, "flag"
	}
	if v := getenv(envKey); v != "" {
		return v, "env"
	}
	if v := cfg[envKey]; v != "" {
		return v, "config"
	}
	return def, "default"
}

// intOrEmpty renders n as a decimal string, or "" for 0 ("not set on the
// command line").
func intOrEmpty(n int) string {
	if n == 0 {
		return ""
	}
	return strconv.Itoa(n)
}

var egressHostRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$`)

// resolveEgress builds the review sandbox's egress allow-list (P12.3):
// opencode.ai for an OpenCode Zen model, then NIGHTSHIFT_PR_EGRESS's
// entries, deduplicated in first-seen order.
func resolveEgress(sel agent.Selector, getenv func(string) string, cfg map[string]string) ([]string, error) {
	var hosts []string
	if sel.Agent == "opencode" {
		provider, _, _ := strings.Cut(sel.Model, "/")
		if provider == "opencode" {
			hosts = append(hosts, "opencode.ai")
		}
	}

	raw := getenv("NIGHTSHIFT_PR_EGRESS")
	if raw == "" {
		raw = cfg["NIGHTSHIFT_PR_EGRESS"]
	}
	for _, part := range strings.Split(raw, ",") {
		h := strings.TrimSpace(part)
		if h == "" {
			continue
		}
		if !egressHostRe.MatchString(h) {
			return nil, fmt.Errorf("NIGHTSHIFT_PR_EGRESS entry %q: want a host or host:port", h)
		}
		hosts = append(hosts, h)
	}

	seen := make(map[string]bool, len(hosts))
	out := make([]string, 0, len(hosts))
	for _, h := range hosts {
		if seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
	}
	return out, nil
}

// ResolveSettings resolves nightshift pr's configuration per P3: flag >
// env > config file > default, with the config file loaded from
// homeDir's own config path. Token resolution failures are carried in
// GitLabTokenErr/GitHubTokenErr rather than failing the whole call,
// because a token is only needed once a forge is actually used.
func ResolveSettings(flags FlagValues, homeDir string, getenv func(string) string) (Settings, error) {
	cfg, err := home.ParseConfig(home.ConfigPath(homeDir))
	if err != nil {
		return Settings{}, err
	}

	modelRaw, modelSrc := pick(flags.Model, "NIGHTSHIFT_PR_MODEL", getenv, cfg, "sonnet")
	sel, err := agent.ParseSelector(modelRaw)
	if err != nil {
		return Settings{}, fmt.Errorf("NIGHTSHIFT_PR_MODEL (from %s): %w", modelSrc, err)
	}

	maxTurnsStr, _ := pick(intOrEmpty(flags.MaxTurns), "NIGHTSHIFT_PR_MAX_TURNS", getenv, cfg, "30")
	maxTurns, err := strconv.Atoi(maxTurnsStr)
	if err != nil || maxTurns <= 0 {
		return Settings{}, fmt.Errorf("max-turns %q: must be a positive integer", maxTurnsStr)
	}

	timeoutStr, _ := pick(flags.Timeout, "NIGHTSHIFT_PR_TIMEOUT", getenv, cfg, "15m")
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil || timeout <= 0 {
		return Settings{}, fmt.Errorf("timeout %q: must be a positive Go duration (e.g. 15m)", timeoutStr)
	}

	maxLinesStr, _ := pick(intOrEmpty(flags.MaxLines), "NIGHTSHIFT_PR_MAX_LINES", getenv, cfg, "3000")
	maxLines, err := strconv.Atoi(maxLinesStr)
	if err != nil || maxLines <= 0 {
		return Settings{}, fmt.Errorf("max-lines %q: must be a positive integer", maxLinesStr)
	}

	egress, err := resolveEgress(sel, getenv, cfg)
	if err != nil {
		return Settings{}, err
	}

	var opencodeCfg string
	if sel.Agent == "opencode" {
		cfgRaw, _ := pick("", "NIGHTSHIFT_OPENCODE_CONFIG", getenv, cfg, "")
		if cfgRaw != "" {
			expanded, err := home.ExpandTilde(cfgRaw)
			if err != nil {
				return Settings{}, fmt.Errorf("NIGHTSHIFT_OPENCODE_CONFIG %q: %w", cfgRaw, err)
			}
			compact, err := agent.LoadOpencodeConfig(expanded)
			if err != nil {
				return Settings{}, err
			}
			opencodeCfg = compact
		}
	}

	mirrorRaw, _ := pick("", "NIGHTSHIFT_PR_MIRROR", getenv, cfg, "")
	mirror, err := home.ExpandTilde(mirrorRaw)
	if err != nil {
		return Settings{}, err
	}

	promptFile, _ := pick("", "NIGHTSHIFT_PR_PROMPT", getenv, cfg, "")

	gitlabURL, _ := pick("", "NIGHTSHIFT_GITLAB_URL", getenv, cfg, "")
	githubURL, _ := pick("", "NIGHTSHIFT_GITHUB_URL", getenv, cfg, "https://github.com")

	var warnings []string
	gitlabToken, w, gitlabTokenErr := home.ResolveToken("NIGHTSHIFT_GITLAB_TOKEN", getenv, cfg)
	if w != "" {
		warnings = append(warnings, w)
	}
	githubToken, w, githubTokenErr := home.ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, cfg)
	if w != "" {
		warnings = append(warnings, w)
	}

	notify, _ := pick("", "NIGHTSHIFT_NOTIFY", getenv, cfg, "on")
	if notify != "on" && notify != "off" {
		return Settings{}, fmt.Errorf("NIGHTSHIFT_NOTIFY=%q: want on or off", notify)
	}

	limitFallback := usagelimit.DefaultFallback
	if raw, _ := pick("", "NIGHTSHIFT_LIMIT_FALLBACK", getenv, cfg, ""); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			return Settings{}, fmt.Errorf("NIGHTSHIFT_LIMIT_FALLBACK=%q: want a positive duration", raw)
		}
		limitFallback = d
	}

	sbxBin, _ := pick("", "NIGHTSHIFT_SBX_BIN", getenv, cfg, "sbx")
	claudeBin, _ := pick("", "NIGHTSHIFT_CLAUDE_BIN", getenv, cfg, "claude")
	opencodeBin, _ := pick("", "NIGHTSHIFT_OPENCODE_BIN", getenv, cfg, "opencode")

	return Settings{
		Home: homeDir,

		Model:    modelRaw,
		ModelSrc: modelSrc,
		Selector: sel,

		MaxTurns: maxTurns,
		Timeout:  timeout,
		MaxLines: maxLines,

		PromptFile: promptFile,
		Mirror:     mirror,
		Egress:     egress,

		SbxBin:         sbxBin,
		ClaudeBin:      claudeBin,
		OpencodeBin:    opencodeBin,
		OpencodeConfig: opencodeCfg,

		GitLabURL:      gitlabURL,
		GitLabToken:    gitlabToken,
		GitLabTokenErr: gitlabTokenErr,
		GitHubURL:      githubURL,
		GitHubToken:    githubToken,
		GitHubTokenErr: githubTokenErr,

		Notify: notify,

		LimitFallback: limitFallback,

		Warnings: warnings,
	}, nil
}
