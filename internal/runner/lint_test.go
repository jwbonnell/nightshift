package runner

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

// fullSpecBody is a clean spec body with all six sections, 2 tasks and 1
// acceptance item — writeDraftSpec/writeReadySpec only write "# spec" as
// the body, which fails every section/task/acceptance check.
const fullSpecBody = `
# demo

## Goal

Goal text.

## Context

Context text.

## Constraints

- a constraint

## Tasks

- [ ] task one
- [ ] task two

## Acceptance criteria

- [ ] criterion one

## Out of scope

- nothing
`

func writeLintSpec(t *testing.T, dir, slug, extraFrontmatter, repo, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nrepo: " + repo + "\nverify: true\n" + extraFrontmatter + "---\n" + body
	path := filepath.Join(dir, slug+".md")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return path
}

func TestLintOutputFormat(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "max_iter: 5\n", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails != 0 || res.Warns != 0 {
		t.Fatalf("res = %+v, want zero", res)
	}

	full, gerr := resolveBase(repo, "main")
	if gerr != nil {
		t.Fatalf("sha: %v", gerr)
	}
	sha := full[:7]
	want := "ok    frontmatter\n" +
		"ok    sections\n" +
		"ok    placeholders\n" +
		"ok    tasks\n" +
		"ok    acceptance\n" +
		"ok    max_iter\n" +
		"ok    base: main @ " + sha + "\n" +
		"skip  verify-on-base (--no-verify)\n" +
		"lint: 0 failed, 0 warnings\n"
	if buf.String() != want {
		t.Fatalf("output =\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestLintFrontmatterFailAndWarn(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("fail on bad frontmatter", func(t *testing.T) {
		homeDir := t.TempDir()
		path := writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)
		// Corrupt verify: to trigger resolveSpecFile's own error.
		data, _ := os.ReadFile(path)
		corrupt := strings.Replace(string(data), "verify: true\n", "", 1)
		if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails == 0 {
			t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "FAIL  frontmatter: ") {
			t.Fatalf("output missing FAIL frontmatter line:\n%s", buf.String())
		}
	})

	t.Run("warn on unknown key", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "bogus: yes\n", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Warns == 0 {
			t.Fatalf("expected a warn, got %+v\n%s", res, buf.String())
		}
		want := `warn  frontmatter: unknown key "bogus"` + "\n"
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, buf.String())
		}
	})
}

func TestLintSections(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("fail on missing required section", func(t *testing.T) {
		homeDir := t.TempDir()
		body := strings.Replace(fullSpecBody, "## Goal\n\nGoal text.\n\n", "", 1)
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, body)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails == 0 {
			t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "FAIL  sections: missing ## Goal\n") {
			t.Fatalf("output missing expected FAIL line:\n%s", buf.String())
		}
	})

	t.Run("warn on missing constraints", func(t *testing.T) {
		homeDir := t.TempDir()
		body := strings.Replace(fullSpecBody, "## Constraints\n\n- a constraint\n\n", "", 1)
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, body)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Warns == 0 {
			t.Fatalf("expected a warn, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "warn  sections: missing ## Constraints\n") {
			t.Fatalf("output missing expected warn line:\n%s", buf.String())
		}
	})
}

func TestLintPlaceholders(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	body := strings.Replace(fullSpecBody, "# demo", "# <short imperative title>", 1)
	body = strings.Replace(body, "- [ ] task two\n", "- [ ] \n", 1)
	body = strings.Replace(body, "- nothing\n", "- \n", 1)
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, body)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails == 0 {
		t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
	}
	if !strings.Contains(buf.String(), "FAIL  placeholders: template placeholders on line(s) ") {
		t.Fatalf("output missing expected FAIL line:\n%s", buf.String())
	}
}

