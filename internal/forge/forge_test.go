package forge_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/forge/github"
	"github.com/jwbonnell/nightshift/internal/forge/gitlab"
)

// TestForgeReadOnly records every HTTP method hit against both clients over
// every exported Client method and asserts it was always GET.
func TestForgeReadOnly(t *testing.T) {
	var methods []string
	record := func(r *http.Request) { methods = append(methods, r.Method) }

	glMux := http.NewServeMux()
	glSrv := httptest.NewServer(glMux)
	defer glSrv.Close()
	glMux.HandleFunc("/api/v4/user", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]string{"username": "alice"})
	})
	glMux.HandleFunc("/api/v4/projects/team%2Fapp/merge_requests/5", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"iid": 5, "project_id": 9, "state": "opened"})
	})
	glMux.HandleFunc("/api/v4/projects/team%2Fapp", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"id": 9, "path_with_namespace": "team/app"})
	})
	glMux.HandleFunc("/api/v4/merge_requests", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode([]map[string]any{})
	})

	gl := gitlab.New(glSrv.URL, "tok")
	var clients []forge.Client = []forge.Client{gl}

	ghMux := http.NewServeMux()
	ghSrv := httptest.NewServer(ghMux)
	defer ghSrv.Close()
	ghMux.HandleFunc("/api/v3/user", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]string{"login": "alice"})
	})
	ghMux.HandleFunc("/api/v3/repos/o/r/pulls/5", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"number": 5, "state": "open"})
	})
	ghMux.HandleFunc("/api/v3/repos/o/r", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"id": 9, "full_name": "o/r"})
	})
	ghMux.HandleFunc("/api/v3/search/issues", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		json.NewEncoder(w).Encode(map[string]any{"items": []map[string]any{}})
	})

	gh := github.New(ghSrv.URL, "tok")
	clients = append(clients, gh)

	repoRef := map[forge.Kind]string{forge.KindGitLab: "team/app", forge.KindGitHub: "o/r"}

	for _, c := range clients {
		ref := repoRef[c.Kind()]
		if _, err := c.CurrentUser(context.Background()); err != nil {
			t.Fatalf("%s.CurrentUser: %v", c.Kind(), err)
		}
		if _, err := c.Get(context.Background(), ref, 5); err != nil {
			t.Fatalf("%s.Get: %v", c.Kind(), err)
		}
		if _, err := c.GetRepo(context.Background(), ref); err != nil {
			t.Fatalf("%s.GetRepo: %v", c.Kind(), err)
		}
		if _, err := c.ListReviewRequests(context.Background(), "alice"); err != nil {
			t.Fatalf("%s.ListReviewRequests: %v", c.Kind(), err)
		}
	}

	if len(methods) != 8 {
		t.Fatalf("got %d requests, want 8", len(methods))
	}
	for _, m := range methods {
		if m != http.MethodGet {
			t.Errorf("method = %s, want GET", m)
		}
	}
}

// TestForgeRefusesCrossHostRedirect asserts both clients refuse an
// off-host redirect, never send their token to the redirect target, and
// name the right URL setting in the error.
func TestForgeRefusesCrossHostRedirect(t *testing.T) {
	cases := []struct {
		name       string
		newClient  func(baseURL, token string) forge.Client
		urlSetting string
	}{
		{"gitlab", func(baseURL, token string) forge.Client { return gitlab.New(baseURL, token) }, "NIGHTSHIFT_GITLAB_URL"},
		{"github", func(baseURL, token string) forge.Client { return github.New(baseURL, token) }, "NIGHTSHIFT_GITHUB_URL"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var bHits int
			srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				bHits++
				if auth := r.Header.Get("Authorization"); auth != "" {
					t.Errorf("cross-host redirect target received Authorization header %q", auth)
				}
				if tok := r.Header.Get("PRIVATE-TOKEN"); tok != "" {
					t.Errorf("cross-host redirect target received PRIVATE-TOKEN header %q", tok)
				}
				w.Write([]byte(`{}`))
			}))
			defer srvB.Close()

			srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, srvB.URL+"/to", http.StatusFound)
			}))
			defer srvA.Close()

			c := tc.newClient(srvA.URL, "tok")
			_, err := c.CurrentUser(context.Background())
			if err == nil {
				t.Fatal("want error")
			}
			if bHits != 0 {
				t.Errorf("server B got %d requests, want 0", bHits)
			}
			if !strings.Contains(err.Error(), tc.urlSetting) {
				t.Errorf("err = %q, want it to mention %s", err, tc.urlSetting)
			}
		})
	}
}
