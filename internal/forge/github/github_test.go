package github_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/forge/github"
)

func TestGitHubClient(t *testing.T) {
	var methods []string
	var gotHeaders []http.Header
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	record := func(r *http.Request) {
		methods = append(methods, r.Method)
		gotHeaders = append(gotHeaders, r.Header.Clone())
	}

	mux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]string{"login": "alice"})
	})
	mux.HandleFunc("/api/v3/repos/o/r/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"number": 7, "title": "t", "state": "open"})
	})
	mux.HandleFunc("/api/v3/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"id": 9, "full_name": "o/r", "clone_url": "https://x/o/r.git"})
	})

	c := github.New(srv.URL, "tok")

	if _, err := c.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if _, err := c.Get(context.Background(), "o/r", 7); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.GetRepo(context.Background(), "o/r"); err != nil {
		t.Fatalf("GetRepo: %v", err)
	}

	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("method = %s, want GET", m)
		}
	}
	if len(methods) != 3 {
		t.Fatalf("got %d requests, want 3", len(methods))
	}
	for _, h := range gotHeaders {
		if got := h.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q, want Bearer tok", got)
		}
		if got := h.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept = %q, want application/vnd.github+json", got)
		}
		if got := h.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q, want 2022-11-28", got)
		}
	}

	if _, err := c.Get(context.Background(), "onlyowner", 1); err == nil {
		t.Error("Get with no slash: want error")
	} else if !strings.Contains(err.Error(), "want owner/name") {
		t.Errorf("err = %q, want it to mention owner/name", err)
	}
	if _, err := c.Get(context.Background(), "a/b/c", 1); err == nil {
		t.Error("Get with two slashes: want error")
	}
	if _, err := c.Get(context.Background(), "a/", 1); err == nil {
		t.Error("Get with empty name: want error")
	}
	if _, err := c.Get(context.Background(), "/b", 1); err == nil {
		t.Error("Get with empty owner: want error")
	}
}

func TestGitHubDecodesFields(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/v3/repos/team/app/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"number": 5, "title": "fix bug", "state": "open", "body": "desc", "draft": true,
			"created_at": "2026-01-01T00:00:00Z",
			"html_url":   "https://github.com/team/app/pull/5",
			"user":       map[string]string{"login": "bob"},
			"head":       map[string]string{"sha": "headsha"},
			"base":       map[string]string{"ref": "main"},
		})
	})

	c := github.New(srv.URL, "tok")
	ch, err := c.Get(context.Background(), "team/app", 5)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := forge.Change{
		Number: 5, Title: "fix bug", State: "open", Draft: true,
		Description: "desc", HeadSHA: "headsha", DiffBaseSHA: "",
		TargetBranch: "main", Author: "bob",
		WebURL:    "https://github.com/team/app/pull/5",
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	if ch != want {
		t.Errorf("Get = %+v, want %+v", ch, want)
	}
}

func TestGitHubStateMapping(t *testing.T) {
	for _, tc := range []struct {
		name     string
		state    string
		mergedAt any
		want     string
	}{
		{"open", "open", nil, "open"},
		{"merged", "closed", "2026-01-01T00:00:00Z", "merged"},
		{"closed", "closed", nil, "closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			srv := httptest.NewServer(mux)
			defer srv.Close()
			mux.HandleFunc("/api/v3/repos/o/r/pulls/1", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{
					"number": 1, "state": tc.state, "merged_at": tc.mergedAt,
				})
			})
			c := github.New(srv.URL, "tok")
			ch, err := c.Get(context.Background(), "o/r", 1)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if ch.State != tc.want {
				t.Errorf("state = %q, want %q", ch.State, tc.want)
			}
			if ch.DiffBaseSHA != "" {
				t.Errorf("DiffBaseSHA = %q, want empty", ch.DiffBaseSHA)
			}
		})
	}
}

