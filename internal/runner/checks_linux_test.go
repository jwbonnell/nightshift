package runner

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestPostRunHardlinkFails confirms the clone-objects check catches a
// hardlinked object file — the exact failure mode cloneRepo's --no-local
// flag exists to prevent (see its comment in state.go).
func TestPostRunHardlinkFails(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify

	r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("loop status = %v, want Complete", status)
	}

	objects := filepath.Join(r.clone, ".git", "objects")
	var victim string
	err := filepath.WalkDir(objects, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || victim != "" {
			return err
		}
		victim = path
		return nil
	})
	if err != nil {
		t.Fatalf("walk objects: %v", err)
	}
	if victim == "" {
		t.Fatalf("no object file found under %s to hardlink", objects)
	}
	if err := os.Link(victim, victim+".hardlink-test"); err != nil {
		t.Fatalf("Link: %v", err)
	}

	rep := r.postRunChecks()
	line := rep.lines[checkCloneObjectsIdx]
	if line.label != "FAIL" {
		t.Fatalf("clone-objects check = %q, want FAIL", line.String())
	}
}
