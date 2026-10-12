// Package github implements internal/forge.Client against github.com or a
// GitHub Enterprise server's REST API. Every method issues a single GET
// (or, for ListReviewRequests, a sequence of GETs, one per search page).
package github

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/jwbonnell/nightshift/internal/forge"
)

// maxPages bounds ListReviewRequests pagination (var, not const, so tests
// can shrink it rather than generating thousands of fake PRs). The search
// API itself stops at 1000 results (10 pages of 100).
var maxPages = 10

const perPage = 100

// Client talks to one GitHub host: github.com or a GitHub Enterprise
// server.
type Client struct {
	webHost string // host[:port] of the web URL; what PR URLs and clone URLs must match
	apiBase string // https://api.github.com, or <web URL>/api/v3 for Enterprise
	token   string
	http    *http.Client

	// Warn receives non-fatal warnings (e.g. the page-cap notice). nil
	// means io.Discard, never os.Stderr directly.
	Warn io.Writer
}

var _ forge.Client = (*Client)(nil)

// New builds a Client for the GitHub host at webURL (e.g.
// "https://github.com" or "https://ghe.example.com"), authenticating with
// token.
func New(webURL, token string) *Client {
	apiBase, webHost := deriveAPIBase(webURL)
	apiHost := "api.github.com"
	if !strings.EqualFold(webHost, "github.com") {
		apiHost = webHost
	}
	return &Client{
		webHost: webHost,
		apiBase: apiBase,
		token:   token,
		http:    forge.NewHTTPClient(apiHost, forge.KindGitHub.URLSetting()),
	}
}

// deriveAPIBase computes the API base URL and the web host from webURL:
// https://api.github.com when the web host is github.com, otherwise
// <web URL>/api/v3 (Enterprise).
func deriveAPIBase(webURL string) (apiBase, webHost string) {
	base := strings.TrimRight(webURL, "/")
	if u, err := url.Parse(base); err == nil {
		webHost = u.Host
	}
	if strings.EqualFold(webHost, "github.com") {
		return "https://api.github.com", webHost
	}
	return base + "/api/v3", webHost
}

func (c *Client) Kind() forge.Kind    { return forge.KindGitHub }
func (c *Client) WebHost() string     { return c.webHost }
func (c *Client) GitAuthUser() string { return "x-access-token" }
func (c *Client) SlugSuffix() string  { return "pr" }

func (c *Client) Label(repoPath string, n int) string {
	return fmt.Sprintf("%s#%d", repoPath, n)
}

func (c *Client) HeadRefspec(n int) string {
	return fmt.Sprintf("+refs/pull/%d/head:refs/cr/%d", n, n)
}

func (c *Client) requireConfigured() error {
	if c.token == "" {
		return fmt.Errorf("github not configured (set NIGHTSHIFT_GITHUB_TOKEN_FILE)")
	}
	return nil
}

func (c *Client) header() map[string]string {
	return map[string]string{
		"Authorization":        "Bearer " + c.token,
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
}

func (c *Client) apiURL(path string) string {
	return c.apiBase + path
}

func (c *Client) warn() io.Writer {
	if c.Warn == nil {
		return io.Discard
	}
	return c.Warn
}

// splitOwnerRepo validates repo is exactly "owner/name", both parts
// non-empty.
func splitOwnerRepo(repo string) (owner, name string, err error) {
	i := strings.Index(repo, "/")
	if i <= 0 || i != strings.LastIndex(repo, "/") || i == len(repo)-1 {
		return "", "", fmt.Errorf("github repo %q: want owner/name", repo)
	}
	return repo[:i], repo[i+1:], nil
}

type githubUser struct {
	Login string `json:"login"`
}

func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	if err := c.requireConfigured(); err != nil {
		return "", err
	}
	var u githubUser
	if err := forge.GetJSON(ctx, c.http, forge.KindGitHub, c.apiURL("/user"), c.header(), &u); err != nil {
		return "", err
	}
	return u.Login, nil
}

