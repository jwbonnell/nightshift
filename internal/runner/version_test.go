package runner

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

// TestVersionStampedInStateLogAndNote: a full run with Options.Version set
// records that version in run.json, in the runner.log start line, and in
// the result note's frontmatter; a resumed run with a different Version
// updates run.json and logs its own "resume:" line.
func TestVersionStampedInStateLogAndNote(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1, Version: "v9",
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		MaxIter: 1,
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("run1: %v", err)
	}

	st := readRunState(t, stateRoot, "demo")
	if st.NightshiftVersion != "v9" {
		t.Fatalf("run.json nightshift_version = %q, want v9", st.NightshiftVersion)
	}

	logPath := filepath.Join(stateRoot, "demo", "runner.log")
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData), "start:") || !strings.Contains(string(logData), "version=v9") {
		t.Fatalf("runner.log missing a start: line with version=v9:\n%s", logData)
	}

	entries, err := os.ReadDir(home.ResultsDir(homeDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected exactly 1 result note, err=%v entries=%v", err, entries)
	}
	noteData, err := os.ReadFile(filepath.Join(home.ResultsDir(homeDir), entries[0].Name()))
	if err != nil {
		t.Fatalf("read result note: %v", err)
	}
	if !strings.Contains(string(noteData), "nightshift: v9\n") {
		t.Fatalf("result note missing frontmatter nightshift: v9:\n%s", noteData)
	}

	sb2 := newStubSandbox()
	sb2.agent = completingAgent(t)
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	opts2.Version = "v10"
	opts2.MaxIter = 0
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusComplete {
		t.Fatalf("run2 (resume): status=%v err=%v, want Complete/nil", status2, err2)
	}

	st2 := readRunState(t, stateRoot, "demo")
	if st2.NightshiftVersion != "v10" {
		t.Fatalf("run.json nightshift_version after resume = %q, want v10", st2.NightshiftVersion)
	}
	logData2, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData2), "resume:") || !strings.Contains(string(logData2), "version=v10") {
		t.Fatalf("runner.log missing a resume: line with version=v10:\n%s", logData2)
	}
}

// TestVersionDefaultsToDev confirms an empty Options.Version is recorded as
// "dev", not left blank.
func TestVersionDefaultsToDev(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)
	stateRoot := home.RunsDir(homeDir)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("Run: %v", err)
	}

	st := readRunState(t, stateRoot, "demo")
	if st.NightshiftVersion != "dev" {
		t.Fatalf("run.json nightshift_version = %q, want dev", st.NightshiftVersion)
	}
}
