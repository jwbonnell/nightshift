// Package interview prepares and launches a read-only host `claude` session
// that interviews the user to write (or refine) a nightshift spec draft. It
// never runs code itself: Prepare only computes the argv and settings/prompt
// files a caller execs claude with.
package interview

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/runner"
)

// Byte caps for file-sourced prompt sections (spec: "each section is
// truncated to its byte cap"); embedded/generated sections 1-5 are
// uncapped.
const (
	contextCapBytes = 16 * 1024
	resultCapBytes  = 6 * 1024
	doneCapBytes    = 16 * 1024
)

//go:embed instructions.md
var instructionsMD string

//go:embed checklist.md
var checklistMD string

// Options configures one interview launch.
type Options struct {
	Home      string
	Slug      string
	DraftPath string // absolute
	Repo      string // absolute, must be a git repo
	Refine    bool   // true for `nightshift refine`
	ClaudeBin string // host claude; env > config > "claude"
	Model     string // env > config > "opus"
	Now       time.Time
}

// Prepare writes <Home>/interview/<slug>/{settings.json,prompt.md} and
// returns the argv (argv[0] = the resolved claude path) and the working
// directory a caller should exec argv in.
func Prepare(o Options) (argv []string, dir string, err error) {
	if !filepath.IsAbs(o.DraftPath) {
		return nil, "", fmt.Errorf("interview: DraftPath must be absolute: %s", o.DraftPath)
	}
	if !filepath.IsAbs(o.Repo) {
		return nil, "", fmt.Errorf("interview: Repo must be absolute: %s", o.Repo)
	}
	if !gitx.IsRepo(o.Repo) {
		return nil, "", fmt.Errorf("interview: %s is not a git repository", o.Repo)
	}

	claudePath, err := exec.LookPath(o.ClaudeBin)
	if err != nil {
		return nil, "", err
	}

	if err := os.MkdirAll(home.ContextDir(o.Home), 0o755); err != nil {
		return nil, "", err
	}
	interviewDir := filepath.Join(o.Home, "interview", o.Slug)
	if err := os.MkdirAll(interviewDir, 0o755); err != nil {
		return nil, "", err
	}

	settingsPath := filepath.Join(interviewDir, "settings.json")
	settingsData, err := buildSettings(o)
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(settingsPath, settingsData, 0o644); err != nil {
		return nil, "", err
	}

	promptPath := filepath.Join(interviewDir, "prompt.md")
	promptData, err := buildPrompt(o)
	if err != nil {
		return nil, "", err
	}
	if err := os.WriteFile(promptPath, []byte(promptData), 0o644); err != nil {
		return nil, "", err
	}

	return buildArgv(o, claudePath, settingsPath, promptPath), o.Repo, nil
}

// settingsDoc mirrors the shape of the generated settings.json.
type settingsDoc struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
}

// editRule formats p as an Edit() permission rule covering the absolute
// path p (so the rule string starts with the literal "Edit(/").
func editRule(p string) string {
	return "Edit(/" + p + ")"
}

// buildSettings generates settings.json: Edit is allowed only on the
// draft, and denied everywhere else nightshift keeps state (deny beats
// allow, so listing the repo and these paths is enough even though they
// don't overlap the draft).
func buildSettings(o Options) ([]byte, error) {
	var doc settingsDoc
	doc.Permissions.Allow = []string{editRule(o.DraftPath)}
	doc.Permissions.Deny = []string{
		editRule(filepath.Join(o.Repo, "**")),
		editRule(filepath.Join(home.ReadyDir(o.Home), "**")),
		editRule(filepath.Join(home.DoneDir(o.Home), "**")),
		editRule(filepath.Join(home.RunsDir(o.Home), "**")),
		editRule(filepath.Join(home.ResultsDir(o.Home), "**")),
		editRule(filepath.Join(home.ContextDir(o.Home), "**")),
		editRule(filepath.Join(o.Home, "interview", "**")),
		editRule(home.ConfigPath(o.Home)),
		editRule(home.TemplatePath(o.Home)),
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// buildPrompt assembles the content of the system-prompt append file, in
// the ten sections the spec lists in order.
func buildPrompt(o Options) (string, error) {
	mode := "new"
	if o.Refine {
		mode = "refine"
	}

	var b strings.Builder

	b.WriteString("## Your role\n\n")
	b.WriteString(instructionsMD)

	b.WriteString("\n## This session\n\n")
	fmt.Fprintf(&b, "Slug: %s\nDraft: %s\nRepo: %s\nDate: %s\nMode: %s\n",
		o.Slug, o.DraftPath, o.Repo, o.Now.Format("2006-01-02"), mode)

	b.WriteString("\n## What the nightshift agent will see\n\n")
	writeFenced(&b, runner.PlanPreamble())
	b.WriteString("\n")
	writeFenced(&b, runner.IterPreamble())

	b.WriteString("\n## Spec checklist\n\n")
	b.WriteString(checklistMD)

	b.WriteString("\n## Spec template\n\n")
	tpl, err := home.ReadTemplate(o.Home)
	if err != nil {
		return "", err
	}
	writeFenced(&b, tpl)

	b.WriteString("\n## Sandbox facts\n\n")
	b.WriteString(fileSectionOrNoneYet(filepath.Join(home.ContextDir(o.Home), "sandbox.md"), contextCapBytes))

	b.WriteString("\n## Lessons from past runs\n\n")
	b.WriteString(fileSectionOrNoneYet(filepath.Join(home.ContextDir(o.Home), "lessons.md"), contextCapBytes))

	b.WriteString("\n## Repo card\n\n")
	b.WriteString(fileSectionOrNoneYet(filepath.Join(o.Repo, ".nightshift.md"), contextCapBytes))

	b.WriteString("\n## Recent runs on this repo\n\n")
	b.WriteString(recentRunsSection(o))

	b.WriteString("\n## Example specs that completed\n\n")
	b.WriteString(doneExamplesSection(o))

	return b.String(), nil
}

// capSection truncates content to capBytes (backing off to a UTF-8 rune
// boundary) and appends a pointer back to path, per the spec's "each
// section is truncated to its byte cap" rule. Content at or under the cap
// is returned unchanged.
func capSection(content string, capBytes int, path string) string {
	if len(content) <= capBytes {
		return content
	}
	cut := capBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	return content[:cut] + "\n[truncated — read " + path + " for the rest]"
}

// fileSectionOrNoneYet reads path, capping it at capBytes; a missing file
// becomes "(none yet)" per spec.
func fileSectionOrNoneYet(path string, capBytes int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(none yet)"
	}
	return capSection(string(data), capBytes, path)
}

// fenceFor picks a backtick fence one rune longer than the longest run of
// backticks already in content (minimum 3), so fenced content can never
// itself contain the fence.
func fenceFor(content string) string {
	longest, run := 0, 0
	for _, r := range content {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
		} else {
			run = 0
		}
	}
	n := longest + 1
	if n < 3 {
		n = 3
	}
	return strings.Repeat("`", n)
}

