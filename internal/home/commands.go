package home

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/lock"
)

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

const commentedConfig = `# nightshift config file.
#
# Precedence for these settings: CLI flag > spec frontmatter > NIGHTSHIFT_*
# env var > this file > default. (max_concurrent is flag > env > this file
# > default; frontmatter is never consulted for it.)
#
# Uncomment a line and set a value to override that default.

# NIGHTSHIFT_BASE=
# NIGHTSHIFT_MODEL=sonnet
# NIGHTSHIFT_PLANNER_MODEL=opus
# NIGHTSHIFT_MAX_ITER=10
# NIGHTSHIFT_ITER_TIMEOUT=30m
# NIGHTSHIFT_MAX_CONCURRENT=2
# NIGHTSHIFT_SBX_BIN=sbx
# NIGHTSHIFT_CLAUDE_BIN=claude
# NIGHTSHIFT_NOTIFY=on

# host claude for 'new'/'refine' interviews (NIGHTSHIFT_CLAUDE_BIN is the one inside the sandbox)
# NIGHTSHIFT_HOST_CLAUDE_BIN=claude
# model for the spec interview
# NIGHTSHIFT_INTERVIEW_MODEL=opus

# run a sandboxed read-only Claude review when a run ends complete
# NIGHTSHIFT_REVIEW=false
# model for that review
# NIGHTSHIFT_REVIEW_MODEL=opus

# opencode binary inside an opencode sandbox
# NIGHTSHIFT_OPENCODE_BIN=opencode
# opencode JSON config for non-Claude providers (no inline secrets)
# NIGHTSHIFT_OPENCODE_CONFIG=
`

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// ensureLayout creates every D2 directory under home, silently (no
// output), creating any that are missing.
func ensureLayout(home string) error {
	for _, d := range []string{home, DraftDir(home), ReadyDir(home), DoneDir(home), ResultsDir(home), RunsDir(home), LogsDir(home), ContextDir(home)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// Init creates home's D2 layout and, if missing, the spec template and a
// fully commented config file. It never overwrites either. sbxBin is
// checked on PATH only to print a warning; a missing sbx is not an error.
func Init(home, sbxBin string, stdout, stderr io.Writer) error {
	var created []string

	for _, d := range []string{home, DraftDir(home), ReadyDir(home), DoneDir(home), ResultsDir(home), RunsDir(home), LogsDir(home), ContextDir(home)} {
		existed := dirExists(d)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		if !existed {
			created = append(created, d)
		}
	}

	tplPath := TemplatePath(home)
	if !fileExists(tplPath) {
		if err := os.WriteFile(tplPath, []byte(embeddedTemplate), 0o644); err != nil {
			return err
		}
		created = append(created, tplPath)
	}

	cfgPath := ConfigPath(home)
	if !fileExists(cfgPath) {
		if err := os.WriteFile(cfgPath, []byte(commentedConfig), 0o644); err != nil {
			return err
		}
		created = append(created, cfgPath)
	}

	sandboxPath := filepath.Join(ContextDir(home), "sandbox.md")
	if !fileExists(sandboxPath) {
		if err := os.WriteFile(sandboxPath, []byte(embeddedContextSandbox), 0o644); err != nil {
			return err
		}
		created = append(created, sandboxPath)
	}

	lessonsPath := filepath.Join(ContextDir(home), "lessons.md")
	if !fileExists(lessonsPath) {
		if err := os.WriteFile(lessonsPath, []byte(embeddedContextLessons), 0o644); err != nil {
			return err
		}
		created = append(created, lessonsPath)
	}

	if len(created) == 0 {
		fmt.Fprintf(stdout, "nightshift home ready at %s\n", home)
	} else {
		for _, p := range created {
			fmt.Fprintf(stdout, "created %s\n", p)
		}
	}

	if _, err := exec.LookPath(sbxBin); err != nil {
		fmt.Fprintf(stderr, "warning: %s not found on PATH — runs will fail until it is installed\n", sbxBin)
	}

	return nil
}

// replaceFrontmatterValue replaces key's value within content's leading
// frontmatter block, leaving every other line untouched. It is a no-op if
// content has no such key in its frontmatter.
func replaceFrontmatterValue(content, key, value string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return content
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			break
		}
		k, _, found := strings.Cut(lines[i], ":")
		if found && strings.TrimSpace(k) == key {
			lines[i] = key + ": " + value
			break
		}
	}
	return strings.Join(lines, "\n")
}

// New starts a draft spec at specs/draft/<slug>.md from home's template
// (or the embedded default), stamping created: to now and substituting
// repo/verify when given. repo and verify of "" leave the template's
// values untouched.
func New(home, slug, repo, verify string, now time.Time, stdout io.Writer) error {
	if !slugRe.MatchString(slug) {
		return fmt.Errorf("invalid slug %q: use lowercase letters, digits, '.', '_' or '-'", slug)
	}

	for _, dir := range []string{DraftDir(home), ReadyDir(home), DoneDir(home)} {
		p := filepath.Join(dir, slug+".md")
		if fileExists(p) {
			return fmt.Errorf("spec %s already exists: %s", slug, p)
		}
	}

	if err := ensureLayout(home); err != nil {
		return err
	}

	content, err := ReadTemplate(home)
	if err != nil {
		return err
	}

	content = replaceFrontmatterValue(content, "created", now.Format(time.RFC3339))
	if repo != "" {
		content = replaceFrontmatterValue(content, "repo", repo)
	}
	if verify != "" {
		content = replaceFrontmatterValue(content, "verify", verify)
	}

	dst := filepath.Join(DraftDir(home), slug+".md")
	if err := os.WriteFile(dst, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(stdout, dst)
	return nil
}

// Ready moves a draft spec to specs/ready/, refusing if it is missing
// required frontmatter or a same-named ready spec already exists.
func Ready(home, slug string, stdout io.Writer) error {
	src := filepath.Join(DraftDir(home), slug+".md")
	if !fileExists(src) {
		return fmt.Errorf("no draft named %s in %s", slug, DraftDir(home))
	}

	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	fm := ParseFrontmatter(data)
	for _, key := range []string{"repo", "verify"} {
		if Unquote(strings.TrimSpace(fm[key])) == "" {
			return fmt.Errorf("%s is missing '%s:' — fill it in before marking it ready", slug, key)
		}
	}

	dst := filepath.Join(ReadyDir(home), slug+".md")
	if fileExists(dst) {
		return fmt.Errorf("spec %s already exists: %s", slug, dst)
	}

	if err := ensureLayout(home); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "ready: %s\n", slug)
	return nil
}

// Done moves a ready spec to specs/done/, refusing while its spec lock is
// held or a same-named done spec already exists. Run state is untouched.
func Done(home, slug string, stdout io.Writer) error {
	src := filepath.Join(ReadyDir(home), slug+".md")
	if !fileExists(src) {
		return fmt.Errorf("no ready spec named %s in %s", slug, ReadyDir(home))
	}

	if info, held := lock.Probe(SpecLockPath(home, slug)); held {
		return fmt.Errorf("spec %s is running (pid %d) — wait for it to finish", slug, info.PID)
	}

	dst := filepath.Join(DoneDir(home), slug+".md")
	if fileExists(dst) {
		return fmt.Errorf("spec %s already exists: %s", slug, dst)
	}

	if err := ensureLayout(home); err != nil {
		return err
	}
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "done: %s\n", slug)
	return nil
}