func TestGitHubSearchPaginates(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var queries []string
	mux.HandleFunc("/api/v3/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		queries = append(queries, q.Get("q"))
		if q.Get("sort") != "created" || q.Get("order") != "asc" {
			t.Errorf("query = %v, want sort=created&order=asc", q)
		}
		page := q.Get("page")
		var items []map[string]any
		if page == "1" {
			for i := 0; i < 100; i++ {
				items = append(items, map[string]any{
					"number": i, "title": fmt.Sprintf("t%d", i), "draft": i == 0,
					"repository_url": "https://api.github.com/repos/team/app",
				})
			}
		} else if page == "2" {
			items = append(items, map[string]any{
				"number": 100, "title": "t100",
				"repository_url": "https://api.github.com/repos/team/app",
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	})

	c := github.New(srv.URL, "tok")
	listed, err := c.ListReviewRequests(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}
	if len(listed) != 101 {
		t.Fatalf("len(listed) = %d, want 101", len(listed))
	}
	if len(queries) != 2 {
		t.Fatalf("pages seen = %d, want 2", len(queries))
	}
	for _, q := range queries {
		if !strings.Contains(q, "user-review-requested:alice") || !strings.Contains(q, "is:open") || !strings.Contains(q, "archived:false") {
			t.Errorf("q = %q, want it to contain user-review-requested:alice, is:open, archived:false", q)
		}
	}
	if !listed[0].Draft {
		t.Error("listed[0].Draft = false, want true")
	}
	if listed[0].RepoRef != "team/app" {
		t.Errorf("listed[0].RepoRef = %q, want team/app", listed[0].RepoRef)
	}
	if listed[0].Label != "team/app" {
		t.Errorf("listed[0].Label = %q, want team/app", listed[0].Label)
	}
	if listed[0].State != "open" {
		t.Errorf("listed[0].State = %q, want open", listed[0].State)
	}
	if listed[0].HeadSHA != "" {
		t.Errorf("listed[0].HeadSHA = %q, want empty", listed[0].HeadSHA)
	}
}

func TestGitHubEnterpriseAPIBase(t *testing.T) {
	for _, tc := range []struct {
		webURL      string
		wantBase    string
		wantWebHost string
	}{
		{"https://github.com", "https://api.github.com", "github.com"},
		{"https://ghe.example.com", "https://ghe.example.com/api/v3", "ghe.example.com"},
		{"https://ghe.example.com/", "https://ghe.example.com/api/v3", "ghe.example.com"},
	} {
		base, host := github.DeriveAPIBaseForTest(tc.webURL)
		if base != tc.wantBase {
			t.Errorf("deriveAPIBase(%q) base = %q, want %q", tc.webURL, base, tc.wantBase)
		}
		if host != tc.wantWebHost {
			t.Errorf("deriveAPIBase(%q) webHost = %q, want %q", tc.webURL, host, tc.wantWebHost)
		}
	}
}

func TestGitHubListReviewRequestsPageCapWarns(t *testing.T) {
	github.SetMaxPagesForTest(t, 2)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v3/search/issues", func(w http.ResponseWriter, r *http.Request) {
		var items []map[string]any
		for i := 0; i < 100; i++ {
			items = append(items, map[string]any{"number": i, "repository_url": "https://api.github.com/repos/o/r"})
		}
		json.NewEncoder(w).Encode(map[string]any{"items": items})
	})

	c := github.New(srv.URL, "tok")
	var buf bytes.Buffer
	c.Warn = &buf

	listed, listErr := c.ListReviewRequests(context.Background(), "alice")

	if listErr != nil {
		t.Fatalf("ListReviewRequests: %v", listErr)
	}
	if len(listed) != 200 {
		t.Fatalf("len(listed) = %d, want 200", len(listed))
	}
	want := "nightshift pr: github: listing stopped at 200 items (page cap) — some requests not shown"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", buf.String(), want)
	}
}

func TestGitHubListReviewRequestsRespectsPageBoundary(t *testing.T) {
	github.SetMaxPagesForTest(t, 1)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v3/search/issues", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{
			{"number": 1, "repository_url": "https://api.github.com/repos/o/r"},
		}})
	})
	c := github.New(srv.URL, "tok")
	listed, err := c.ListReviewRequests(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("len(listed) = %d, want 1", len(listed))
	}
}

func TestPRFunctionsUnconfiguredOrError(t *testing.T) {
	c := github.New("https://github.com", "")
	if _, err := c.CurrentUser(context.Background()); err == nil {
		t.Error("CurrentUser: want error when unconfigured")
	} else if !strings.Contains(err.Error(), "github not configured") {
		t.Errorf("CurrentUser err = %q, want it to mention 'github not configured'", err)
	}
	if _, err := c.Get(context.Background(), "o/r", 1); err == nil {
		t.Error("Get: want error when unconfigured")
	}
	if _, err := c.GetRepo(context.Background(), "o/r"); err == nil {
		t.Error("GetRepo: want error when unconfigured")
	}
	if _, err := c.ListReviewRequests(context.Background(), "alice"); err == nil {
		t.Error("ListReviewRequests: want error when unconfigured")
	}

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	c2 := github.New(srv.URL, "tok")
	if _, err := c2.CurrentUser(context.Background()); err == nil {
		t.Error("CurrentUser: want HTTP error")
	}
	if _, err := c2.Get(context.Background(), "o/r", 1); err == nil {
		t.Error("Get: want HTTP error")
	}
	if _, err := c2.GetRepo(context.Background(), "o/r"); err == nil {
		t.Error("GetRepo: want HTTP error")
	}
	if _, err := c2.ListReviewRequests(context.Background(), "alice"); err == nil {
		t.Error("ListReviewRequests: want HTTP error")
	}
}

func TestGitHubHelpers(t *testing.T) {
	c := github.New("https://ghe.example.com", "tok")
	if c.Kind() != forge.KindGitHub {
		t.Errorf("Kind = %v, want github", c.Kind())
	}
	if c.WebHost() != "ghe.example.com" {
		t.Errorf("WebHost = %q, want ghe.example.com", c.WebHost())
	}
	if c.GitAuthUser() != "x-access-token" {
		t.Errorf("GitAuthUser = %q, want x-access-token", c.GitAuthUser())
	}
	if c.SlugSuffix() != "pr" {
		t.Errorf("SlugSuffix = %q, want pr", c.SlugSuffix())
	}
	if c.Label("o/r", 12) != "o/r#12" {
		t.Errorf("Label = %q, want o/r#12", c.Label("o/r", 12))
	}
	if c.HeadRefspec(9) != "+refs/pull/9/head:refs/cr/9" {
		t.Errorf("HeadRefspec = %q", c.HeadRefspec(9))
	}
}

func TestGetPRRepoPathEscaped(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v3/repos/a%20b/c%20d/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		json.NewEncoder(w).Encode(map[string]any{"number": 5})
	})
	c := github.New(srv.URL, "tok")
	if _, err := c.Get(context.Background(), "a b/c d", 5); err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := "/api/v3/repos/a%20b/c%20d/pulls/5"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestGetPRTwoSlashesRejected(t *testing.T) {
	c := github.New("https://github.com", "tok")
	if _, err := c.Get(context.Background(), "group/sub/app", 5); err == nil {
		t.Fatal("want owner/name validation error for a repo with two slashes")
	}
}