// writeFenced appends content to b wrapped in a fence safe for content's
// own backtick runs.
func writeFenced(b *strings.Builder, content string) {
	fence := fenceFor(content)
	b.WriteString(fence)
	b.WriteString("\n")
	b.WriteString(content)
	if !strings.HasSuffix(content, "\n") {
		b.WriteString("\n")
	}
	b.WriteString(fence)
	b.WriteString("\n")
}

// resultFileItem is one candidate file from a directory listing, ordered
// by mtime desc then name desc as a deterministic tie-break.
type resultFileItem struct {
	path, name string
	modTime    time.Time
}

func sortNewestFirst(items []resultFileItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].modTime.Equal(items[j].modTime) {
			return items[i].name > items[j].name
		}
		return items[i].modTime.After(items[j].modTime)
	})
}

// recentRunsSection lists the newest 3 results (by mtime) whose
// frontmatter repo: matches o.Repo after filepath.Clean, newest first.
func recentRunsSection(o Options) string {
	dir := home.ResultsDir(o.Home)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "(none yet)"
	}

	wantRepo := filepath.Clean(o.Repo)
	var items []resultFileItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fm := home.ParseFrontmatter(data)
		repo := home.Unquote(strings.TrimSpace(fm["repo"]))
		if repo == "" || filepath.Clean(repo) != wantRepo {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, resultFileItem{path: p, name: e.Name(), modTime: info.ModTime()})
	}
	if len(items) == 0 {
		return "(none yet)"
	}
	sortNewestFirst(items)
	if len(items) > 3 {
		items = items[:3]
	}
	return renderFileSections(items, resultCapBytes)
}

// doneExamplesSection lists the newest 2 done specs (by mtime), newest
// first.
func doneExamplesSection(o Options) string {
	dir := home.DoneDir(o.Home)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "(none yet)"
	}

	var items []resultFileItem
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, resultFileItem{path: filepath.Join(dir, e.Name()), name: e.Name(), modTime: info.ModTime()})
	}
	if len(items) == 0 {
		return "(none yet)"
	}
	sortNewestFirst(items)
	if len(items) > 2 {
		items = items[:2]
	}
	return renderFileSections(items, doneCapBytes)
}

// renderFileSections renders each item as "### <filename>" followed by
// its capped content.
func renderFileSections(items []resultFileItem, capBytes int) string {
	var b strings.Builder
	for i, it := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "### %s\n\n", it.name)
		data, err := os.ReadFile(it.path)
		if err != nil {
			continue
		}
		b.WriteString(capSection(string(data), capBytes, it.path))
		b.WriteString("\n")
	}
	return b.String()
}

// buildArgv builds the exact claude invocation for o, in the order the
// CLI must receive it: each flag and its value as separate argv elements.
func buildArgv(o Options, claudePath, settingsPath, promptPath string) []string {
	kickoff := newKickoff(o)
	if o.Refine {
		kickoff = refineKickoff(o)
	}
	return []string{
		claudePath,
		"--model", o.Model,
		"--permission-mode", "dontAsk",
		"--tools", "Read,Grep,Glob,Edit,Write",
		"--disallowedTools", "mcp__*",
		"--settings", settingsPath,
		"--append-system-prompt-file", promptPath,
		"--add-dir", o.Home,
		"-n", "spec-" + o.Slug,
		kickoff,
	}
}

func newKickoff(o Options) string {
	return fmt.Sprintf(
		"Interview me to write the nightshift spec %q for this repository. The draft file is %s. Start by asking what I want done — don't read code yet.",
		o.Slug, o.DraftPath)
}

func refineKickoff(o Options) string {
	return fmt.Sprintf(
		"Read the nightshift spec draft at %s, tell me its weakest points against the checklist, then interview me to improve it.",
		o.DraftPath)
}
