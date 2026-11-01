// Package home resolves nightshift's home directory, its D2 layout, and
// parses its optional config file (D3). It also implements the init/new/
// ready/done subcommands and the embedded spec template.
package home

import (
	"os"
	"path/filepath"
)

// Home returns nightshift's home directory: $NIGHTSHIFT_HOME if set
// (empty counts as unset), else ~/.nightshift.
func Home() (string, error) {
	if h := os.Getenv("NIGHTSHIFT_HOME"); h != "" {
		return h, nil
	}
	hd, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(hd, ".nightshift"), nil
}

// The D2 layout, as paths relative to a resolved home directory.

func SpecsDir(home string) string     { return filepath.Join(home, "specs") }
func DraftDir(home string) string     { return filepath.Join(home, "specs", "draft") }
func ReadyDir(home string) string     { return filepath.Join(home, "specs", "ready") }
func DoneDir(home string) string      { return filepath.Join(home, "specs", "done") }
func ResultsDir(home string) string   { return filepath.Join(home, "results") }
func RunsDir(home string) string      { return filepath.Join(home, "runs") }
func LogsDir(home string) string      { return filepath.Join(home, "logs") }
func ConfigPath(home string) string   { return filepath.Join(home, "config") }
func TemplatePath(home string) string { return filepath.Join(home, "spec-template.md") }
func ContextDir(home string) string   { return filepath.Join(home, "context") }
func LensesDir(home string) string    { return filepath.Join(home, "lenses") }

// ReviewsDir, PRDir and PRPromptPath are nightshift pr's layout (P5):
// review notes land in ReviewsDir, PRDir holds its state (clones, work
// trees, logs, lock), and PRPromptPath is the optional prompt override.
func ReviewsDir(home string) string   { return filepath.Join(home, "reviews") }
func PRDir(home string) string        { return filepath.Join(home, "pr") }
func PRPromptPath(home string) string { return filepath.Join(home, "pr-prompt.md") }

// HoldoutDir holds the owner's per-slug holdout test files for acceptance gates.
func HoldoutDir(home string) string { return filepath.Join(home, "holdout") }

// SpecLockPath is the per-spec lock file path under home's state root,
// shared with internal/runner so both agree on the same convention.
func SpecLockPath(home, slug string) string {
	return filepath.Join(RunsDir(home), ".locks", "spec-"+slug+".lock")
}