func TestLintTasksAndAcceptance(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("fail on zero tasks", func(t *testing.T) {
		homeDir := t.TempDir()
		body := strings.Replace(fullSpecBody, "- [ ] task one\n- [ ] task two\n", "", 1)
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, body)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails == 0 {
			t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), `FAIL  tasks: no "- [ ]" items under ## Tasks`+"\n") {
			t.Fatalf("output missing expected FAIL line:\n%s", buf.String())
		}
	})

	t.Run("warn on many tasks", func(t *testing.T) {
		homeDir := t.TempDir()
		extra := strings.Repeat("- [ ] extra task\n", 8)
		body := strings.Replace(fullSpecBody, "- [ ] task two\n", "- [ ] task two\n"+extra, 1)
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "max_iter: 20\n", repo, body)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Warns == 0 {
			t.Fatalf("expected a warn, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "warn  tasks: 10 tasks — consider splitting the spec\n") {
			t.Fatalf("output missing expected warn line:\n%s", buf.String())
		}
	})

	t.Run("fail on zero acceptance criteria", func(t *testing.T) {
		homeDir := t.TempDir()
		body := strings.Replace(fullSpecBody, "- [ ] criterion one\n", "", 1)
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, body)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails == 0 {
			t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), `FAIL  acceptance: no "- [ ]" items under ## Acceptance criteria`+"\n") {
			t.Fatalf("output missing expected FAIL line:\n%s", buf.String())
		}
	})
}

func TestLintMaxIter(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "max_iter: 2\n", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Warns == 0 {
		t.Fatalf("expected a warn, got %+v\n%s", res, buf.String())
	}
	want := "warn  max_iter: 2 < 2 tasks + 2 — allow one planning iteration plus at least one retry\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, buf.String())
	}
}

func TestLintBase(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("fail on unresolvable base", func(t *testing.T) {
		homeDir := t.TempDir()
		writeLintSpec(t, home.DraftDir(homeDir), "demo", "base: nonexistent\n", repo, fullSpecBody)

		var buf bytes.Buffer
		res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
		if err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if res.Fails == 0 {
			t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
		}
		if !strings.Contains(buf.String(), "FAIL  base: ") {
			t.Fatalf("output missing expected FAIL line:\n%s", buf.String())
		}
	})

	t.Run("skip after earlier failure", func(t *testing.T) {
		homeDir := t.TempDir()
		path := writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)
		data, _ := os.ReadFile(path)
		corrupt := strings.Replace(string(data), "verify: true\n", "", 1)
		if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}

		var buf bytes.Buffer
		if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Stdout: &buf}); err != nil {
			t.Fatalf("Lint: %v", err)
		}
		if !strings.Contains(buf.String(), "skip  base (earlier failure)\n") {
			t.Fatalf("output missing expected skip line:\n%s", buf.String())
		}
		if !strings.Contains(buf.String(), "skip  max_iter (earlier failure)\n") {
			t.Fatalf("output missing expected skip line:\n%s", buf.String())
		}
		if !strings.Contains(buf.String(), "skip  verify-on-base (earlier failure)\n") {
			t.Fatalf("output missing expected skip line:\n%s", buf.String())
		}
	})
}

func TestLintFindsDraftThenReady(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()

	draftPath := writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)
	writeLintSpec(t, home.ReadyDir(homeDir), "demo", "", repo, fullSpecBody)

	path, err := locateLintSpec(homeDir, "demo")
	if err != nil {
		t.Fatalf("locateLintSpec: %v", err)
	}
	if path != draftPath {
		t.Fatalf("path = %q, want draft path %q", path, draftPath)
	}

	if err := os.Remove(draftPath); err != nil {
		t.Fatalf("remove draft: %v", err)
	}
	path, err = locateLintSpec(homeDir, "demo")
	if err != nil {
		t.Fatalf("locateLintSpec: %v", err)
	}
	readyPath := filepath.Join(home.ReadyDir(homeDir), "demo.md")
	if path != readyPath {
		t.Fatalf("path = %q, want ready path %q", path, readyPath)
	}

	if err := os.Remove(readyPath); err != nil {
		t.Fatalf("remove ready: %v", err)
	}
	_, err = locateLintSpec(homeDir, "demo")
	want := "no spec named demo in specs/draft/ or specs/ready/"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
