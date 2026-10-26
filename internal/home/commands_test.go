package home

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/lock"
)

// fakeBin writes a trivial executable script named name into a fresh
// directory and returns that directory, suitable for prepending to PATH.
func fakeBin(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake bin: %v", err)
	}
	return dir
}

func TestInitCreatesLayout(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nightshift-home")
	t.Setenv("PATH", fakeBin(t, "sbx"))

	var stdout, stderr bytes.Buffer
	if err := Init(home, "sbx", &stdout, &stderr); err != nil {
		t.Fatalf("Init: %v", err)
	}

	for _, d := range []string{home, DraftDir(home), ReadyDir(home), DoneDir(home), ResultsDir(home), RunsDir(home), LogsDir(home), ReviewsDir(home)} {
		if !dirExists(d) {
			t.Fatalf("directory missing: %s", d)
		}
	}
	prDir := PRDir(home)
	for _, d := range []string{prDir, filepath.Join(prDir, "repos"), filepath.Join(prDir, "work"), filepath.Join(prDir, "logs")} {
		info, err := os.Stat(d)
		if err != nil {
			t.Fatalf("pr directory missing: %s: %v", d, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s is not a directory", d)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("%s mode = %o, want 0700", d, perm)
		}
	}
	if !fileExists(TemplatePath(home)) {
		t.Fatalf("template missing: %s", TemplatePath(home))
	}
	if !fileExists(ConfigPath(home)) {
		t.Fatalf("config missing: %s", ConfigPath(home))
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr with sbx on PATH: %q", stderr.String())
	}

	// Hand-edit the config, then re-run Init: it must create nothing and
	// must not touch the hand-edited config.
	handEdited := []byte("# hand edited\nNIGHTSHIFT_MODEL=opus\n")
	if err := os.WriteFile(ConfigPath(home), handEdited, 0o644); err != nil {
		t.Fatalf("hand-edit config: %v", err)
	}

	var stdout2, stderr2 bytes.Buffer
	if err := Init(home, "sbx", &stdout2, &stderr2); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	want := "nightshift home ready at " + home + "\n"
	if stdout2.String() != want {
		t.Fatalf("second Init stdout = %q, want %q", stdout2.String(), want)
	}

	got, err := os.ReadFile(ConfigPath(home))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !bytes.Equal(got, handEdited) {
		t.Fatalf("config changed by second Init: got %q, want %q", got, handEdited)
	}
}

func TestInitCreatesLensesDir(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nightshift-home")
	t.Setenv("PATH", fakeBin(t, "sbx"))

	var stdout, stderr bytes.Buffer
	if err := Init(home, "sbx", &stdout, &stderr); err != nil {
		t.Fatalf("Init: %v", err)
	}

	lensesDir := LensesDir(home)
	info, err := os.Stat(lensesDir)
	if err != nil {
		t.Fatalf("lenses dir missing: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", lensesDir)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("lenses dir mode = %o, want 0755", info.Mode().Perm())
	}

	entries, err := os.ReadDir(lensesDir)
	if err != nil {
		t.Fatalf("read lenses dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("lenses dir not empty: %v", entries)
	}

	count := strings.Count(stdout.String(), "created "+lensesDir+"\n")
	if count != 1 {
		t.Fatalf("stdout has %d lines creating %s, want 1: %q", count, lensesDir, stdout.String())
	}

	var stdout2 bytes.Buffer
	if err := Init(home, "sbx", &stdout2, &stderr); err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if strings.Contains(stdout2.String(), lensesDir) {
		t.Fatalf("second Init mentioned %s: %q", lensesDir, stdout2.String())
	}
}

func TestInitWarnsWithoutSbx(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nightshift-home")
	t.Setenv("PATH", t.TempDir()) // empty PATH: sbx cannot be found

	var stdout, stderr bytes.Buffer
	err := Init(home, "sbx", &stdout, &stderr)
	if err != nil {
		t.Fatalf("Init returned an error, want nil: %v", err)
	}
	want := "warning: sbx not found on PATH — runs will fail until it is installed\n"
	if stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestInitSeedsContext(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nightshift-home")
	t.Setenv("PATH", fakeBin(t, "sbx"))

	var stdout, stderr bytes.Buffer
	if err := Init(home, "sbx", &stdout, &stderr); err != nil {
		t.Fatalf("Init: %v", err)
	}

	sandboxPath := filepath.Join(ContextDir(home), "sandbox.md")
	lessonsPath := filepath.Join(ContextDir(home), "lessons.md")

	for _, want := range []string{"created " + sandboxPath, "created " + lessonsPath} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	}

	gotSandbox, err := os.ReadFile(sandboxPath)
	if err != nil {
		t.Fatalf("read sandbox.md: %v", err)
	}
	if string(gotSandbox) != embeddedContextSandbox {
		t.Fatalf("sandbox.md = %q, want embedded seed %q", gotSandbox, embeddedContextSandbox)
	}

	gotLessons, err := os.ReadFile(lessonsPath)
	if err != nil {
		t.Fatalf("read lessons.md: %v", err)
	}
	if string(gotLessons) != embeddedContextLessons {
		t.Fatalf("lessons.md = %q, want embedded seed %q", gotLessons, embeddedContextLessons)
	}
}

func TestInitKeepsEditedContext(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nightshift-home")
	t.Setenv("PATH", fakeBin(t, "sbx"))

	var stdout, stderr bytes.Buffer
	if err := Init(home, "sbx", &stdout, &stderr); err != nil {
		t.Fatalf("Init: %v", err)
	}

	sandboxPath := filepath.Join(ContextDir(home), "sandbox.md")
	edited := []byte("# hand edited sandbox facts\n")
	if err := os.WriteFile(sandboxPath, edited, 0o644); err != nil {
		t.Fatalf("hand-edit sandbox.md: %v", err)
	}

	var stdout2, stderr2 bytes.Buffer
	if err := Init(home, "sbx", &stdout2, &stderr2); err != nil {
		t.Fatalf("second Init: %v", err)
	}

	got, err := os.ReadFile(sandboxPath)
	if err != nil {
		t.Fatalf("read sandbox.md: %v", err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatalf("sandbox.md changed by second Init: got %q, want %q", got, edited)
	}
}

func TestNewFromTemplate(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

	t.Run("embedded template when home has none", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		var stdout bytes.Buffer
		if err := New(home, "demo", "/repo/path", "go test ./...", now, &stdout); err != nil {
			t.Fatalf("New: %v", err)
		}

		dst := filepath.Join(DraftDir(home), "demo.md")
		if strings.TrimSpace(stdout.String()) != dst {
			t.Fatalf("stdout = %q, want %q", stdout.String(), dst)
		}

		data, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("read draft: %v", err)
		}
		fm := ParseFrontmatter(data)
		if fm["created"] != now.Format(time.RFC3339) {
			t.Fatalf("created = %q, want %q", fm["created"], now.Format(time.RFC3339))
		}
		if fm["repo"] != "/repo/path" {
			t.Fatalf("repo = %q, want %q", fm["repo"], "/repo/path")
		}
		if fm["verify"] != "go test ./..." {
			t.Fatalf("verify = %q, want %q", fm["verify"], "go test ./...")
		}
	})

	t.Run("uses home's own template when present", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		if err := ensureLayout(home); err != nil {
			t.Fatalf("ensureLayout: %v", err)
		}
		custom := "---\ncreated: x\nrepo: x\nverify: x\n---\n\nCUSTOM MARKER\n"
		if err := os.WriteFile(TemplatePath(home), []byte(custom), 0o644); err != nil {
			t.Fatalf("write custom template: %v", err)
		}

		var stdout bytes.Buffer
		if err := New(home, "demo", "", "", now, &stdout); err != nil {
			t.Fatalf("New: %v", err)
		}

		data, err := os.ReadFile(filepath.Join(DraftDir(home), "demo.md"))
		if err != nil {
			t.Fatalf("read draft: %v", err)
		}
		if !strings.Contains(string(data), "CUSTOM MARKER") {
			t.Fatalf("draft does not contain the custom template's marker: %s", data)
		}
	})
}

