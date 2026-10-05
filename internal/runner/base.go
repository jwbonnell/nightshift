package runner

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jwbonnell/nightshift/internal/gitx"
)

var shaLike = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// refExists reports whether ref resolves to a commit in repo.
func refExists(repo, ref string) bool {
	_, err := gitx.Run(repo, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// defaultBaseName picks repo's base branch when none was requested:
// origin/HEAD's target, else "main", else "master", else "HEAD".
func defaultBaseName(repo string) string {
	if out, err := gitx.Run(repo, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if out = strings.TrimSpace(out); out != "" {
			return strings.TrimPrefix(out, "origin/")
		}
	}
	for _, cand := range []string{"main", "master"} {
		if refExists(repo, "refs/heads/"+cand) || refExists(repo, "refs/remotes/origin/"+cand) {
			return cand
		}
	}
	return "HEAD"
}

// resolveBase resolves a requested base name to a full commit sha in
// repo, preferring origin/<name> over a same-named local branch. A ref
// path, "HEAD", or a sha-like string is tried as given, with no
// origin/local preference. It never fetches or creates refs.
func resolveBase(repo, name string) (string, error) {
	var candidates []string
	switch {
	case strings.HasPrefix(name, "refs/"), name == "HEAD", shaLike.MatchString(name):
		candidates = []string{name}
	default:
		candidates = []string{"refs/remotes/origin/" + name, "refs/heads/" + name}
	}

	for _, cand := range candidates {
		out, err := gitx.Run(repo, "rev-parse", "--verify", "--quiet", cand+"^{commit}")
		if out = strings.TrimSpace(out); err == nil && out != "" {
			return out, nil
		}
	}
	return "", fmt.Errorf("base %q not found in %s (looked for origin/%s and %s); fetch it or fix base:", name, repo, name, name)
}
