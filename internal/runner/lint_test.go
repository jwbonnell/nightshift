package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

// stubVerifier is the BaseVerifier test double used by every step-3
// check-8 test; sbxBaseVerifier (the real sbx implementation) lands in a
// follow-up commit and is tested separately.
type stubVerifier struct {
	fn func(ctx context.Context, dir string, egress []string, cmd string) (string, error)
}

func (s stubVerifier) Verify(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
	return s.fn(ctx, dir, egress, cmd)
}

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

func TestLintVerifyOnBasePass(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

	called := false
	verifier := stubVerifier{fn: func(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
		called = true
		return "all good", nil
	}}

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Verifier: verifier, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if !called {
		t.Fatalf("verifier was not called")
	}
	if res.Fails != 0 {
		t.Fatalf("res = %+v, want zero fails\n%s", res, buf.String())
	}
	if !strings.Contains(buf.String(), "ok    verify-on-base\n") {
		t.Fatalf("output missing ok line:\n%s", buf.String())
	}
}

func TestLintVerifyOnBaseFailShowsTail(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

	var lines []string
	for i := 1; i <= 50; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	out := strings.Join(lines, "\n")
	verifier := stubVerifier{fn: func(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
		return out, fmt.Errorf("exit status 1")
	}}

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Verifier: verifier, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails == 0 {
		t.Fatalf("expected a FAIL, got %+v\n%s", res, buf.String())
	}
	got := buf.String()
	if !strings.Contains(got, "FAIL  verify-on-base: exit: exit status 1\n") {
		t.Fatalf("output missing FAIL line:\n%s", got)
	}
	if !strings.Contains(got, "      line 21\n") || !strings.Contains(got, "      line 50\n") {
		t.Fatalf("output missing expected tail lines:\n%s", got)
	}
	if strings.Contains(got, "line 20\n") {
		t.Fatalf("output unexpectedly contains line 20 (only the last 30 lines should show):\n%s", got)
	}
}

func TestLintVerifyRunsAtBaseNotHead(t *testing.T) {
	repo := newSourceRepo(t)

	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v1"), 0o644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "v1")
	baseSHA, err := resolveBase(repo, "main")
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}

	if err := os.WriteFile(filepath.Join(repo, "file.txt"), []byte("v2"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	mustGit(t, repo, "add", "-A")
	mustGit(t, repo, "commit", "-q", "-m", "v2")

	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "base: "+baseSHA+"\n", repo, fullSpecBody)

	var got string
	verifier := stubVerifier{fn: func(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
		data, rerr := os.ReadFile(filepath.Join(dir, "file.txt"))
		if rerr != nil {
			return "", rerr
		}
		got = string(data)
		return "ok", nil
	}}

	var buf bytes.Buffer
	if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Verifier: verifier, Stdout: &buf}); err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if got != "v1" {
		t.Fatalf("verifier saw file.txt = %q, want %q (verify-on-base must run at base, not HEAD)\n%s", got, "v1", buf.String())
	}
}

func TestLintTempCloneRemoved(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "", repo, fullSpecBody)

	var capturedDir string
	verifier := stubVerifier{fn: func(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
		capturedDir = dir
		return "ok", nil
	}}

	var buf bytes.Buffer
	if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Verifier: verifier, Stdout: &buf}); err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if capturedDir == "" {
		t.Fatalf("verifier was not called")
	}
	if _, err := os.Stat(filepath.Dir(capturedDir)); !os.IsNotExist(err) {
		t.Fatalf("temp clone dir %s still exists after Lint returned: %v", filepath.Dir(capturedDir), err)
	}
}