func TestNewRefusesDuplicateAndBadSlug(t *testing.T) {
	now := time.Now()

	t.Run("bad slug", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		err := New(home, "Not-Valid", "", "", now, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error")
		}
		want := `invalid slug "Not-Valid": use lowercase letters, digits, '.', '_' or '-'`
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
		if fileExists(filepath.Join(DraftDir(home), "Not-Valid.md")) {
			t.Fatalf("draft written despite bad slug")
		}
	})

	t.Run("duplicate in draft", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		if err := New(home, "demo", "", "", now, &bytes.Buffer{}); err != nil {
			t.Fatalf("first New: %v", err)
		}
		err := New(home, "demo", "", "", now, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error on duplicate")
		}
		want := "spec demo already exists: " + filepath.Join(DraftDir(home), "demo.md")
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("duplicate in ready", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		if err := ensureLayout(home); err != nil {
			t.Fatalf("ensureLayout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ReadyDir(home), "demo.md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed ready: %v", err)
		}
		err := New(home, "demo", "", "", now, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error on duplicate in ready")
		}
	})

	t.Run("duplicate in done", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		if err := ensureLayout(home); err != nil {
			t.Fatalf("ensureLayout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(DoneDir(home), "demo.md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed done: %v", err)
		}
		err := New(home, "demo", "", "", now, &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error on duplicate in done")
		}
	})
}

