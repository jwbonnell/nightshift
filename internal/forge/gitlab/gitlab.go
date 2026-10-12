// Package gitlab implements internal/forge.Client against a self-hosted
// GitLab's REST API (v4). Every method issues a single GET (or, for
// ListReviewRequests, a sequence of GETs, one per page).
package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/jwbonnell/nightshift/internal/forge"
)

// maxPages bounds ListReviewRequests pagination (var, not const, so tests
// can shrink it rather than generating 5000 fake MRs).
var maxPages = 50

const perPage = 100

// Client talks to one self-hosted GitLab instance.
type Client struct {
	baseURL string // web URL, trimmed of trailing slash; API base is baseURL+"/api/v4"
	token   string
	host    string
	http    *http.Client
}

var _ forge.Client = (*Client)(nil)

// New builds a Client for the GitLab instance at baseURL (e.g.
// "https://gitlab.example.com"), authenticating with token.
func New(baseURL, token string) *Client {
	base := strings.TrimRight(baseURL, "/")
	host := ""
	if u, err := url.Parse(base); err == nil {
		host = u.Host
	}
	return &Client{
		baseURL: base,
		token:   token,
		host:    host,
		http:    forge.NewHTTPClient(host, forge.KindGitLab.URLSetting()),
	}
}

func (c *Client) Kind() forge.Kind    { return forge.KindGitLab }
func (c *Client) WebHost() string     { return c.host }
func (c *Client) GitAuthUser() string { return "oauth2" }
func (c *Client) SlugSuffix() string  { return "mr" }

func (c *Client) Label(repoPath string, n int) string {
	return fmt.Sprintf("%s!%d", repoPath, n)
}

func (c *Client) HeadRefspec(n int) string {
	return fmt.Sprintf("+refs/merge-requests/%d/head:refs/cr/%d", n, n)
}

func (c *Client) requireConfigured() error {
	if c.baseURL == "" || c.token == "" {
		return fmt.Errorf("gitlab not configured (set %s and a token)", forge.KindGitLab.URLSetting())
	}
	return nil
}

func (c *Client) header() map[string]string {
	return map[string]string{"PRIVATE-TOKEN": c.token}
}

func (c *Client) apiURL(path string) string {
	return c.baseURL + "/api/v4" + path
}

type user struct {
	Username string `json:"username"`
}

func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	if err := c.requireConfigured(); err != nil {
		return "", err
	}
	var u user
	if err := forge.GetJSON(ctx, c.http, forge.KindGitLab, c.apiURL("/user"), c.header(), &u); err != nil {
		return "", err
	}
	return u.Username, nil
}

type mr struct {
	IID          int    `json:"iid"`
	ProjectID    int    `json:"project_id"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Description  string `json:"description"`
	SHA          string `json:"sha"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	CreatedAt    string `json:"created_at"`
	WebURL       string `json:"web_url"`
	Author       struct {
		Username string `json:"username"`
	} `json:"author"`
	DiffRefs struct {
		BaseSHA  string `json:"base_sha"`
		HeadSHA  string `json:"head_sha"`
		StartSHA string `json:"start_sha"`
	} `json:"diff_refs"`
	Draft          bool `json:"draft"`
	WorkInProgress bool `json:"work_in_progress"`
	References     struct {
		Full string `json:"full"`
	} `json:"references"`
}

func (m mr) isDraft() bool { return m.Draft || m.WorkInProgress }

func normalizeState(s string) string {
	if s == "opened" {
		return "open"
	}
	return s
}

func (m mr) toChange() forge.Change {
	return forge.Change{
		Number:       m.IID,
		Title:        m.Title,
		State:        normalizeState(m.State),
		Draft:        m.isDraft(),
		Description:  m.Description,
		HeadSHA:      m.SHA,
		DiffBaseSHA:  m.DiffRefs.BaseSHA,
		TargetBranch: m.TargetBranch,
		Author:       m.Author.Username,
		WebURL:       m.WebURL,
		CreatedAt:    m.CreatedAt,
	}
}

// listedLabel mirrors the reference: the project path before the LAST "!",
// falling back to the numeric project id when references.full is empty.
func (m mr) listedLabel() string {
	if m.References.Full != "" {
		if i := strings.LastIndex(m.References.Full, "!"); i >= 0 {
			return m.References.Full[:i]
		}
		return m.References.Full
	}
	return strconv.Itoa(m.ProjectID)
}

func (c *Client) Get(ctx context.Context, repo string, n int) (forge.Change, error) {
	if err := c.requireConfigured(); err != nil {
		return forge.Change{}, err
	}
	var m mr
	u := c.apiURL(fmt.Sprintf("/projects/%s/merge_requests/%d", url.PathEscape(repo), n))
	if err := forge.GetJSON(ctx, c.http, forge.KindGitLab, u, c.header(), &m); err != nil {
		return forge.Change{}, err
	}
	return m.toChange(), nil
}

type project struct {
	ID                int64  `json:"id"`
	PathWithNamespace string `json:"path_with_namespace"`
	HTTPURLToRepo     string `json:"http_url_to_repo"`
}

func (c *Client) GetRepo(ctx context.Context, repo string) (forge.Repo, error) {
	if err := c.requireConfigured(); err != nil {
		return forge.Repo{}, err
	}
	var p project
	u := c.apiURL(fmt.Sprintf("/projects/%s", url.PathEscape(repo)))
	if err := forge.GetJSON(ctx, c.http, forge.KindGitLab, u, c.header(), &p); err != nil {
		return forge.Repo{}, err
	}
	return forge.Repo{ID: p.ID, Path: p.PathWithNamespace, CloneURL: p.HTTPURLToRepo}, nil
}

func (c *Client) ListReviewRequests(ctx context.Context, username string) ([]forge.Listed, error) {
	if err := c.requireConfigured(); err != nil {
		return nil, err
	}
	var all []forge.Listed
	for page := 1; page <= maxPages; page++ {
		u := c.apiURL(fmt.Sprintf("/merge_requests?scope=all&state=opened&reviewer_username=%s&order_by=created_at&sort=asc&per_page=%d&page=%d",
			url.QueryEscape(username), perPage, page))
		var batch []mr
		if err := forge.GetJSON(ctx, c.http, forge.KindGitLab, u, c.header(), &batch); err != nil {
			return nil, err
		}
		for _, m := range batch {
			all = append(all, forge.Listed{
				RepoRef: strconv.Itoa(m.ProjectID),
				Label:   m.listedLabel(),
				Number:  m.IID,
				Title:   m.Title,
				Draft:   m.isDraft(),
				State:   normalizeState(m.State),
				HeadSHA: m.SHA,
			})
		}
		if len(batch) < perPage {
			return all, nil
		}
		if page == maxPages {
			fmt.Fprintf(os.Stderr, "nightshift pr: %s: listing stopped at %d items (page cap) — some requests not shown\n", forge.KindGitLab, len(all))
		}
	}
	return all, nil
}
