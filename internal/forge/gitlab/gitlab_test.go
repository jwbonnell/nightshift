package gitlab_test

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
	"github.com/jwbonnell/nightshift/internal/forge/gitlab"
)

func TestGitLabReadOnly(t *testing.T) {
	var methods []string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "tok" {
			t.Errorf("PRIVATE-TOKEN = %q, want tok", got)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization = %q, want unset (gitlab uses PRIVATE-TOKEN only)", got)
		}
		json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})
	mux.HandleFunc("/api/v4/projects/team%2Fapp/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		json.NewEncoder(w).Encode(map[string]any{
			"iid": 5, "project_id": 9, "title": "t", "state": "opened",
		})
	})
	mux.HandleFunc("/api/v4/projects/team%2Fapp", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		json.NewEncoder(w).Encode(map[string]any{
			"id": 9, "path_with_namespace": "team/app", "http_url_to_repo": "https://x/team/app.git",
		})
	})
	mux.HandleFunc("/api/v4/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		q := r.URL.Query()
		if q.Get("scope") != "all" || q.Get("state") != "opened" || q.Get("reviewer_username") != "alice" {
			t.Errorf("query = %v, want scope=all state=opened reviewer_username=alice", q)
		}
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	c := gitlab.New(srv.URL, "tok")

	if _, err := c.CurrentUser(context.Background()); err != nil {
		t.Fatalf("CurrentUser: %v", err)
	}
	if _, err := c.Get(context.Background(), "team/app", 5); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.GetRepo(context.Background(), "team/app"); err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	if _, err := c.ListReviewRequests(context.Background(), "alice"); err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}

	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("method = %s, want GET", m)
		}
	}
	if len(methods) != 4 {
		t.Errorf("got %d requests, want 4", len(methods))
	}
}

func TestGitLabDecodesFields(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})
	mux.HandleFunc("/api/v4/projects/team%2Fapp/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"iid": 5, "project_id": 9, "title": "fix bug", "state": "opened",
			"description": "desc", "sha": "headsha", "target_branch": "main",
			"web_url":    "https://gitlab.example.com/team/app/-/merge_requests/5",
			"created_at": "2026-01-01T00:00:00Z",
			"author":     map[string]string{"username": "bob"},
			"diff_refs":  map[string]string{"base_sha": "basesha", "head_sha": "headsha2", "start_sha": "startsha"},
			"draft":      false, "work_in_progress": true,
		})
	})
	mux.HandleFunc("/api/v4/projects/team%2Fapp", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"id": 9, "path_with_namespace": "team/app", "http_url_to_repo": "https://gitlab.example.com/team/app.git",
		})
	})

	c := gitlab.New(srv.URL, "tok")

	ch, err := c.Get(context.Background(), "team/app", 5)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := forge.Change{
		Number: 5, Title: "fix bug", State: "open", Draft: true,
		Description: "desc", HeadSHA: "headsha", DiffBaseSHA: "basesha",
		TargetBranch: "main", Author: "bob",
		WebURL:    "https://gitlab.example.com/team/app/-/merge_requests/5",
		CreatedAt: "2026-01-01T00:00:00Z",
	}
	if ch != want {
		t.Errorf("Get = %+v, want %+v", ch, want)
	}

	repo, err := c.GetRepo(context.Background(), "team/app")
	if err != nil {
		t.Fatalf("GetRepo: %v", err)
	}
	wantRepo := forge.Repo{ID: 9, Path: "team/app", CloneURL: "https://gitlab.example.com/team/app.git"}
	if repo != wantRepo {
		t.Errorf("GetRepo = %+v, want %+v", repo, wantRepo)
	}
}

func TestGitLabStateMapping(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"opened", "open"},
		{"closed", "closed"},
		{"merged", "merged"},
		{"locked", "locked"},
	} {
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		mux.HandleFunc("/api/v4/projects/p/merge_requests/1", func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{"iid": 1, "state": tc.in})
		})
		c := gitlab.New(srv.URL, "tok")
		ch, err := c.Get(context.Background(), "p", 1)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if ch.State != tc.want {
			t.Errorf("state(%q) = %q, want %q", tc.in, ch.State, tc.want)
		}
		srv.Close()
	}
}

func TestGetMRProjectPathEscaped(t *testing.T) {
	var gotPath string
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v4/projects/group%2Fsub%2Fapp/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		json.NewEncoder(w).Encode(map[string]any{"iid": 5})
	})
	c := gitlab.New(srv.URL, "tok")
	if _, err := c.Get(context.Background(), "group/sub/app", 5); err != nil {
		t.Fatalf("Get: %v", err)
	}
	want := "/api/v4/projects/group%2Fsub%2Fapp/merge_requests/5"
	if gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
}