func TestReadyValidatesFrontmatter(t *testing.T) {
	writeDraft := func(t *testing.T, home, slug, content string) {
		t.Helper()
		if err := ensureLayout(home); err != nil {
			t.Fatalf("ensureLayout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(DraftDir(home), slug+".md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write draft: %v", err)
		}
	}

	t.Run("missing repo refuses", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeDraft(t, home, "demo", "---\nverify: true\n---\n")
		err := Ready(home, "demo", &bytes.Buffer{})
		want := "demo is missing 'repo:' — fill it in before marking it ready"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("missing verify refuses", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeDraft(t, home, "demo", "---\nrepo: /x\n---\n")
		err := Ready(home, "demo", &bytes.Buffer{})
		want := "demo is missing 'verify:' — fill it in before marking it ready"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("quoted empty value still refuses", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeDraft(t, home, "demo", `---
repo: ""
verify: true
---
`)
		err := Ready(home, "demo", &bytes.Buffer{})
		want := "demo is missing 'repo:' — fill it in before marking it ready"
		if err == nil || err.Error() != want {
			t.Fatalf("err = %v, want %q", err, want)
		}
	})

	t.Run("valid draft moves to ready", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeDraft(t, home, "demo", "---\nrepo: /x\nverify: true\n---\n")

		var stdout bytes.Buffer
		if err := Ready(home, "demo", &stdout); err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if stdout.String() != "ready: demo\n" {
			t.Fatalf("stdout = %q, want %q", stdout.String(), "ready: demo\n")
		}
		if fileExists(filepath.Join(DraftDir(home), "demo.md")) {
			t.Fatalf("draft still present after Ready")
		}
		if !fileExists(filepath.Join(ReadyDir(home), "demo.md")) {
			t.Fatalf("spec not moved to ready")
		}
	})

	t.Run("no such draft", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		err := Ready(home, "nope", &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error")
		}
	})

	t.Run("existing ready target refuses", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeDraft(t, home, "demo", "---\nrepo: /x\nverify: true\n---\n")
		if err := os.WriteFile(filepath.Join(ReadyDir(home), "demo.md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed ready: %v", err)
		}
		err := Ready(home, "demo", &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error")
		}
		if !fileExists(filepath.Join(DraftDir(home), "demo.md")) {
			t.Fatalf("draft should remain untouched when ready target exists")
		}
	})
}

func TestDoneRefusesRunning(t *testing.T) {
	writeReady := func(t *testing.T, home, slug string) {
		t.Helper()
		if err := ensureLayout(home); err != nil {
			t.Fatalf("ensureLayout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ReadyDir(home), slug+".md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed ready: %v", err)
		}
	}

	t.Run("held lock refuses with pid", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeReady(t, home, "demo")

		l, err := lock.TryAcquire(SpecLockPath(home, "demo"), "demo")
		if err != nil {
			t.Fatalf("TryAcquire: %v", err)
		}
		defer l.Release()

		err = Done(home, "demo", time.Now(), &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error")
		}
		want := fmt.Sprintf("spec demo is running (pid %d) — wait for it to finish", os.Getpid())
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
		if !fileExists(filepath.Join(ReadyDir(home), "demo.md")) {
			t.Fatalf("ready spec should remain untouched while locked")
		}
	})

	t.Run("not running moves to done", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeReady(t, home, "demo")

		var stdout bytes.Buffer
		if err := Done(home, "demo", time.Now(), &stdout); err != nil {
			t.Fatalf("Done: %v", err)
		}
		if stdout.String() != "done: demo\n" {
			t.Fatalf("stdout = %q, want %q", stdout.String(), "done: demo\n")
		}
		if fileExists(filepath.Join(ReadyDir(home), "demo.md")) {
			t.Fatalf("ready spec still present after Done")
		}
		if !fileExists(filepath.Join(DoneDir(home), "demo.md")) {
			t.Fatalf("spec not moved to done")
		}
	})

	t.Run("existing done target refuses", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "nightshift-home")
		writeReady(t, home, "demo")
		if err := os.WriteFile(filepath.Join(DoneDir(home), "demo.md"), []byte("x"), 0o644); err != nil {
			t.Fatalf("seed done: %v", err)
		}
		err := Done(home, "demo", time.Now(), &bytes.Buffer{})
		if err == nil {
			t.Fatalf("expected an error")
		}
	})
}

