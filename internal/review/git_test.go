package review

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
)

func testGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// makeRemote builds a bare remote with an empty "base" commit on main and
// a second "add a" commit (a.txt "hi\n") pushed only to the change-head
// ref named by refTemplate (e.g. "refs/merge-requests/%d/head" for GitLab
// or "refs/pull/%d/head" for GitHub), never to main.
func makeRemote(t *testing.T, refTemplate string, n int) (remoteDir, headSHA, baseSHA string) {
	t.Helper()
	bare := t.TempDir()
	testGit(t, bare, "init", "--bare", "-q", "-b", "main")

	work := t.TempDir()
	testGit(t, work, "init", "-q", "-b", "main")
	testGit(t, work, "commit", "-q", "--allow-empty", "-m", "base")
	baseSHA = testGit(t, work, "rev-parse", "HEAD")
	testGit(t, work, "remote", "add", "origin", bare)
	testGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}
	testGit(t, work, "add", "-A")
	testGit(t, work, "commit", "-q", "-m", "add a")
	headSHA = testGit(t, work, "rev-parse", "HEAD")

	testGit(t, work, "push", "-q", "origin", "HEAD:"+fmt.Sprintf(refTemplate, n))

	return bare, headSHA, baseSHA
}

func gitlabEnv(token string, record func([]string)) gitEnv {
	return gitEnv{
		scheme:       "https",
		webHost:      "gitlab.example.com",
		urlSetting:   "NIGHTSHIFT_GITLAB_URL",
		authUser:     "oauth2",
		token:        token,
		allowFileURL: true,
		record:       record,
	}
}

func githubEnv(token string, record func([]string)) gitEnv {
	return gitEnv{
		scheme:       "https",
		webHost:      "github.com",
		urlSetting:   "NIGHTSHIFT_GITHUB_URL",
		authUser:     "x-access-token",
		token:        token,
		allowFileURL: true,
		record:       record,
	}
}

func TestTokenNeverPersisted(t *testing.T) {
	ctx := context.Background()
	remote, headSHA, _ := makeRemote(t, "refs/merge-requests/%d/head", 1)

	var recorded [][]string
	record := func(argv []string) { recorded = append(recorded, argv) }
	g := gitlabEnv("super-secret-token", record)

	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/1/head:refs/cr/1", "main", 1); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}

	cred := base64.StdEncoding.EncodeToString([]byte("oauth2:super-secret-token"))

	cfg, err := os.ReadFile(filepath.Join(repo, "config"))
	if err != nil {
		t.Fatalf("read repo config: %v", err)
	}
	if strings.Contains(string(cfg), "super-secret-token") || strings.Contains(string(cfg), cred) {
		t.Fatalf("repo config contains the token or its basic-auth encoding:\n%s", cfg)
	}

	remoteURL := testGit(t, "", "--git-dir="+repo, "config", "--get", "remote.origin.url")
	if strings.Contains(remoteURL, "super-secret-token") {
		t.Fatalf("remote.origin.url contains the token: %s", remoteURL)
	}

	for _, argv := range recorded {
		joined := strings.Join(argv, " ")
		if strings.Contains(joined, "super-secret-token") || strings.Contains(joined, cred) {
			t.Fatalf("recorded git argv leaks the token: %v", argv)
		}
	}

	// All-zero sha from the API is well-formed but doesn't exist in the
	// clone, so resolveHead must fall back to refs/cr/1.
	zero := strings.Repeat("0", 40)
	got, err := resolveHead(ctx, g, repo, zero, 1)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	if got != headSHA {
		t.Fatalf("resolveHead fallback = %s, want %s", got, headSHA)
	}
}

