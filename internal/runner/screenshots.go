package runner

import (
	"path/filepath"
	"regexp"
	"strings"
)

// screenshotsRe matches the optional `screenshots: <dir>` declaration in a
// repo's .nightshift.md. Only spaces and tabs may follow the colon: with (?m),
// `\s*` would swallow a newline and read the next line, so a bare
// `screenshots:` would wrongly count as declared.
var screenshotsRe = regexp.MustCompile(`(?m)^screenshots:[ \t]*(\S+)`)

// parseScreenshotsDir returns the repo-relative screenshots directory declared
// in nightshiftMd (first match wins, trailing slash stripped). Values that
// are empty, absolute, or escape the repo count as not declared.
func parseScreenshotsDir(nightshiftMd []byte) (string, bool) {
	m := screenshotsRe.FindSubmatch(nightshiftMd)
	if m == nil {
		return "", false
	}
	raw := strings.TrimRight(string(m[1]), "/")
	if raw == "" {
		return "", false
	}
	dir := filepath.Clean(raw)
	if dir == "." || dir == ".." || filepath.IsAbs(dir) || strings.HasPrefix(dir, "../") {
		return "", false
	}
	return dir, true
}

// repoScreenshotsDir reads <repo>/.nightshift.md (never following a symlink)
// and parses its screenshots declaration. An empty repo, a missing or
// unreadable file all mean not declared.
func repoScreenshotsDir(repo string) (string, bool) {
	if repo == "" {
		return "", false
	}
	b, err := ReadScaffold(filepath.Join(repo, ".nightshift.md"))
	if err != nil {
		return "", false
	}
	return parseScreenshotsDir([]byte(b))
}
