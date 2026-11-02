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

// screenshotsIterParagraph is the paragraph appended to the iteration
// prompt when a repo declares a screenshots directory.
func screenshotsIterParagraph(dir string) string {
	return "Screenshots: after verify, this repo writes first-frame screenshots of\n" +
		"its screens to " + dir + ". Before you run 'touch .spec-complete' on a spec that\n" +
		"touches UI, open every image there that your spec could affect and write\n" +
		"one line per image in PROGRESS.md: what it shows, and whether it matches\n" +
		"the spec. An image that shows nothing interactive, overlapping or\n" +
		"clipped text, or an element the spec requires but you cannot see, blocks\n" +
		"completion exactly like a red test. Do not run the verify command\n" +
		"yourself to produce them if that is expensive; read whatever is already\n" +
		"in " + dir + " from the last verify, and say so if it is empty."
}

// screenshotsPlanSentence is the sentence appended to the planning prompt.
func screenshotsPlanSentence(dir string) string {
	return "This repo has screenshots in " + dir + "; plan a step near the end that views them."
}
