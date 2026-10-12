package review

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/jwbonnell/nightshift/internal/forge"
)

// Target identifies one PR/MR to review.
type Target struct {
	Forge  forge.Kind
	Repo   string // GitLab: project path (or numeric id); GitHub: owner/name
	Number int
}

const usageLine = "usage: nightshift pr <url> | <owner/repo#n | group/project!n> | <repo> <n> | --pending"

// ParseArgs parses nightshift pr's positional target arguments, per P4:
// one URL, one "!"/"#" shorthand, or a "<repo> <n>" pair disambiguated by
// forgeFlag (or, absent that, whichever single forge is configured).
func ParseArgs(positional []string, forgeFlag string, s Settings) (Target, error) {
	switch len(positional) {
	case 1:
		arg := positional[0]
		if strings.Contains(arg, "://") {
			if forgeFlag != "" {
				return Target{}, fmt.Errorf("--forge only applies to <repo> <n>")
			}
			return parseChangeURL(arg, s)
		}
		if i := lastIndexAny(arg, "!#"); i >= 0 {
			if forgeFlag != "" {
				return Target{}, fmt.Errorf("--forge only applies to <repo> <n>")
			}
			return parseShorthand(arg, i, s)
		}
		return Target{}, errors.New(usageLine)
	case 2:
		return parseRepoNumber(positional[0], positional[1], forgeFlag, s)
	default:
		return Target{}, errors.New(usageLine)
	}
}

func lastIndexAny(s, chars string) int {
	return strings.LastIndexAny(s, chars)
}

// mrPathRe matches a GitLab merge-request URL path; group 1 is everything
// before "/-/merge_requests/", group 2 is the iid.
var mrPathRe = regexp.MustCompile(`^(.*)/-/merge_requests/(\d+)`)

// prPathRe matches a GitHub pull-request URL path (after stripping any
// Enterprise base path); group 1 is the owner, group 2 the repo name,
// group 3 the PR number.
var prPathRe = regexp.MustCompile(`^/([^/]+)/([^/]+)/pull/(\d+)`)

// parseChangeURL routes an absolute http(s) URL to whichever configured
// forge's web host it matches, gitlab first, then parses the forge-native
// path.
func parseChangeURL(raw string, s Settings) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return Target{}, fmt.Errorf("%q doesn't look like a PR/MR URL (need an absolute http(s) URL)", raw)
	}

	if s.Configured(forge.KindGitLab) {
		if base, err := url.Parse(s.GitLabURL); err == nil && base.Host != "" && strings.EqualFold(u.Host, base.Host) {
			return parseGitLabPath(raw, u, base)
		}
	}
	if s.Configured(forge.KindGitHub) {
		if base, err := url.Parse(s.GitHubURL); err == nil && base.Host != "" && strings.EqualFold(u.Host, base.Host) {
			return parseGitHubPath(raw, u, base)
		}
	}

	gitlabDisplay := "unset"
	if s.Configured(forge.KindGitLab) {
		if base, err := url.Parse(s.GitLabURL); err == nil {
			gitlabDisplay = base.Host
		}
	}
	githubDisplay := "unset"
	if s.Configured(forge.KindGitHub) {
		if base, err := url.Parse(s.GitHubURL); err == nil {
			githubDisplay = base.Host
		}
	}
	return Target{}, fmt.Errorf("%q doesn't match a configured forge (gitlab: %s, github: %s)", raw, gitlabDisplay, githubDisplay)
}

func parseGitLabPath(raw string, u, base *url.URL) (Target, error) {
	m := mrPathRe.FindStringSubmatch(u.Path)
	if m == nil {
		return Target{}, fmt.Errorf("%q has no /-/merge_requests/<n> — not an MR URL", raw)
	}
	projectPath := strings.Trim(m[1], "/")
	if bp := strings.Trim(base.Path, "/"); bp != "" {
		projectPath = strings.Trim(strings.TrimPrefix(projectPath, bp), "/")
	}
	if projectPath == "" {
		return Target{}, fmt.Errorf("%q has no project path before /-/merge_requests/", raw)
	}
	n, err := strconv.Atoi(m[2])
	if err != nil || n <= 0 {
		return Target{}, fmt.Errorf("%q has an invalid merge request iid", raw)
	}
	return Target{Forge: forge.KindGitLab, Repo: projectPath, Number: n}, nil
}

