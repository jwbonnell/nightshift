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

// ValidSlug reports whether s is a valid spec slug: lowercase letters,
// digits, '.', '_' or '-', starting with a letter or digit.
func ValidSlug(s string) bool {
	return slugRe.MatchString(s)
}

const commentedConfig = `# nightshift config file.
#
# Precedence for these settings: CLI flag > spec frontmatter > NIGHTSHIFT_*
# env var > this file > default. (max_concurrent and jobs are flag > env >
# this file > default; frontmatter is never consulted for either.)
#
# Uncomment a line and set a value to override that default.

# NIGHTSHIFT_BASE=
# NIGHTSHIFT_MODEL=sonnet
# NIGHTSHIFT_PLANNER_MODEL=opus
# NIGHTSHIFT_MAX_ITER=10
# NIGHTSHIFT_EXTEND_ITER=10
# NIGHTSHIFT_ITER_TIMEOUT=30m
# NIGHTSHIFT_MAX_CONCURRENT=2
# how many eligible specs 'run --next --jobs' can start at once, capped by
# NIGHTSHIFT_MAX_CONCURRENT
# NIGHTSHIFT_JOBS=1
# NIGHTSHIFT_SBX_BIN=sbx
# NIGHTSHIFT_CLAUDE_BIN=claude
# NIGHTSHIFT_NOTIFY=on

# host claude for 'new'/'refine' interviews (NIGHTSHIFT_CLAUDE_BIN is the one inside the sandbox)
# NIGHTSHIFT_HOST_CLAUDE_BIN=claude
# model for the spec interview
# NIGHTSHIFT_INTERVIEW_MODEL=opus

# run a sandboxed read-only Claude review when a run ends complete, as
# true/false or a comma list of lenses (general, requirements, quality, security)
# NIGHTSHIFT_REVIEW=false
# model for that review
# NIGHTSHIFT_REVIEW_MODEL=opus
# fix rounds: hand a rejecting review back to the agent up to N times (0-5)
# NIGHTSHIFT_REVIEW_ROUNDS=0
# acceptance-gate hand-backs before the run finishes with the gate failure recorded (0-5)
# NIGHTSHIFT_GATE_ROUNDS=2

# how long to wait out a usage limit with no usable reset time
# NIGHTSHIFT_LIMIT_FALLBACK=60m
# give up waiting out a limit if the wait would exceed this (no --until set)
# NIGHTSHIFT_LIMIT_MAX_WAIT=6h
# stop after this many limit hits in a row with no green iteration between them
# NIGHTSHIFT_LIMIT_MAX_HITS=3
# opencode binary inside an opencode sandbox
# NIGHTSHIFT_OPENCODE_BIN=opencode
# opencode JSON config for non-Claude providers (no inline secrets)
# NIGHTSHIFT_OPENCODE_CONFIG=

# nightshift pr: pre-review of GitHub PRs / GitLab MRs
# NIGHTSHIFT_PR_MODEL=sonnet
# free opencode reviewer: NIGHTSHIFT_PR_MODEL=opencode/opencode/longcat-2.5-preview-free
# NIGHTSHIFT_PR_MAX_TURNS=30
# NIGHTSHIFT_PR_TIMEOUT=15m
# NIGHTSHIFT_PR_MAX_LINES=3000
# override the embedded review prompt (defaults to <home>/pr-prompt.md, then embedded)
# NIGHTSHIFT_PR_PROMPT=
# also write each review note here (committed if it's a git work tree)
# NIGHTSHIFT_PR_MIRROR=
# extra hosts to allow the review sandbox to reach, comma-separated
# NIGHTSHIFT_PR_EGRESS=

# self-hosted GitLab
# NIGHTSHIFT_GITLAB_URL=
# file containing the GitLab token, mode 600 (a NIGHTSHIFT_GITLAB_TOKEN= line here is rejected)
# NIGHTSHIFT_GITLAB_TOKEN_FILE=
# NIGHTSHIFT_GITHUB_URL=https://github.com
# file containing the GitHub token, mode 600 (a NIGHTSHIFT_GITHUB_TOKEN= line here is rejected)
# NIGHTSHIFT_GITHUB_TOKEN_FILE=
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

	for _, d := range []string{home, DraftDir(home), ReadyDir(home), DoneDir(home), ResultsDir(home), RunsDir(home), LogsDir(home), ContextDir(home), LensesDir(home), ReviewsDir(home), HoldoutDir(home)} {
		existed := dirExists(d)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		if !existed {
			created = append(created, d)
		}
	}

	prDir := PRDir(home)
	for _, d := range []string{prDir, filepath.Join(prDir, "repos"), filepath.Join(prDir, "work"), filepath.Join(prDir, "logs")} {
		existed := dirExists(d)
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
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

// upsertFrontmatterValue sets key to value in content's frontmatter,
// replacing an existing key in place, else inserting after created: (or just
// before the closing ---). ok is false, with content unchanged, when there is
// no complete frontmatter block.
func upsertFrontmatterValue(content, key, value string) (string, bool) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		return content, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return content, false
	}
	created := -1
	for i := 1; i < end; i++ {
		k, _, found := strings.Cut(lines[i], ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(k) {
		case key:
			lines[i] = key + ": " + value
			return strings.Join(lines, "\n"), true
		case "created":
			created = i
		}
	}
	at := end
	if created >= 0 {
		at = created + 1
	}
	line := key + ": " + value
	lines = append(lines[:at], append([]string{line}, lines[at:]...)...)
	return strings.Join(lines, "\n"), true
}

// rename is os.Rename; tests swap it to inject a move failure.
var rename = os.Rename

// stampDone moves the ready spec at src to dst with done: set to now. The
// stamped content goes to a temp file in ReadyDir that is renamed straight
// to dst, and only then is src removed, so a failed move never leaves a
// stamped spec in ready/. A spec with no frontmatter is renamed unchanged.
func stampDone(home, src, dst, slug string, now time.Time) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out, ok := upsertFrontmatterValue(string(data), "done", now.Format(time.RFC3339))
	if !ok {
		return rename(src, dst)
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(ReadyDir(home), "."+slug+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if _, err := tmp.WriteString(out); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("moved %s to %s but could not remove %s: %w", src, dst, src, err)
	}
	return nil
}

// FindFold returns the path of the file in dir named name, matched
// case-insensitively (D11.6); an exact match wins. It never leaves dir: an
// empty name, a name with a path separator, and "." or ".." all report not
// found.
func FindFold(dir, name string) (string, bool) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`+string(filepath.Separator)) {
		return "", false
	}
	exact := filepath.Join(dir, name)
	if fileExists(exact) {
		return exact, true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(e.Name(), name) {
			return filepath.Join(dir, e.Name()), true
		}
	}
	return "", false
}