func TestListReviewMRsPaginatesAndDraftFlag(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var pagesSeen []string
	mux.HandleFunc("/api/v4/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		pagesSeen = append(pagesSeen, q.Get("page"))
		if q.Get("order_by") != "created_at" || q.Get("sort") != "asc" {
			t.Errorf("query = %v, want order_by=created_at&sort=asc", q)
		}
		page := q.Get("page")
		var batch []map[string]any
		if page == "1" {
			for i := 0; i < 100; i++ {
				batch = append(batch, map[string]any{
					"iid": i, "project_id": 1, "title": fmt.Sprintf("t%d", i),
					"state": "opened", "draft": i == 0, "created_at": "2026-01-01T00:00:00Z",
					"references": map[string]string{"full": fmt.Sprintf("team/app!%d", i)},
				})
			}
		} else if page == "2" {
			batch = append(batch, map[string]any{
				"iid": 100, "project_id": 1, "title": "t100", "state": "opened",
				"created_at": "2026-01-02T00:00:00Z",
				"references": map[string]string{"full": "team/app!100"},
			})
		}
		json.NewEncoder(w).Encode(batch)
	})

	c := gitlab.New(srv.URL, "tok")
	listed, err := c.ListReviewRequests(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}
	if len(listed) != 101 {
		t.Fatalf("len(listed) = %d, want 101", len(listed))
	}
	if len(pagesSeen) != 2 {
		t.Fatalf("pages seen = %v, want 2", pagesSeen)
	}
	if !listed[0].Draft {
		t.Error("listed[0].Draft = false, want true")
	}
	if listed[0].Label != "team/app" {
		t.Errorf("listed[0].Label = %q, want team/app", listed[0].Label)
	}
	if listed[0].RepoRef != "1" {
		t.Errorf("listed[0].RepoRef = %q, want 1", listed[0].RepoRef)
	}
}

func TestMRFunctionsUnconfiguredOrError(t *testing.T) {
	c := gitlab.New("", "")
	if _, err := c.CurrentUser(context.Background()); err == nil {
		t.Error("CurrentUser: want error when unconfigured")
	} else if !strings.Contains(err.Error(), "gitlab not configured") {
		t.Errorf("CurrentUser err = %q, want it to mention 'gitlab not configured'", err)
	}
	if _, err := c.Get(context.Background(), "p", 1); err == nil {
		t.Error("Get: want error when unconfigured")
	}
	if _, err := c.GetRepo(context.Background(), "p"); err == nil {
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
	c2 := gitlab.New(srv.URL, "tok")
	if _, err := c2.CurrentUser(context.Background()); err == nil {
		t.Error("CurrentUser: want HTTP error")
	}
	if _, err := c2.Get(context.Background(), "p", 1); err == nil {
		t.Error("Get: want HTTP error")
	}
	if _, err := c2.GetRepo(context.Background(), "p"); err == nil {
		t.Error("GetRepo: want HTTP error")
	}
	if _, err := c2.ListReviewRequests(context.Background(), "alice"); err == nil {
		t.Error("ListReviewRequests: want HTTP error")
	}
}

func TestGitLabHelpers(t *testing.T) {
	c := gitlab.New("https://gitlab.example.com", "tok")
	if c.Kind() != forge.KindGitLab {
		t.Errorf("Kind = %v, want gitlab", c.Kind())
	}
	if c.WebHost() != "gitlab.example.com" {
		t.Errorf("WebHost = %q, want gitlab.example.com", c.WebHost())
	}
	if c.GitAuthUser() != "oauth2" {
		t.Errorf("GitAuthUser = %q, want oauth2", c.GitAuthUser())
	}
	if c.SlugSuffix() != "mr" {
		t.Errorf("SlugSuffix = %q, want mr", c.SlugSuffix())
	}
	if c.Label("team/app", 12) != "team/app!12" {
		t.Errorf("Label = %q, want team/app!12", c.Label("team/app", 12))
	}
	if c.HeadRefspec(9) != "+refs/merge-requests/9/head:refs/cr/9" {
		t.Errorf("HeadRefspec = %q", c.HeadRefspec(9))
	}
}

func TestListReviewRequestsPageCapWarns(t *testing.T) {
	gitlab.SetMaxPagesForTest(t, 2)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v4/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		var batch []map[string]any
		for i := 0; i < 100; i++ {
			batch = append(batch, map[string]any{"iid": i, "project_id": 1, "state": "opened"})
		}
		json.NewEncoder(w).Encode(batch)
	})

	c := gitlab.New(srv.URL, "tok")
	var buf bytes.Buffer
	c.Warn = &buf

	listed, listErr := c.ListReviewRequests(context.Background(), "alice")

	if listErr != nil {
		t.Fatalf("ListReviewRequests: %v", listErr)
	}
	if len(listed) != 200 {
		t.Fatalf("len(listed) = %d, want 200", len(listed))
	}
	want := "nightshift pr: gitlab: listing stopped at 200 items (page cap) — some requests not shown"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("stderr = %q, want it to contain %q", buf.String(), want)
	}
}

func TestListReviewMRsRespectsPageBoundary(t *testing.T) {
	// A page shorter than perPage stops pagination even if it's exactly maxPages.
	gitlab.SetMaxPagesForTest(t, 1)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v4/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{"iid": 1, "project_id": 1, "state": "opened"}})
	})
	c := gitlab.New(srv.URL, "tok")
	listed, err := c.ListReviewRequests(context.Background(), "alice")
	if err != nil {
		t.Fatalf("ListReviewRequests: %v", err)
	}
	if len(listed) != 1 {
		t.Errorf("len(listed) = %d, want 1", len(listed))
	}
}

func TestListReviewRequestsPageError(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v4/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := gitlab.New(srv.URL, "tok")
	if _, err := c.ListReviewRequests(context.Background(), "alice"); err == nil {
		t.Error("want error")
	}
}

func TestGitLabCurrentUserHTTPError(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	c := gitlab.New(srv.URL, "tok")
	_, err := c.CurrentUser(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("err = %q, want it to mention HTTP 401", err)
	}
}