func TestDoneStampsTime(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 30, 0, 0, time.FixedZone("EDT", -4*3600))
	stamp := "done: 2026-10-25T09:30:00-04:00"

	run := func(t *testing.T, content string) string {
		t.Helper()
		home := filepath.Join(t.TempDir(), "nightshift-home")
		if err := ensureLayout(home); err != nil {
			t.Fatalf("ensureLayout: %v", err)
		}
		if err := os.WriteFile(filepath.Join(ReadyDir(home), "demo.md"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := Done(home, "demo", now, &bytes.Buffer{}); err != nil {
			t.Fatalf("Done: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(DoneDir(home), "demo.md"))
		if err != nil {
			t.Fatal(err)
		}
		if ents, _ := os.ReadDir(ReadyDir(home)); len(ents) != 0 {
			t.Fatalf("leftover files in ready/: %v", ents)
		}
		return string(got)
	}

	t.Run("key added after created", func(t *testing.T) {
		got := run(t, "---\ntype: spec\ncreated: 2026-10-24\nrepo: x\n---\nbody\n")
		want := "---\ntype: spec\ncreated: 2026-10-24\n" + stamp + "\nrepo: x\n---\nbody\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("key added at end without created", func(t *testing.T) {
		got := run(t, "---\ntype: spec\n---\nbody\n")
		want := "---\ntype: spec\n" + stamp + "\n---\nbody\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("existing done replaced", func(t *testing.T) {
		got := run(t, "---\ncreated: a\ndone: old\n---\nbody\n")
		want := "---\ncreated: a\n" + stamp + "\n---\nbody\n"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("no frontmatter unchanged", func(t *testing.T) {
		in := "just a body\nwith lines\n"
		if got := run(t, in); got != in {
			t.Fatalf("got %q, want %q", got, in)
		}
	})
}

func TestDoneCaseInsensitiveReadyFile(t *testing.T) {
	h := filepath.Join(t.TempDir(), "nightshift-home")
	if err := ensureLayout(h); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(ReadyDir(h), "x.MD")
	if err := os.WriteFile(src, []byte("---\ntype: spec\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Done(h, "x", time.Now(), &bytes.Buffer{}); err != nil {
		t.Fatalf("Done: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(DoneDir(h), "x.md"))
	if err != nil {
		t.Fatalf("done/x.md missing: %v", err)
	}
	if !strings.Contains(string(got), "done: ") {
		t.Errorf("not stamped: %q", got)
	}
	if ents, _ := os.ReadDir(ReadyDir(h)); len(ents) != 0 {
		t.Errorf("leftover files in ready/: %v", ents)
	}
}

func TestDoneMoveFailureLeavesReadyUnstamped(t *testing.T) {
	setup := func(t *testing.T) (string, string, []byte) {
		h := filepath.Join(t.TempDir(), "nightshift-home")
		if err := ensureLayout(h); err != nil {
			t.Fatal(err)
		}
		src := filepath.Join(ReadyDir(h), "demo.md")
		orig := []byte("---\ntype: spec\ncreated: 2026-10-24\n---\nbody\n")
		if err := os.WriteFile(src, orig, 0o644); err != nil {
			t.Fatal(err)
		}
		return h, src, orig
	}
	check := func(t *testing.T, h, src string, orig []byte) {
		if err := Done(h, "demo", time.Now(), &bytes.Buffer{}); err == nil {
			t.Fatal("Done succeeded despite a failing move")
		}
		got, err := os.ReadFile(src)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, orig) {
			t.Errorf("ready spec changed: %q", got)
		}
		ents, _ := os.ReadDir(ReadyDir(h))
		if len(ents) != 1 {
			t.Errorf("ready/ should hold only the spec: %v", ents)
		}
	}
	t.Run("injected", func(t *testing.T) {
		h, src, orig := setup(t)
		old := rename
		rename = func(string, string) error { return errors.New("injected rename failure") }
		t.Cleanup(func() { rename = old })
		check(t, h, src, orig)
	})
	t.Run("permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores directory permissions")
		}
		h, src, orig := setup(t)
		if err := os.Chmod(DoneDir(h), 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(DoneDir(h), 0o755) })
		check(t, h, src, orig)
	})
}

func TestFindFoldStaysInDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(root, "x.md"), filepath.Join(dir, "sub", "x.md")} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"../x.md", "sub/x.md", "..", ".", ""} {
		if p, ok := FindFold(dir, name); ok {
			t.Errorf("FindFold(%q) = %q, want not found", name, p)
		}
	}
	for _, n := range []string{"Twin.md", "twin.md", "Plain.MD"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if p, ok := FindFold(dir, "twin.md"); !ok || filepath.Base(p) != "twin.md" {
		t.Errorf("exact match: got %q, %v", p, ok)
	}
	if p, ok := FindFold(dir, "plain.md"); !ok || filepath.Base(p) != "Plain.MD" {
		t.Errorf("fold match: got %q, %v", p, ok)
	}
}
