// Package forge abstracts the two code-review hosts nightshift pr talks to
// (a self-hosted GitLab and github.com/GitHub Enterprise) behind one
// read-only Client interface. Every exported method issues GET requests
// only — see TestForgeReadOnly in internal/forge/gitlab and
// internal/forge/github.
package forge

import (
	"context"
	"fmt"
)

// Kind identifies which forge a Client talks to.
type Kind string

const (
	KindGitLab Kind = "gitlab"
	KindGitHub Kind = "github"
)

// Noun is the forge-native word for a change: "MR" for GitLab, "PR" for
// GitHub.
func (k Kind) Noun() string {
	if k == KindGitLab {
		return "MR"
	}
	return "PR"
}

// RepoNoun is the forge-native word for a repository: "project" for
// GitLab, "repo" for GitHub.
func (k Kind) RepoNoun() string {
	if k == KindGitLab {
		return "project"
	}
	return "repo"
}

// URLSetting is the env/config key that names this forge's base URL.
func (k Kind) URLSetting() string {
	if k == KindGitLab {
		return "NIGHTSHIFT_GITLAB_URL"
	}
	return "NIGHTSHIFT_GITHUB_URL"
}

// SlugSuffix is the forge-native note-slug suffix: "mr" for GitLab, "pr"
// for GitHub — the same value as a Client's own SlugSuffix(), available
// without constructing one (e.g. for --dry-run's client-less preview).
func (k Kind) SlugSuffix() string {
	if k == KindGitLab {
		return "mr"
	}
	return "pr"
}

// Label formats repoPath/n the forge-native way ("path!n" for GitLab,
// "path#n" for GitHub) — the same value as a Client's own Label(), without
// needing one.
func (k Kind) Label(repoPath string, n int) string {
	if k == KindGitLab {
		return fmt.Sprintf("%s!%d", repoPath, n)
	}
	return fmt.Sprintf("%s#%d", repoPath, n)
}

// Change is one pull/merge request, normalized across both forges.
type Change struct {
	Number       int
	Title        string
	State        string // normalized: "open" | "closed" | "merged" | "locked"
	Draft        bool
	Description  string
	HeadSHA      string
	DiffBaseSHA  string // GitLab diff_refs.base_sha; "" for GitHub (merge-base is used)
	TargetBranch string
	Author       string
	WebURL       string
	CreatedAt    string
}

// Repo is a forge repository, normalized across both forges.
type Repo struct {
	ID       int64
	Path     string // canonical: GitLab path_with_namespace, GitHub full_name
	CloneURL string // GitLab http_url_to_repo, GitHub clone_url
}

// Listed is one entry of a "review requested from me" listing.
type Listed struct {
	RepoRef string // what Get/GetRepo accept: GitLab numeric project id, GitHub full_name
	Label   string // display path: GitLab references.full minus "!n" (fallback id), GitHub full_name
	Number  int
	Title   string
	Draft   bool
	State   string // normalized
	HeadSHA string // "" when the listing doesn't carry it (GitHub search)
}

// Client is the read-only interface nightshift pr uses for both forges.
// Every exported method must issue GET requests only.
type Client interface {
	Kind() Kind
	WebHost() string // host[:port] that MR/PR URLs and clone URLs must match
	CurrentUser(ctx context.Context) (string, error)
	Get(ctx context.Context, repo string, n int) (Change, error)
	GetRepo(ctx context.Context, repo string) (Repo, error)
	ListReviewRequests(ctx context.Context, user string) ([]Listed, error) // oldest-created first
	HeadRefspec(n int) string                                              // fetch refspec for the change head into refs/cr/<n>
	GitAuthUser() string                                                   // "oauth2" (GitLab) / "x-access-token" (GitHub)
	Label(repoPath string, n int) string                                   // "path!n" / "path#n"
	SlugSuffix() string                                                    // "mr" / "pr"
}
