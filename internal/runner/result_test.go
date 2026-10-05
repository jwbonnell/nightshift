package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/gitx"
)

func TestResultCommittedOnlyWhenHomeIsRepo(t *testing.T) {
	repo := newSourceRepo(t)

	t.Run("plain home: note written, no commit", func(t *testing.T) {
		sb := newStubSandbox()
		sb.agent = func(ctx context.Context, iter int, dir string) error {
			if iter == 2 {
				commitFile(t, dir, "work.go", "package main\n", "work")
				touchComplete(t, dir)
			}
			return nil
		}
		sb.verify = func(iter int, dir string) error { return nil }

		var stdout bytes.Buffer
		opts := Options{Stdout: &stdout, Notifier: noopNotifierForTest{}}
		r := newTestRun(t, repo, "", opts, sb)
		status := r.loop()

		notePath, err := r.writeResultNote(status)
		if err != nil {
			t.Fatalf("writeResultNote: %v", err)
		}
		if !fileExistsT(notePath) {
			t.Fatalf("result note not written")
		}
		if gitx.IsRepo(r.opts.Home) {
			t.Fatalf("test setup: home should not be a git repo")
		}
	})

	t.Run("home as a git repo: exactly one commit touching only the note", func(t *testing.T) {
		sb := newStubSandbox()
		sb.agent = func(ctx context.Context, iter int, dir string) error {
			if iter == 2 {
				commitFile(t, dir, "work.go", "package main\n", "work")
				touchComplete(t, dir)
			}
			return nil
		}
		sb.verify = func(iter int, dir string) error { return nil }

		var stdout bytes.Buffer
		opts := Options{Stdout: &stdout, Notifier: noopNotifierForTest{}}
		r := newTestRun(t, repo, "", opts, sb)

		mustGit(t, r.opts.Home, "init", "-q")
		mustGit(t, r.opts.Home, "config", "user.email", "a@b.c")
		mustGit(t, r.opts.Home, "config", "user.name", "a")
		mustGit(t, r.opts.Home, "add", "-A")
		mustGit(t, r.opts.Home, "commit", "-q", "-m", "initial home commit")

		// An unrelated dirty file that must stay uncommitted.
		if err := os.WriteFile(filepath.Join(r.opts.Home, "unrelated.txt"), []byte("dirty"), 0o644); err != nil {
			t.Fatalf("write unrelated.txt: %v", err)
		}

		before := mustGit(t, r.opts.Home, "rev-list", "--count", "HEAD")

		status := r.loop()
		notePath, err := r.writeResultNote(status)
		if err != nil {
			t.Fatalf("writeResultNote: %v", err)
		}

		after := mustGit(t, r.opts.Home, "rev-list", "--count", "HEAD")
		if before == after {
			t.Fatalf("expected exactly one new commit, count unchanged at %s", before)
		}

		subject := mustGit(t, r.opts.Home, "log", "-1", "--format=%s")
		wantSubject := "nightshift: result demo (complete)"
		if subject != wantSubject {
			t.Fatalf("commit subject = %q, want %q", subject, wantSubject)
		}

		changed := mustGit(t, r.opts.Home, "show", "--name-only", "--format=", "HEAD")
		relNote, err := filepath.Rel(r.opts.Home, notePath)
		if err != nil {
			t.Fatalf("rel: %v", err)
		}
		files := strings.Fields(changed)
		if len(files) != 1 || files[0] != relNote {
			t.Fatalf("commit touched %v, want exactly [%s]", files, relNote)
		}

		status2 := mustGit(t, r.opts.Home, "status", "--porcelain", "--", "unrelated.txt")
		if !strings.Contains(status2, "unrelated.txt") {
			t.Fatalf("unrelated.txt should still show as dirty: %q", status2)
		}
	})
}

