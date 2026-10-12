package forge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
)

type payload struct {
	Value string `json:"value"`
}

func TestGetJSONDecodesSuccess(t *testing.T) {
	var gotHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Test")
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Write([]byte(`{"value":"ok"}`))
	}))
	defer srv.Close()

	host := srv.Listener.Addr().String()
	c := forge.NewHTTPClient(host, "NIGHTSHIFT_GITLAB_URL")

	var out payload
	if err := forge.GetJSON(context.Background(), c, forge.KindGitLab, srv.URL, map[string]string{"X-Test": "hdr"}, &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if out.Value != "ok" {
		t.Errorf("out.Value = %q, want ok", out.Value)
	}
	if gotHeader != "hdr" {
		t.Errorf("header = %q, want hdr", gotHeader)
	}
}

func TestGetJSONHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := forge.NewHTTPClient(srv.Listener.Addr().String(), "NIGHTSHIFT_GITHUB_URL")
	var out payload
	err := forge.GetJSON(context.Background(), c, forge.KindGitHub, srv.URL, nil, &out)
	if err == nil {
		t.Fatal("want error")
	}
	want := "github: HTTP 404 for " + srv.URL + " (check URL/token/scope)"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestGetJSONDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	c := forge.NewHTTPClient(srv.Listener.Addr().String(), "NIGHTSHIFT_GITLAB_URL")
	var out payload
	err := forge.GetJSON(context.Background(), c, forge.KindGitLab, srv.URL, nil, &out)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.HasPrefix(err.Error(), "gitlab: decode "+srv.URL+": ") {
		t.Errorf("err = %q, want prefix %q", err.Error(), "gitlab: decode "+srv.URL+": ")
	}
}

func TestNewHTTPClientFollowsSameHostRedirect(t *testing.T) {
	var targetHit bool
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/from", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srv.URL+"/to", http.StatusFound)
	})
	mux.HandleFunc("/to", func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.Write([]byte(`{"value":"ok"}`))
	})

	c := forge.NewHTTPClient(srv.Listener.Addr().String(), "NIGHTSHIFT_GITLAB_URL")
	var out payload
	if err := forge.GetJSON(context.Background(), c, forge.KindGitLab, srv.URL+"/from", nil, &out); err != nil {
		t.Fatalf("GetJSON: %v", err)
	}
	if !targetHit {
		t.Error("redirect target was never hit")
	}
	if out.Value != "ok" {
		t.Errorf("out.Value = %q, want ok", out.Value)
	}
}

func TestNewHTTPClientRefusesCrossHostRedirect(t *testing.T) {
	var bHits int
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHits++
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("cross-host redirect target received Authorization header %q", auth)
		}
		w.Write([]byte(`{"value":"ok"}`))
	}))
	defer srvB.Close()

	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, srvB.URL+"/to", http.StatusFound)
	}))
	defer srvA.Close()

	apiHost := srvA.Listener.Addr().String()
	c := forge.NewHTTPClient(apiHost, "NIGHTSHIFT_GITLAB_URL")
	var out payload
	err := forge.GetJSON(context.Background(), c, forge.KindGitLab, srvA.URL, map[string]string{"Authorization": "secret-token"}, &out)
	if err == nil {
		t.Fatal("want error")
	}
	bHost := srvB.Listener.Addr().String()
	want := "forge: refusing redirect from " + apiHost + " to " + bHost + " (different host; the token is never sent off-host) — check NIGHTSHIFT_GITLAB_URL"
	if !strings.Contains(err.Error(), want) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), want)
	}
	if bHits != 0 {
		t.Errorf("server B was hit %d times, want 0 (token must never reach the off-host server)", bHits)
	}
}

func TestNewHTTPClientStopsAfter10Redirects(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		http.Redirect(w, r, srv.URL+"/loop?n="+strconv.Itoa(n+1), http.StatusFound)
	})

	c := forge.NewHTTPClient(srv.Listener.Addr().String(), "NIGHTSHIFT_GITLAB_URL")
	var out payload
	err := forge.GetJSON(context.Background(), c, forge.KindGitLab, srv.URL+"/loop?n=0", nil, &out)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "forge: stopped after 10 redirects") {
		t.Errorf("err = %q, want it to contain the 10-redirect message", err.Error())
	}
}

func TestGetJSONRejectsInvalidURL(t *testing.T) {
	c := forge.NewHTTPClient("example.com", "NIGHTSHIFT_GITLAB_URL")
	var out payload
	err := forge.GetJSON(context.Background(), c, forge.KindGitLab, "://bad-url", nil, &out)
	if err == nil {
		t.Fatal("want error")
	}
}

func TestNewHTTPClientHostComparisonIsPortSensitive(t *testing.T) {
	// A redirect from host:portA to host:portB must be refused, even
	// though the hostname is identical — the apiHost comparison is
	// against URL.Host, which includes the port.
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse %s: %v", srv.URL, err)
	}
	mux.HandleFunc("/from", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+u.Hostname()+":1/to", http.StatusFound)
	})

	c := forge.NewHTTPClient(srv.Listener.Addr().String(), "NIGHTSHIFT_GITHUB_URL")
	var out payload
	err = forge.GetJSON(context.Background(), c, forge.KindGitHub, srv.URL+"/from", nil, &out)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "forge: refusing redirect") {
		t.Errorf("err = %q, want a refusing-redirect error", err.Error())
	}
}
