package runner

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// maxScreenshotsListed caps how many images a review prompt names.
const maxScreenshotsListed = 40

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

// screenshotPNGs lists the PNG files under root/dir as sorted repo-relative
// slash paths, at most maxScreenshotsListed, with more = how many were cut.
// Any symlinked component, or a non-directory, yields nothing, so a link
// like build/screens -> /home/x can't leak host file names into a prompt.
func screenshotPNGs(root, dir string) (list []string, more int) {
	cur := root
	for _, part := range strings.Split(filepath.ToSlash(dir), "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if err != nil || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			return nil, 0
		}
	}
	var all []string
	_ = filepath.WalkDir(cur, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() || strings.ToLower(filepath.Ext(path)) != ".png" {
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil || strings.IndexFunc(rel, unicode.IsControl) >= 0 {
			return nil
		}
		all = append(all, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(all)
	if len(all) > maxScreenshotsListed {
		return all[:maxScreenshotsListed], len(all) - maxScreenshotsListed
	}
	return all, 0
}

// screenshotsReviewSection renders the "Screenshots to look at" block, or ""
// when there is nothing to list. prefix is prepended to each path.
func screenshotsReviewSection(list []string, more int, prefix string) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Screenshots to look at\n\n")
	for _, p := range list {
		b.WriteString("- " + prefix + p + "\n")
	}
	if more > 0 {
		b.WriteString("+" + strconv.Itoa(more) + " more\n")
	}
	b.WriteString("\nOpen each one. Report anything a tester would notice in the first ten seconds that the diff does not explain.\n")
	return b.String()
}
