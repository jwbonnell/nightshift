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
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
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

	if err := os.MkdirAll(filepath.Join(o.Home, "context"), 0o755); err != nil {
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
		editRule(filepath.Join(o.Home, "context", "**")),
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

// buildPrompt assembles the content of the system-prompt append file.
// Section 1-2 only for now; later sections (the nightshift preambles, the
// checklist, context sources) are added on top of this in-order build.
func buildPrompt(o Options) (string, error) {
	mode := "new"
	if o.Refine {
		mode = "refine"
	}

	s := "## Your role\n\n" + instructionsMD +
		"\n## This session\n\n" +
		fmt.Sprintf("Slug: %s\nDraft: %s\nRepo: %s\nDate: %s\nMode: %s\n",
			o.Slug, o.DraftPath, o.Repo, o.Now.Format("2006-01-02"), mode)
	return s, nil
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