func TestGitNoRedirects(t *testing.T) {
	g := gitlabEnv("tok", nil)
	env := g.authEnv()

	if !contains(env, "GIT_CONFIG_COUNT=2") {
		t.Fatalf("authEnv() = %v, want GIT_CONFIG_COUNT=2", env)
	}
	found := false
	for i := 0; i < len(env); i++ {
		if strings.HasPrefix(env[i], "GIT_CONFIG_KEY_") && strings.HasSuffix(env[i], "=http.followRedirects") {
			keyN := strings.TrimPrefix(strings.TrimSuffix(env[i], "=http.followRedirects"), "GIT_CONFIG_KEY_")
			want := "GIT_CONFIG_VALUE_" + keyN + "=false"
			if i+1 >= len(env) || env[i+1] != want {
				t.Fatalf("authEnv() = %v, want %q right after %q", env, want, env[i])
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("authEnv() = %v, missing http.followRedirects=false", env)
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

func TestGitAuthPerForge(t *testing.T) {
	cases := []struct {
		name     string
		g        gitEnv
		wantUser string
		wantHost string
	}{
		{"gitlab", gitlabEnv("tok", nil), "oauth2", "gitlab.example.com"},
		{"github", githubEnv("tok", nil), "x-access-token", "github.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := c.g.authEnv()
			wantKey := fmt.Sprintf("GIT_CONFIG_KEY_0=http.https://%s/.extraHeader", c.wantHost)
			if env[0] != "GIT_CONFIG_COUNT=2" || env[1] != wantKey {
				t.Fatalf("authEnv() = %v, want key %q", env, wantKey)
			}
			cred := base64.StdEncoding.EncodeToString([]byte(c.wantUser + ":tok"))
			wantValue := "GIT_CONFIG_VALUE_0=Authorization: Basic " + cred
			if env[2] != wantValue {
				t.Fatalf("authEnv() value = %q, want %q", env[2], wantValue)
			}
			if env[3] != "GIT_CONFIG_KEY_1=http.followRedirects" || env[4] != "GIT_CONFIG_VALUE_1=false" {
				t.Fatalf("authEnv() = %v, want followRedirects=false last", env)
			}
		})
	}
}

func TestCloneHostMismatch(t *testing.T) {
	ctx := context.Background()
	var recorded [][]string
	g := gitlabEnv("tok", func(argv []string) { recorded = append(recorded, argv) })
	g.allowFileURL = false

	reposDir := t.TempDir()
	_, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, "https://evil.example/x.git")
	if err == nil {
		t.Fatalf("expected a host-mismatch error")
	}
	if !strings.Contains(err.Error(), "NIGHTSHIFT_GITLAB_URL") {
		t.Fatalf("error %q should name NIGHTSHIFT_GITLAB_URL", err)
	}
	if len(recorded) != 0 {
		t.Fatalf("ensureClone ran git despite the host mismatch: %v", recorded)
	}
	if _, err := os.Stat(reposDir); err == nil {
		if entries, _ := os.ReadDir(reposDir); len(entries) != 0 {
			t.Fatalf("reposDir should stay empty, got %v", entries)
		}
	}
}

func TestEnsureCloneRefusesNonFileWithoutAllow(t *testing.T) {
	ctx := context.Background()
	g := gitlabEnv("tok", nil)
	g.allowFileURL = false

	reposDir := t.TempDir()
	_, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, "/some/plain/path.git")
	if err == nil {
		t.Fatalf("expected an error for a plain-path clone URL without allowFileURL")
	}
	if !strings.Contains(err.Error(), "must be http(s)") {
		t.Fatalf("error = %q, want it to mention http(s)", err)
	}
}

func TestResolveHeadAndBaseFallback(t *testing.T) {
	ctx := context.Background()
	remote, headSHA, baseSHA := makeRemote(t, "refs/merge-requests/%d/head", 7)
	g := gitlabEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/7/head:refs/cr/7", "main", 7); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}

	zero := strings.Repeat("0", 40)

	head, err := resolveHead(ctx, g, repo, zero, 7)
	if err != nil {
		t.Fatalf("resolveHead fallback: %v", err)
	}
	if head != headSHA {
		t.Fatalf("resolveHead fallback = %s, want %s", head, headSHA)
	}

	head2, err := resolveHead(ctx, g, repo, headSHA, 7)
	if err != nil {
		t.Fatalf("resolveHead direct: %v", err)
	}
	if head2 != headSHA {
		t.Fatalf("resolveHead direct = %s, want %s", head2, headSHA)
	}

	base, err := resolveBase(ctx, g, repo, zero, "main", head)
	if err != nil {
		t.Fatalf("resolveBase fallback: %v", err)
	}
	if base != baseSHA {
		t.Fatalf("resolveBase fallback (merge-base) = %s, want %s", base, baseSHA)
	}

	base2, err := resolveBase(ctx, g, repo, baseSHA, "main", head)
	if err != nil {
		t.Fatalf("resolveBase direct: %v", err)
	}
	if base2 != baseSHA {
		t.Fatalf("resolveBase direct = %s, want %s", base2, baseSHA)
	}
}

func TestFetchMissingRefErrors(t *testing.T) {
	ctx := context.Background()
	remote, _, _ := makeRemote(t, "refs/merge-requests/%d/head", 1)
	g := gitlabEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitLab, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}

	if err := fetchChange(ctx, g, repo, "+refs/merge-requests/404/head:refs/cr/404", "main", 404); err == nil {
		t.Fatalf("expected an error fetching a nonexistent change ref")
	}
}

func TestGitHubFetchRefspec(t *testing.T) {
	ctx := context.Background()
	remote, headSHA, baseSHA := makeRemote(t, "refs/pull/%d/head", 9)
	g := githubEnv("", nil)
	reposDir := t.TempDir()
	repo, err := ensureClone(ctx, g, reposDir, forge.KindGitHub, 1, remote)
	if err != nil {
		t.Fatalf("ensureClone: %v", err)
	}
	if err := fetchChange(ctx, g, repo, "+refs/pull/9/head:refs/cr/9", "main", 9); err != nil {
		t.Fatalf("fetchChange: %v", err)
	}

	head, err := resolveHead(ctx, g, repo, "", 9)
	if err != nil {
		t.Fatalf("resolveHead: %v", err)
	}
	if head != headSHA {
		t.Fatalf("resolveHead = %s, want %s", head, headSHA)
	}

	// GitHub never supplies a usable base sha (base.sha is the branch
	// tip, not the merge base), so this must go through merge-base.
	base, err := resolveBase(ctx, g, repo, "", "main", head)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if base != baseSHA {
		t.Fatalf("resolveBase = %s, want %s", base, baseSHA)
	}
}