func parseGitHubPath(raw string, u, base *url.URL) (Target, error) {
	p := u.Path
	if bp := strings.Trim(base.Path, "/"); bp != "" {
		p = strings.TrimPrefix(p, "/"+bp)
	}
	m := prPathRe.FindStringSubmatch(p)
	if m == nil {
		return Target{}, fmt.Errorf("%q has no /<owner>/<repo>/pull/<n> — not a PR URL", raw)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return Target{}, fmt.Errorf("%q has an invalid pull request number", raw)
	}
	return Target{Forge: forge.KindGitHub, Repo: m[1] + "/" + m[2], Number: n}, nil
}

// parseShorthand parses "<path>!<n>" (GitLab) or "<owner>/<name>#<n>"
// (GitHub), split at the LAST "!"/"#" in arg (idx is that index).
func parseShorthand(arg string, idx int, s Settings) (Target, error) {
	repo := arg[:idx]
	numStr := arg[idx+1:]
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return Target{}, fmt.Errorf("number %q must be a positive integer", numStr)
	}

	if arg[idx] == '!' {
		if !s.Configured(forge.KindGitLab) {
			return Target{}, unconfiguredForgeErr(forge.KindGitLab, s)
		}
		return Target{Forge: forge.KindGitLab, Repo: repo, Number: n}, nil
	}

	if !isOwnerName(repo) {
		return Target{}, fmt.Errorf("github repo %q: want owner/name", repo)
	}
	if !s.Configured(forge.KindGitHub) {
		return Target{}, unconfiguredForgeErr(forge.KindGitHub, s)
	}
	return Target{Forge: forge.KindGitHub, Repo: repo, Number: n}, nil
}

// parseRepoNumber parses the two-argument "<repo> <n>" form, with
// forgeFlag (or, absent that, whichever single forge is configured)
// deciding which forge repo names.
func parseRepoNumber(repo, numStr, forgeFlag string, s Settings) (Target, error) {
	n, err := strconv.Atoi(numStr)
	if err != nil || n <= 0 {
		return Target{}, fmt.Errorf("number %q must be a positive integer", numStr)
	}

	var kind forge.Kind
	switch forgeFlag {
	case "":
		gitlabOK := s.Configured(forge.KindGitLab)
		githubOK := s.Configured(forge.KindGitHub)
		switch {
		case gitlabOK && githubOK:
			return Target{}, fmt.Errorf("<repo> <n> is ambiguous with both forges configured — add --forge gitlab|github")
		case gitlabOK:
			kind = forge.KindGitLab
		case githubOK:
			kind = forge.KindGitHub
		default:
			return Target{}, fmt.Errorf("no forge configured — set NIGHTSHIFT_GITLAB_URL + NIGHTSHIFT_GITLAB_TOKEN_FILE and/or NIGHTSHIFT_GITHUB_TOKEN_FILE")
		}
	case "gitlab":
		kind = forge.KindGitLab
	case "github":
		kind = forge.KindGitHub
	default:
		return Target{}, fmt.Errorf("--forge %q: want gitlab or github", forgeFlag)
	}

	if !s.Configured(kind) {
		return Target{}, unconfiguredForgeErr(kind, s)
	}
	return Target{Forge: kind, Repo: repo, Number: n}, nil
}

func isOwnerName(s string) bool {
	owner, name, found := strings.Cut(s, "/")
	return found && owner != "" && name != "" && !strings.Contains(name, "/")
}

func unconfiguredForgeErr(kind forge.Kind, s Settings) error {
	if kind == forge.KindGitLab {
		if s.GitLabTokenErr != nil {
			return s.GitLabTokenErr
		}
		return fmt.Errorf("gitlab is not configured (set NIGHTSHIFT_GITLAB_URL and NIGHTSHIFT_GITLAB_TOKEN_FILE)")
	}
	if s.GitHubTokenErr != nil {
		return s.GitHubTokenErr
	}
	return fmt.Errorf("github is not configured (set NIGHTSHIFT_GITHUB_TOKEN_FILE)")
}

// Slug derives a note filename stem from a forge-native repo path and
// change number: every rune outside [a-zA-Z0-9._-] folds to '-', then
// "-<suffix><n>" is appended (suffix is "mr" or "pr", forge.Client's
// SlugSuffix()). Slug("team/app", 12, "mr") == "team-app-mr12".
func Slug(path string, n int, suffix string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	fmt.Fprintf(&b, "-%s%d", suffix, n)
	return b.String()
}
