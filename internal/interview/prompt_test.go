package interview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/runner"
)

func readPrompt(t *testing.T, o Options) string {
	t.Helper()
	if _, _, err := Prepare(o); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(o.Home, "interview", o.Slug, "prompt.md"))
	if err != nil {
		t.Fatalf("read prompt.md: %v", err)
	}
	return string(data)
}

var promptHeadings = []string{
	"## Your role",
	"## This session",
	"## What the nightshift agent will see",
	"## Spec checklist",
	"## Spec template",
	"## Sandbox facts",
	"## Lessons from past runs",
	"## Repo card",
	"## Recent runs on this repo",
	"## Example specs that completed",
}

func TestPromptSectionOrder(t *testing.T) {
	prompt := readPrompt(t, testOptions(t, false))

	last := -1
	for _, h := range promptHeadings {
		idx := strings.Index(prompt, h)
		if idx < 0 {
			t.Fatalf("missing heading %q", h)
		}
		if idx <= last {
			t.Fatalf("heading %q out of order", h)
		}
		last = idx
	}
}

// section returns the body of heading h: everything between it and the
// next "\n## " (or end of string).
func section(t *testing.T, prompt, h string) string {
	t.Helper()
	idx := strings.Index(prompt, h)
	if idx < 0 {
		t.Fatalf("missing heading %q", h)
	}
	rest := prompt[idx+len(h):]
	if next := strings.Index(rest, "\n## "); next >= 0 {
		return rest[:next]
	}
	return rest
}

func TestPromptNoneYetFallbacks(t *testing.T) {
	prompt := readPrompt(t, testOptions(t, false))

	for _, h := range []string{
		"## Sandbox facts",
		"## Lessons from past runs",
		"## Repo card",
		"## Recent runs on this repo",
		"## Example specs that completed",
	} {
		if !strings.Contains(section(t, prompt, h), "(none yet)") {
			t.Errorf("section %q: want (none yet), got %q", h, section(t, prompt, h))
		}
	}
}

func TestPromptTruncatesWithPointer(t *testing.T) {
	o := testOptions(t, false)
	if err := os.MkdirAll(filepath.Join(o.Home, "context"), 0o755); err != nil {
		t.Fatalf("mkdir context: %v", err)
	}
	sandboxPath := filepath.Join(o.Home, "context", "sandbox.md")
	big := strings.Repeat("a", contextCapBytes+1024)
	if err := os.WriteFile(sandboxPath, []byte(big), 0o644); err != nil {
		t.Fatalf("write sandbox.md: %v", err)
	}

	prompt := readPrompt(t, o)
	body := section(t, prompt, "## Sandbox facts")

	want := "[truncated — read " + sandboxPath + " for the rest]"
	if !strings.Contains(body, want) {
		t.Fatalf("expected truncation pointer %q in %q", want, body)
	}
	if len(body) >= len(big) {
		t.Fatalf("expected a truncated body shorter than the original %d bytes, got %d", len(big), len(body))
	}
}

func writeResult(t *testing.T, path, repo string, mod time.Time) {
	t.Helper()
	content := fmt.Sprintf("---\ntype: nightshift-result\nrepo: %s\n---\n\nbody\n", repo)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write result %s: %v", path, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestPromptRecentRunsFilteredByRepo(t *testing.T) {
	o := testOptions(t, false)
	resultsDir := home.ResultsDir(o.Home)
	if err := os.MkdirAll(resultsDir, 0o755); err != nil {
		t.Fatalf("mkdir results: %v", err)
	}

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	otherRepo := filepath.Join(t.TempDir(), "other-repo")

	// 4 matching results, oldest to newest.
	for i, name := range []string{"match-1.md", "match-2.md", "match-3.md", "match-4.md"} {
		writeResult(t, filepath.Join(resultsDir, name), o.Repo, base.Add(time.Duration(i)*time.Hour))
	}
	// 2 results for a different repo, newer than all the matches.
	for i, name := range []string{"other-1.md", "other-2.md"} {
		writeResult(t, filepath.Join(resultsDir, name), otherRepo, base.Add(time.Duration(10+i)*time.Hour))
	}

	prompt := readPrompt(t, o)
	body := section(t, prompt, "## Recent runs on this repo")

	wantOrder := []string{"match-4.md", "match-3.md", "match-2.md"}
	last := -1
	for _, name := range wantOrder {
		idx := strings.Index(body, name)
		if idx < 0 {
			t.Fatalf("expected %q in recent runs section, got %q", name, body)
		}
		if idx <= last {
			t.Fatalf("%q out of order in %q", name, body)
		}
		last = idx
	}
	for _, name := range []string{"match-1.md", "other-1.md", "other-2.md"} {
		if strings.Contains(body, name) {
			t.Fatalf("did not expect %q in recent runs section, got %q", name, body)
		}
	}
}

func TestPromptIncludesPreamblesAndChecklistVerbatim(t *testing.T) {
	prompt := readPrompt(t, testOptions(t, false))

	for _, want := range []string{
		runner.PlanPreamble(),
		runner.IterPreamble(),
		checklistMD,
		instructionsMD,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing expected verbatim content: %q", want)
		}
	}
}
