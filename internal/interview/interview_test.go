package interview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/testenv"
)

func TestMain(m *testing.M) {
	testenv.HermeticGit()
	os.Exit(m.Run())
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := gitx.Run(dir, "init", "-q"); err != nil {
		t.Fatalf("git init: %v", err)
	}
	return dir
}

func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "claude")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return path
}

func testOptions(t *testing.T, refine bool) Options {
	t.Helper()
	homeDir := t.TempDir()
	repoDir := newTestRepo(t)
	return Options{
		Home:      homeDir,
		Slug:      "my-slug",
		DraftPath: filepath.Join(homeDir, "specs", "draft", "my-slug.md"),
		Repo:      repoDir,
		Refine:    refine,
		ClaudeBin: fakeClaude(t),
		Model:     "opus",
		Now:       time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC),
	}
}

func TestSettingsJSONExact(t *testing.T) {
	o := testOptions(t, false)

	argv, _, err := Prepare(o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_ = argv

	settingsPath := filepath.Join(o.Home, "interview", o.Slug, "settings.json")
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings.json: %v", err)
	}

	want := "{\n" +
		"  \"permissions\": {\n" +
		"    \"allow\": [\n" +
		"      \"Edit(/" + o.DraftPath + ")\"\n" +
		"    ],\n" +
		"    \"deny\": [\n" +
		"      \"Edit(/" + filepath.Join(o.Repo, "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "specs", "ready", "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "specs", "done", "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "runs", "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "results", "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "context", "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "interview", "**") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "config") + ")\",\n" +
		"      \"Edit(/" + filepath.Join(o.Home, "spec-template.md") + ")\"\n" +
		"    ]\n" +
		"  }\n" +
		"}\n"

	if string(got) != want {
		t.Fatalf("settings.json mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}

	var round json.RawMessage
	if err := json.Unmarshal(got, &round); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	if !strings.Contains(string(got), "\"Edit(//") {
		t.Fatalf("expected an absolute-path rule starting Edit(//, got %s", got)
	}
}

func TestArgvExactNew(t *testing.T) {
	o := testOptions(t, false)

	argv, dir, err := Prepare(o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	claudePath := o.ClaudeBin
	settingsPath := filepath.Join(o.Home, "interview", o.Slug, "settings.json")
	promptPath := filepath.Join(o.Home, "interview", o.Slug, "prompt.md")
	kickoff := "Interview me to write the nightshift spec \"my-slug\" for this repository. " +
		"The draft file is " + o.DraftPath + ". Start by asking what I want done — don't read code yet."

	want := []string{
		claudePath,
		"--model", "opus",
		"--tools", "Read,Grep,Glob,Edit,Write",
		"--disallowedTools", "mcp__*",
		"--settings", settingsPath,
		"--append-system-prompt-file", promptPath,
		"--add-dir", o.Home,
		"-n", "spec-my-slug",
		kickoff,
	}

	if len(argv) != len(want) {
		t.Fatalf("argv length mismatch\ngot:  %#v\nwant: %#v", argv, want)
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] mismatch\ngot:  %q\nwant: %q", i, argv[i], want[i])
		}
	}

	for _, a := range argv {
		if a == "--dangerously-skip-permissions" || a == "bypassPermissions" {
			t.Fatalf("argv must never contain a permission-bypass flag: %v", argv)
		}
	}

	if dir != o.Repo {
		t.Fatalf("dir = %q, want %q", dir, o.Repo)
	}
}

func TestArgvExactRefine(t *testing.T) {
	o := testOptions(t, true)

	argv, _, err := Prepare(o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	wantKickoff := "Read the nightshift spec draft at " + o.DraftPath +
		", tell me its weakest points against the checklist, then interview me to improve it."

	got := argv[len(argv)-1]
	if got != wantKickoff {
		t.Fatalf("refine kickoff = %q, want %q", got, wantKickoff)
	}
}

func TestDirIsRepo(t *testing.T) {
	o := testOptions(t, false)

	_, dir, err := Prepare(o)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if dir != o.Repo {
		t.Fatalf("dir = %q, want %q", dir, o.Repo)
	}

	notRepo := t.TempDir()
	o2 := o
	o2.Repo = notRepo
	if _, _, err := Prepare(o2); err == nil {
		t.Fatalf("expected Prepare to error when Repo is not a git repository")
	}
}