func TestLintSkipsVerifyAfterEarlierFailure(t *testing.T) {
	repo := newSourceRepo(t)

	run := func(t *testing.T, extraFrontmatter string, corruptVerify bool) (string, bool) {
		homeDir := t.TempDir()
		path := writeLintSpec(t, home.DraftDir(homeDir), "demo", extraFrontmatter, repo, fullSpecBody)
		if corruptVerify {
			data, _ := os.ReadFile(path)
			corrupt := strings.Replace(string(data), "verify: true\n", "", 1)
			if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
		}

		called := false
		verifier := stubVerifier{fn: func(ctx context.Context, dir string, egress []string, cmd string) (string, error) {
			called = true
			return "", nil
		}}

		var buf bytes.Buffer
		if _, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", Verifier: verifier, Stdout: &buf}); err != nil {
			t.Fatalf("Lint: %v", err)
		}
		return buf.String(), called
	}

	t.Run("bad base", func(t *testing.T) {
		out, called := run(t, "base: nonexistent\n", false)
		if called {
			t.Fatalf("verifier should not have been called:\n%s", out)
		}
		if !strings.Contains(out, "skip  verify-on-base (earlier failure)\n") {
			t.Fatalf("output missing expected skip line:\n%s", out)
		}
	})

	t.Run("bad frontmatter", func(t *testing.T) {
		out, called := run(t, "", true)
		if called {
			t.Fatalf("verifier should not have been called:\n%s", out)
		}
		if !strings.Contains(out, "skip  verify-on-base (earlier failure)\n") {
			t.Fatalf("output missing expected skip line:\n%s", out)
		}
	})
}

// TestLintFetchesOriginBeforeBase mirrors runLocked's own best-effort
// `git fetch origin` before resolving the base, so lint verifies the same
// commit a real run would even when the local origin/main ref is stale.
func TestLintFetchesOriginBeforeBase(t *testing.T) {
	bare := t.TempDir()
	mustGit(t, bare, "init", "-q", "--bare", "-b", "main")

	seed := newSourceRepo(t)
	mustGit(t, seed, "remote", "add", "origin", bare)
	mustGit(t, seed, "push", "-q", "origin", "main")

	src := cloneRepo(t, bare)

	if err := os.WriteFile(filepath.Join(seed, "more.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write more.go: %v", err)
	}
	mustGit(t, seed, "add", "-A")
	mustGit(t, seed, "commit", "-q", "-m", "second")
	mustGit(t, seed, "push", "-q", "origin", "main")
	newSHA := mustGit(t, seed, "rev-parse", "HEAD")

	staleSHA, err := resolveBase(src, "main")
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	if staleSHA == newSHA {
		t.Fatalf("expected src's origin/main to be stale before lint fetches")
	}

	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "max_iter: 5\n", src, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails != 0 {
		t.Fatalf("res = %+v, want no fails\n%s", res, buf.String())
	}
	want := "ok    base: main @ " + newSHA[:7] + "\n"
	if !strings.Contains(buf.String(), want) {
		t.Fatalf("output missing %q:\n%s", want, buf.String())
	}
}

// TestLintNoOriginNoFetch asserts lint's output is unchanged for a spec
// repo with no origin remote: no fetch attempt, no warn line.
func TestLintNoOriginNoFetch(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "max_iter: 5\n", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails != 0 || res.Warns != 0 {
		t.Fatalf("res = %+v, want zero\n%s", res, buf.String())
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
	if strings.Contains(buf.String(), "fetch origin") {
		t.Fatalf("output should not mention fetch origin:\n%s", buf.String())
	}
}

// TestLintFetchFailureWarns asserts a failing fetch (origin set, but
// unreachable) produces exactly one warn line, then base still resolves
// from local refs.
func TestLintFetchFailureWarns(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "remote", "add", "origin", filepath.Join(t.TempDir(), "missing"))

	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "max_iter: 5\n", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Fails != 0 {
		t.Fatalf("res = %+v, want no fails\n%s", res, buf.String())
	}
	if res.Warns != 1 {
		t.Fatalf("res = %+v, want exactly one warn\n%s", res, buf.String())
	}

	full, gerr := resolveBase(repo, "main")
	if gerr != nil {
		t.Fatalf("sha: %v", gerr)
	}
	want := "warn  base: fetch origin failed (using local refs): "
	wantBase := "ok    base: main @ " + full[:7] + "\n"
	idx := strings.Index(buf.String(), want)
	if idx < 0 {
		t.Fatalf("output missing expected warn line:\n%s", buf.String())
	}
	rest := buf.String()[idx:]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		t.Fatalf("warn line has no terminator:\n%s", buf.String())
	}
	if rest[nl+1:nl+1+len(wantBase)] != wantBase {
		t.Fatalf("warn line not immediately followed by base ok line:\n%s", buf.String())
	}
}