// New starts a draft spec at specs/draft/<slug>.md from home's template
// (or the embedded default), stamping created: to now and substituting
// repo/verify when given. repo and verify of "" leave the template's
// values untouched.
func New(home, slug, repo, verify string, now time.Time, stdout io.Writer) error {
	if !ValidSlug(slug) {
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
// held or a same-named done spec already exists. The ready file is matched
// case-insensitively; the destination is always <slug>.md. It stamps a done:
// key (RFC 3339, local offset) into the spec's frontmatter as part of the
// move; a spec with no frontmatter is moved unchanged. Run state is
// untouched.
func Done(home, slug string, now time.Time, stdout io.Writer) error {
	src, found := FindFold(ReadyDir(home), slug+".md")
	if !found {
		return fmt.Errorf("no ready spec named %s in %s", slug, ReadyDir(home))
	}

	if info, held := lock.Probe(SpecLockPath(home, slug)); held {
		return fmt.Errorf("spec %s is running (pid %d) — wait for it to finish", slug, info.PID)
	}

	dst := filepath.Join(DoneDir(home), slug+".md")
	if existing, exists := FindFold(DoneDir(home), slug+".md"); exists {
		return fmt.Errorf("spec %s already exists: %s", slug, existing)
	}

	if err := ensureLayout(home); err != nil {
		return err
	}
	if err := stampDone(home, src, dst, slug, now); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "done: %s\n", slug)
	return nil
}