type pullRequest struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	State     string  `json:"state"`
	Body      string  `json:"body"`
	Draft     bool    `json:"draft"`
	CreatedAt string  `json:"created_at"`
	HTMLURL   string  `json:"html_url"`
	MergedAt  *string `json:"merged_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

// normalizedState maps GitHub's state to the forge-normalized one: open →
// open; closed with a non-null merged_at → merged; otherwise closed.
func (p pullRequest) normalizedState() string {
	if p.State == "open" {
		return "open"
	}
	if p.MergedAt != nil {
		return "merged"
	}
	return "closed"
}

func (p pullRequest) toChange() forge.Change {
	return forge.Change{
		Number:       p.Number,
		Title:        p.Title,
		State:        p.normalizedState(),
		Draft:        p.Draft,
		Description:  p.Body,
		HeadSHA:      p.Head.SHA,
		DiffBaseSHA:  "", // GitHub's base.sha is the base branch tip, not the merge base
		TargetBranch: p.Base.Ref,
		Author:       p.User.Login,
		WebURL:       p.HTMLURL,
		CreatedAt:    p.CreatedAt,
	}
}

func (c *Client) Get(ctx context.Context, repo string, n int) (forge.Change, error) {
	if err := c.requireConfigured(); err != nil {
		return forge.Change{}, err
	}
	owner, name, err := splitOwnerRepo(repo)
	if err != nil {
		return forge.Change{}, err
	}
	var p pullRequest
	u := c.apiURL(fmt.Sprintf("/repos/%s/%s/pulls/%d", url.PathEscape(owner), url.PathEscape(name), n))
	if err := forge.GetJSON(ctx, c.http, forge.KindGitHub, u, c.header(), &p); err != nil {
		return forge.Change{}, err
	}
	return p.toChange(), nil
}

type repoInfo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
	CloneURL string `json:"clone_url"`
}

func (c *Client) GetRepo(ctx context.Context, repo string) (forge.Repo, error) {
	if err := c.requireConfigured(); err != nil {
		return forge.Repo{}, err
	}
	owner, name, err := splitOwnerRepo(repo)
	if err != nil {
		return forge.Repo{}, err
	}
	var r repoInfo
	u := c.apiURL(fmt.Sprintf("/repos/%s/%s", url.PathEscape(owner), url.PathEscape(name)))
	if err := forge.GetJSON(ctx, c.http, forge.KindGitHub, u, c.header(), &r); err != nil {
		return forge.Repo{}, err
	}
	return forge.Repo{ID: r.ID, Path: r.FullName, CloneURL: r.CloneURL}, nil
}

type searchResult struct {
	Items []struct {
		Number        int    `json:"number"`
		Title         string `json:"title"`
		Draft         bool   `json:"draft"`
		RepositoryURL string `json:"repository_url"`
	} `json:"items"`
}

// repoRefFromURL takes the owner/name out of a repository_url
// (<api>/repos/owner/name), the part after the last "/repos/".
func repoRefFromURL(repositoryURL string) string {
	const marker = "/repos/"
	if i := strings.LastIndex(repositoryURL, marker); i >= 0 {
		return repositoryURL[i+len(marker):]
	}
	return repositoryURL
}

func (c *Client) ListReviewRequests(ctx context.Context, user string) ([]forge.Listed, error) {
	if err := c.requireConfigured(); err != nil {
		return nil, err
	}
	query := "is:pr is:open archived:false user-review-requested:" + user
	var all []forge.Listed
	for page := 1; page <= maxPages; page++ {
		u := c.apiURL(fmt.Sprintf("/search/issues?q=%s&sort=created&order=asc&per_page=%d&page=%d",
			url.QueryEscape(query), perPage, page))
		var res searchResult
		if err := forge.GetJSON(ctx, c.http, forge.KindGitHub, u, c.header(), &res); err != nil {
			return nil, err
		}
		for _, item := range res.Items {
			ref := repoRefFromURL(item.RepositoryURL)
			all = append(all, forge.Listed{
				RepoRef: ref,
				Label:   ref,
				Number:  item.Number,
				Title:   item.Title,
				Draft:   item.Draft,
				State:   "open",
			})
		}
		if len(res.Items) < perPage {
			return all, nil
		}
		if page == maxPages {
			fmt.Fprintf(c.warn(), "nightshift pr: %s: listing stopped at %d items (page cap) — some requests not shown\n", forge.KindGitHub, len(all))
		}
	}
	return all, nil
}
