package review

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/forge"
)

// gitEnv is the sanitized, forge-aware git execution layer used for every
// host-side clone/fetch nightshift pr does. It never lets a forge token
// reach argv, a remote URL, or a clone's on-disk config — only an
// in-process GIT_CONFIG_* env override, applied to clone/fetch only.
type gitEnv struct {
	scheme       string // "http" or "https", from the configured base URL
	webHost      string // forge.Client.WebHost(); clone URLs must match this host
	urlSetting   string // forge.Kind.URLSetting(); named in checkCloneURL's error text
	authUser     string // forge.Client.GitAuthUser(): "oauth2" / "x-access-token"
	token        string
	allowFileURL bool                // tests only
	record       func(argv []string) // tests only
}

// fullSHARe matches a full, lowercase 40-hex-character git SHA.
var fullSHARe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// run execs git with args in dir, stripping every GIT_* variable from the
// inherited environment before adding back a minimal, sanitized set plus
// extraEnv. args is recorded (for tests) exactly as passed, never
// including any secret.
func (g gitEnv) run(ctx context.Context, dir string, extraEnv []string, args ...string) (string, error) {
	if g.record != nil {
		g.record(append([]string(nil), args...))
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = dir

	env := make([]string, 0, len(os.Environ())+len(extraEnv)+3)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GIT_") {
			continue
		}
		env = append(env, e)
	}
	env = append(env,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	env = append(env, extraEnv...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (g gitEnv) git(ctx context.Context, extraEnv []string, args ...string) (string, error) {
	return g.run(ctx, "", extraEnv, args...)
}

// authEnv returns the GIT_CONFIG_* overrides that inject the forge's
// bearer credential as an HTTP Basic header, scoped to this forge's own
// host, plus a hard "no redirects" so the header is never replayed
// off-host. Empty token (unconfigured forge) means no auth at all.
func (g gitEnv) authEnv() []string {
	if g.token == "" {
		return nil
	}
	cred := base64.StdEncoding.EncodeToString([]byte(g.authUser + ":" + g.token))
	key := fmt.Sprintf("http.%s://%s/.extraHeader", g.scheme, g.webHost)
	return []string{
		"GIT_CONFIG_COUNT=2",
		"GIT_CONFIG_KEY_0=" + key,
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
		"GIT_CONFIG_KEY_1=http.followRedirects",
		"GIT_CONFIG_VALUE_1=false",
	}
}

// checkCloneURL refuses anything that isn't http(s) against g.webHost
// (file/empty scheme only when g.allowFileURL, tests only), so a
// compromised API response can never make the clone talk to a different
// host than the one the token is scoped to.
func checkCloneURL(cloneURL string, g gitEnv) error {
	u, err := url.Parse(cloneURL)
	if err != nil {
		return fmt.Errorf("clone URL %q is invalid: %w", cloneURL, err)
	}
	switch u.Scheme {
	case "", "file":
		if g.allowFileURL {
			return nil
		}
		return fmt.Errorf("clone URL %q must be http(s) — refusing", cloneURL)
	case "http", "https":
	default:
		return fmt.Errorf("clone URL %q must be http(s) — refusing", cloneURL)
	}
	if !strings.EqualFold(u.Host, g.webHost) {
		return fmt.Errorf("clone URL host %q doesn't match %s host %q — refusing", u.Host, g.urlSetting, g.webHost)
	}
	return nil
}

// ensureClone returns the persistent bare clone at
// <reposDir>/<kind>-<id>.git, cloning it first if it doesn't exist yet.
func ensureClone(ctx context.Context, g gitEnv, reposDir string, kind forge.Kind, id int64, cloneURL string) (string, error) {
	repoPath := filepath.Join(reposDir, fmt.Sprintf("%s-%d.git", kind, id))
	if info, err := os.Stat(repoPath); err == nil && info.IsDir() {
		return repoPath, nil
	}

	if err := checkCloneURL(cloneURL, g); err != nil {
		return "", err
	}
	if err := os.MkdirAll(reposDir, 0o700); err != nil {
		return "", fmt.Errorf("create repos dir: %w", err)
	}
	if _, err := g.git(ctx, g.authEnv(), "clone", "--bare", "--no-tags", cloneURL, repoPath); err != nil {
		return "", fmt.Errorf("clone %s: %w", cloneURL, err)
	}
	return repoPath, nil
}

// fetchChange fetches the change's head via refspec (forge.Client.HeadRefspec(n),
// landing in refs/cr/<n> for both forges) plus the target branch's tip.
func fetchChange(ctx context.Context, g gitEnv, repo, refspec, target string, n int) error {
	_, err := g.git(ctx, g.authEnv(), "--git-dir="+repo, "fetch", "--no-tags", "origin",
		refspec,
		fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", target, target))
	if err != nil {
		return fmt.Errorf("fetch %d: %w", n, err)
	}
	return nil
}

// resolveHead prefers the API-reported head sha when the clone already
// has that commit, falling back to the fetched refs/cr/<n> (e.g. when the
// change advanced between the API call and the fetch).
func resolveHead(ctx context.Context, g gitEnv, repo, apiSHA string, n int) (string, error) {
	if fullSHARe.MatchString(apiSHA) {
		if _, err := g.git(ctx, nil, "--git-dir="+repo, "cat-file", "-e", apiSHA+"^{commit}"); err == nil {
			return apiSHA, nil
		}
	}
	ref := fmt.Sprintf("refs/cr/%d", n)
	out, err := g.git(ctx, nil, "--git-dir="+repo, "rev-parse", ref)
	if err != nil {
		return "", fmt.Errorf("resolve head for change %d: neither the API sha nor %s exist in the clone: %w", n, ref, err)
	}
	if !fullSHARe.MatchString(out) {
		return "", fmt.Errorf("rev-parse %s returned unexpected output %q", ref, out)
	}
	return out, nil
}

// resolveBase prefers the API-reported base sha (GitLab's diff_refs.base_sha)
// when the clone already has that commit, falling back to the merge-base
// of the target branch and head (always the path for GitHub, whose
// base.sha is the branch tip, not the merge base).
func resolveBase(ctx context.Context, g gitEnv, repo, apiBase, target, head string) (string, error) {
	if fullSHARe.MatchString(apiBase) {
		if _, err := g.git(ctx, nil, "--git-dir="+repo, "cat-file", "-e", apiBase+"^{commit}"); err == nil {
			return apiBase, nil
		}
	}
	out, err := g.git(ctx, nil, "--git-dir="+repo, "merge-base", "refs/remotes/origin/"+target, head)
	if err != nil {
		return "", fmt.Errorf("merge-base %s %s: %w", target, head, err)
	}
	if !fullSHARe.MatchString(out) {
		return "", fmt.Errorf("merge-base returned unexpected output %q", out)
	}
	return out, nil
}

// exportTree checks sha out into dest with no .git directory at all (a
// real checkout-index, not git archive, so export-ignore cannot hide
// anything from the reviewer), using a scratch index file that never
// touches the clone's own index.
func exportTree(ctx context.Context, g gitEnv, repo, sha, dest string) error {
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return fmt.Errorf("create export dir %s: %w", dest, err)
	}

	idx, err := os.CreateTemp("", "prreview-index-*")
	if err != nil {
		return fmt.Errorf("create temp index: %w", err)
	}
	idxPath := idx.Name()
	idx.Close()
	defer os.Remove(idxPath)

	extraEnv := []string{"GIT_INDEX_FILE=" + idxPath}
	if _, err := g.git(ctx, extraEnv, "--git-dir="+repo, "read-tree", sha); err != nil {
		return fmt.Errorf("read-tree %s: %w", sha, err)
	}
	if _, err := g.git(ctx, extraEnv, "--git-dir="+repo, "--work-tree="+dest, "checkout-index", "-a", "-f"); err != nil {
		return fmt.Errorf("checkout-index %s into %s: %w", sha, dest, err)
	}
	return nil
}

// lockfileExcludes keeps generated, high-noise lockfiles out of the diff
// the reviewer reads.
var lockfileExcludes = []string{
	":(exclude,glob)**/package-lock.json",
	":(exclude,glob)**/yarn.lock",
	":(exclude,glob)**/pnpm-lock.yaml",
	":(exclude,glob)**/go.sum",
	":(exclude,glob)**/Cargo.lock",
	":(exclude,glob)**/poetry.lock",
	":(exclude,glob)**/composer.lock",
	":(exclude,glob)**/Gemfile.lock",
	":(exclude,glob)**/*.min.js",
	":(exclude,glob)**/.terraform.lock.hcl",
}

func excludePathspecs() []string {
	return append([]string{"--", "."}, lockfileExcludes...)
}

func changesDiff(ctx context.Context, g gitEnv, repo, base, head string) (string, error) {
	args := append([]string{"--git-dir=" + repo, "diff", "-U5", "--no-color", base, head}, excludePathspecs()...)
	out, err := g.git(ctx, nil, args...)
	if err != nil {
		return "", fmt.Errorf("diff %s..%s: %w", base, head, err)
	}
	return out, nil
}

func diffStatText(ctx context.Context, g gitEnv, repo, base, head string) (string, error) {
	args := append([]string{"--git-dir=" + repo, "diff", "--stat=120", base, head}, excludePathspecs()...)
	out, err := g.git(ctx, nil, args...)
	if err != nil {
		return "", fmt.Errorf("diff --stat %s..%s: %w", base, head, err)
	}
	return out, nil
}

func numstatText(ctx context.Context, g gitEnv, repo, base, head string) (string, error) {
	args := append([]string{"--git-dir=" + repo, "diff", "--numstat", base, head}, excludePathspecs()...)
	out, err := g.git(ctx, nil, args...)
	if err != nil {
		return "", fmt.Errorf("diff --numstat %s..%s: %w", base, head, err)
	}
	return out, nil
}

func commitLog(ctx context.Context, g gitEnv, repo, base, head string) (string, error) {
	out, err := g.git(ctx, nil, "--git-dir="+repo, "log", "--no-merges", "--pretty=format:%h %an: %s%n%b", base+".."+head)
	if err != nil {
		return "", fmt.Errorf("log %s..%s: %w", base, head, err)
	}
	return out, nil
}