func TestDiffstatKeepsLeadingSpace(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	var stdout bytes.Buffer
	r := newTestRun(t, repo, "", Options{Stdout: &stdout}, sb)
	status := r.loop()

	body := r.resultBody(status)
	i := strings.Index(body, "## Changes\n\n```\n")
	if i < 0 {
		t.Fatalf("no ## Changes block in result body:\n%s", body)
	}
	rest := body[i+len("## Changes\n\n```\n"):]
	firstLine := rest[:strings.Index(rest, "\n")]
	if !strings.HasPrefix(firstLine, " ") {
		t.Fatalf("diffstat first line does not start with a space (D11.7): %q", firstLine)
	}
}

func TestResultNoteUnique(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	var stdout bytes.Buffer
	r := newTestRun(t, repo, "", Options{Stdout: &stdout}, sb)
	status := r.loop()

	path1, err := r.writeResultNote(status)
	if err != nil {
		t.Fatalf("writeResultNote 1: %v", err)
	}
	path2, err := r.writeResultNote(status)
	if err != nil {
		t.Fatalf("writeResultNote 2: %v", err)
	}
	if path1 == path2 {
		t.Fatalf("second writeResultNote reused the same path: %s", path1)
	}
	wantSuffix := "-2.md"
	if !strings.HasSuffix(path2, wantSuffix) {
		t.Fatalf("second path = %q, want suffix %q", path2, wantSuffix)
	}
	if !fileExistsT(path1) || !fileExistsT(path2) {
		t.Fatalf("both result notes should exist")
	}
}

func TestNotifyTextSuffixes(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.verify = func(iter int, dir string) error { return nil }
	r := newTestRun(t, repo, "max_iter: 1\n", Options{Stdout: &bytes.Buffer{}}, sb)
	r.loop()

	t.Run("sync owed", func(t *testing.T) {
		r.st.SyncOwed = true
		r.st.SyncRef = ""
		title, _ := r.notifyText(StatusError)
		if !strings.HasSuffix(title, "(sync failed)") {
			t.Fatalf("title = %q, want suffix '(sync failed)'", title)
		}
	})

	t.Run("sync pending", func(t *testing.T) {
		r.st.SyncOwed = false
		r.st.SyncRef = r.pendingSyncRef()
		title, _ := r.notifyText(StatusComplete)
		if !strings.HasSuffix(title, "(sync pending)") {
			t.Fatalf("title = %q, want suffix '(sync pending)'", title)
		}
	})

	t.Run("neither", func(t *testing.T) {
		r.st.SyncOwed = false
		r.st.SyncRef = "refs/heads/" + r.st.Branch
		title, msg := r.notifyText(StatusComplete)
		if strings.Contains(title, "(sync") {
			t.Fatalf("title = %q, want no sync suffix", title)
		}
		if msg != summaryLine(StatusComplete, r.st) {
			t.Fatalf("msg = %q, want the summary line", msg)
		}
	})
}

func TestFinishWritesNoteAndSummary(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 2 {
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		}
		return nil
	}
	sb.verify = func(iter int, dir string) error { return nil }

	var stdout bytes.Buffer
	r := newTestRun(t, repo, "", Options{Stdout: &stdout, Notifier: noopNotifierForTest{}}, sb)
	loopStatus := r.loop()

	finalStatus, err := r.finish(loopStatus)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if finalStatus != StatusComplete {
		t.Fatalf("finalStatus = %v, want Complete", finalStatus)
	}
	if r.st.Status != "complete" {
		t.Fatalf("run.json status = %q, want %q", r.st.Status, "complete")
	}
	if len(r.st.History) != 1 || !strings.HasSuffix(r.st.History[0], "complete") {
		t.Fatalf("History = %v", r.st.History)
	}

	out := stdout.String()
	if !strings.Contains(out, "spec demo: complete after 2 iteration(s), 1 commit(s) on spec/demo") {
		t.Fatalf("stdout missing summary line: %q", out)
	}
	if !strings.Contains(out, "result note: ") {
		t.Fatalf("stdout missing 'result note:' line: %q", out)
	}
}

// noopNotifierForTest avoids depending on internal/notify just to inject
// a do-nothing Notifier in these tests.
type noopNotifierForTest struct{}

func (noopNotifierForTest) Notify(title, msg string, urgent bool) {}
