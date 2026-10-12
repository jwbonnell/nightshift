package review

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/home"
)

// makeSizedRemote is makeRemote plus a change touching a file with lines
// changed lines, for size-cap tests.
func makeSizedRemote(t *testing.T, refTemplate string, n, lines int) (remote, head, base string) {
	t.Helper()
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-q", "-b", "main")

	work := t.TempDir()
	mustGit(t, work, "init", "-q", "-b", "main")
	mustGit(t, work, "commit", "-q", "--allow-empty", "-m", "base")
	base = mustGit(t, work, "rev-parse", "HEAD")
	mustGit(t, work, "remote", "add", "origin", bare)
	mustGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

	var b strings.Builder
	for i := 0; i < lines; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(work, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write big.txt: %v", err)
	}
	mustGit(t, work, "add", "-A")
	mustGit(t, work, "commit", "-q", "-m", "change")
	head = mustGit(t, work, "rev-parse", "HEAD")
	mustGit(t, work, "push", "-q", "origin", "HEAD:"+fmt.Sprintf(refTemplate, n))
	return bare, head, base
}

// fakeNotifier records every Notify call, for TestPendingNotifies.
type fakeNotifier struct {
	calls []struct {
		title, msg string
		urgent     bool
	}
}

func (f *fakeNotifier) Notify(title, msg string, urgent bool) {
	f.calls = append(f.calls, struct {
		title, msg string
		urgent     bool
	}{title, msg, urgent})
}

// pendingOptions builds Options for a --pending run with one gitlab
// fakeClient and a stub sandbox.
func pendingOptions(t *testing.T, homeDir string, client *fakeClient, sb *stubSandbox) Options {
	t.Helper()
	opts := baseOptions(t, homeDir, client, sb)
	opts.Pending = true
	opts.Agent = mustAgent(t, opts.Settings.Selector)
	return opts
}

func TestPendingSkipsDrafts(t *testing.T) {
	homeDir := t.TempDir()
	remote, head, base := makeRemote(t, "refs/merge-requests/%d/head", 2)
	change := testChange("Open one", "a", "main", head, base)
	repo := forge.Repo{ID: 1, Path: "team/app", CloneURL: remote}

	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "1", Label: "team/app", Number: 1, Title: "Draft one", Draft: true, State: "open"},
				{RepoRef: "1", Label: "team/app", Number: 2, Title: "Open one", Draft: false, State: "open"},
			}, nil
		},
		getFn:     func(ctx context.Context, r string, n int) (forge.Change, error) { return change, nil },
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) { return repo, nil },
	}
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := pendingOptions(t, homeDir, client, sb)
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !strings.Contains(stdout.String(), "team/app!1  skipped (draft)") {
		t.Fatalf("stdout = %q, want a draft skip for #1", stdout.String())
	}
	if client.getCalls != 1 {
		t.Fatalf("Get calls = %d, want 1 (the draft must never be fetched)", client.getCalls)
	}
	if !strings.Contains(stdout.String(), "reviewed: 1 · skipped: 1 · failed: 0") {
		t.Fatalf("stdout = %q, missing the expected summary", stdout.String())
	}
}

func TestLimitValidation(t *testing.T) {
	t.Run("negative limit", func(t *testing.T) {
		homeDir := t.TempDir()
		client := &fakeClient{kind: forge.KindGitLab}
		sb := &stubSandbox{available: true}
		opts := baseOptions(t, homeDir, client, sb)
		opts.Pending = true
		opts.Limit = -1
		var stderr strings.Builder
		opts.Stderr = &stderr

		if rc := Run(opts); rc != 2 {
			t.Fatalf("Run() = %d, want 2 (stderr: %s)", rc, stderr.String())
		}
		if want := "nightshift pr: --limit must be 0 (no limit) or a positive number of reviews\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
		if _, err := os.Stat(home.PRDir(homeDir)); !os.IsNotExist(err) {
			t.Errorf("state dir exists after a --limit validation error, want it never created: %v", err)
		}
	})

	t.Run("limit with an explicit target", func(t *testing.T) {
		homeDir := t.TempDir()
		client := &fakeClient{kind: forge.KindGitLab}
		sb := &stubSandbox{available: true}
		opts := baseOptions(t, homeDir, client, sb)
		opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
		opts.Limit = 3
		var stderr strings.Builder
		opts.Stderr = &stderr

		if rc := Run(opts); rc != 2 {
			t.Fatalf("Run() = %d, want 2 (stderr: %s)", rc, stderr.String())
		}
		if want := "nightshift pr: --limit only applies to --pending\n"; stderr.String() != want {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
		if _, err := os.Stat(home.PRDir(homeDir)); !os.IsNotExist(err) {
			t.Errorf("state dir exists after a --limit validation error, want it never created: %v", err)
		}
	})
}

func TestPendingLabelsUsePath(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "42", Label: "team/app", Number: 9, Title: "Draft", Draft: true, State: "open"},
			}, nil
		},
	}
	sb := &stubSandbox{available: true}
	opts := pendingOptions(t, homeDir, client, sb)
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !strings.Contains(stdout.String(), "team/app!9  skipped (draft)") {
		t.Fatalf("stdout = %q, want the listed path (not RepoRef) in the label", stdout.String())
	}
}

func TestPendingOldestFirstBothForges(t *testing.T) {
	homeDir := t.TempDir()

	remote1, head1, base1 := makeRemote(t, "refs/merge-requests/%d/head", 1)
	remote2, head2, base2 := makeRemote(t, "refs/pull/%d/head", 1)

	var order []string

	gitlabClient := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "1", Label: "team/app", Number: 1, Title: "GL one", State: "open"}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			order = append(order, "gitlab")
			return testChange("GL one", "a", "main", head1, base1), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 1, Path: "team/app", CloneURL: remote1}, nil
		},
	}
	githubClient := &fakeClient{
		kind:          forge.KindGitHub,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "owner/repo", Label: "owner/repo", Number: 1, Title: "GH one", State: "open"}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			order = append(order, "github")
			return testChange("GH one", "b", "main", head2, base2), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 2, Path: "owner/repo", CloneURL: remote2}, nil
		},
	}

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, gitlabClient, sb)
	opts.Clients[forge.KindGitHub] = githubClient
	opts.Settings.GitHubToken = "github-test-token"
	opts.Pending = true
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if len(order) != 2 || order[0] != "gitlab" || order[1] != "github" {
		t.Fatalf("order = %v, want [gitlab github]", order)
	}
	if !strings.Contains(stdout.String(), "reviewed: 2 · skipped: 0 · failed: 0") {
		t.Fatalf("stdout = %q, missing the expected summary", stdout.String())
	}
}

func TestPendingBothForges(t *testing.T) {
	homeDir := t.TempDir()
	remote1, head1, base1 := makeRemote(t, "refs/merge-requests/%d/head", 1)
	remote2, head2, base2 := makeRemote(t, "refs/merge-requests/%d/head", 2)
	remote3, head3, base3 := makeRemote(t, "refs/pull/%d/head", 1)

	gitlabClient := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "1", Label: "team/app", Number: 1, Title: "One", State: "open"},
				{RepoRef: "2", Label: "team/other", Number: 2, Title: "Two", State: "open"},
			}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			if n == 1 {
				return testChange("One", "a", "main", head1, base1), nil
			}
			return testChange("Two", "a", "main", head2, base2), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			if r == "1" {
				return forge.Repo{ID: 1, Path: "team/app", CloneURL: remote1}, nil
			}
			return forge.Repo{ID: 2, Path: "team/other", CloneURL: remote2}, nil
		},
	}
	githubClient := &fakeClient{
		kind:          forge.KindGitHub,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "owner/repo", Label: "owner/repo", Number: 1, Title: "GH", State: "open"}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return testChange("GH", "b", "main", head3, base3), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 3, Path: "owner/repo", CloneURL: remote3}, nil
		},
	}

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, gitlabClient, sb)
	opts.Clients[forge.KindGitHub] = githubClient
	opts.Settings.GitHubToken = "github-test-token"
	opts.Pending = true
	opts.Limit = 2
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !strings.Contains(stdout.String(), "limit reached: 1 change(s) left for the next run") {
		t.Fatalf("stdout = %q, want a limit-reached line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "reviewed: 2 · skipped: 0 · failed: 0 · left: 1") {
		t.Fatalf("stdout = %q, missing the expected limited summary", stdout.String())
	}
}

func TestPendingOneForgeFails(t *testing.T) {
	homeDir := t.TempDir()
	remote, head, base := makeRemote(t, "refs/pull/%d/head", 1)

	gitlabClient := &fakeClient{
		kind: forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("401 unauthorized")
		},
	}
	githubClient := &fakeClient{
		kind:          forge.KindGitHub,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "owner/repo", Label: "owner/repo", Number: 1, Title: "GH", State: "open"}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return testChange("GH", "b", "main", head, base), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 1, Path: "owner/repo", CloneURL: remote}, nil
		},
	}

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, gitlabClient, sb)
	opts.Clients[forge.KindGitHub] = githubClient
	opts.Settings.GitHubToken = "github-test-token"
	opts.Pending = true
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout, stderr strings.Builder
	opts.Stdout = &stdout
	opts.Stderr = &stderr

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if !strings.Contains(stderr.String(), "gitlab: resolve current user:") {
		t.Fatalf("stderr = %q, missing the gitlab failure message", stderr.String())
	}
	if !strings.Contains(stdout.String(), "reviewed: 1 · skipped: 0 · failed: 1") {
		t.Fatalf("stdout = %q, want the github change still reviewed", stdout.String())
	}
}

func TestNoForgeConfiguredPending(t *testing.T) {
	homeDir := t.TempDir()
	opts := Options{
		Settings: Settings{Home: homeDir, Selector: agent.Selector{Agent: "claude", Model: "sonnet"}, SbxBin: "sbx", Timeout: time.Second, MaxTurns: 1, MaxLines: 100, Notify: "off"},
		Pending:  true,
		Ctx:      context.Background(),
		Stdout:   io.Discard,
	}
	var stderr strings.Builder
	opts.Stderr = &stderr

	rc := Run(opts)
	if rc != 2 {
		t.Fatalf("Run() = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "no forge configured") {
		t.Fatalf("stderr = %q, missing the no-forge message", stderr.String())
	}
}

func TestPendingNotifies(t *testing.T) {
	homeDir := t.TempDir()
	remote, head, base := makeRemote(t, "refs/merge-requests/%d/head", 1)
	change := testChange("One", "a", "main", head, base)
	repo := forge.Repo{ID: 1, Path: "team/app", CloneURL: remote}

	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "1", Label: "team/app", Number: 1, Title: "One", State: "open"}}, nil
		},
		getFn:     func(ctx context.Context, r string, n int) (forge.Change, error) { return change, nil },
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) { return repo, nil },
	}
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("BLOCKING", 0.1, false), nil
	}

	opts := pendingOptions(t, homeDir, client, sb)
	notifier := &fakeNotifier{}
	opts.Notifier = notifier

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if len(notifier.calls) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(notifier.calls))
	}
	call := notifier.calls[0]
	if call.title != "nightshift pr" {
		t.Fatalf("title = %q, want nightshift pr", call.title)
	}
	if !strings.Contains(call.msg, "blocking: 1") {
		t.Fatalf("msg = %q, want a blocking: 1 suffix", call.msg)
	}
	if !call.urgent {
		t.Fatalf("urgent = false, want true for a BLOCKING verdict")
	}
}

func TestPendingNotifiesSkippedForExplicitTargets(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)
	notifier := &fakeNotifier{}
	opts.Notifier = notifier

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if len(notifier.calls) != 0 {
		t.Fatalf("notify calls = %d, want 0 for explicit targets", len(notifier.calls))
	}
}

func TestPendingGetFailureLogged(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "1", Label: "team/app", Number: 1, Title: "One", State: "open"}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return forge.Change{}, fmt.Errorf("network error")
		},
	}
	sb := &stubSandbox{available: true}
	opts := pendingOptions(t, homeDir, client, sb)
	var stdout strings.Builder
	opts.Stdout = &stdout

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if !strings.Contains(stdout.String(), "team/app!1  FAILED: network error") {
		t.Fatalf("stdout = %q, want a raw FAILED line", stdout.String())
	}

	logPath := filepath.Join(home.PRDir(homeDir), "logs", "failures.log")
	data := readFile(t, logPath)
	if !strings.Contains(data, "team-app-mr1") || !strings.Contains(data, "network error") {
		t.Fatalf("failures.log = %q, missing the expected entry", data)
	}
}

func TestCancelStopsPending(t *testing.T) {
	homeDir := t.TempDir()
	remote1, head1, base1 := makeRemote(t, "refs/merge-requests/%d/head", 1)
	remote2, head2, base2 := makeRemote(t, "refs/merge-requests/%d/head", 2)

	ctx, cancel := context.WithCancel(context.Background())

	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "1", Label: "team/app", Number: 1, Title: "One", State: "open"},
				{RepoRef: "2", Label: "team/other", Number: 2, Title: "Two", State: "open"},
			}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			if n == 1 {
				return testChange("One", "a", "main", head1, base1), nil
			}
			return testChange("Two", "a", "main", head2, base2), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			if r == "1" {
				return forge.Repo{ID: 1, Path: "team/app", CloneURL: remote1}, nil
			}
			return forge.Repo{ID: 2, Path: "team/other", CloneURL: remote2}, nil
		},
	}

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		cancel() // cancel the run's own ctx right after the first review starts
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Pending = true
	opts.Ctx = ctx
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout, stderr strings.Builder
	opts.Stdout = &stdout
	opts.Stderr = &stderr

	rc := Run(opts)
	if rc != 130 {
		t.Fatalf("Run() = %d, want 130", rc)
	}
	if !strings.Contains(stdout.String(), "interrupted: 1") {
		t.Fatalf("stdout = %q, want an interrupted: 1 summary suffix, not failed", stdout.String())
	}
	if strings.Contains(stdout.String(), "failed: 1") {
		t.Fatalf("stdout = %q, an interrupted change must not count as failed", stdout.String())
	}
	if !strings.Contains(stderr.String(), "interrupted — 2 change(s) not reviewed") {
		t.Fatalf("stderr = %q, missing the interrupted message", stderr.String())
	}
}

// TestLimitCountsOnlyExecAndFailedExec checks P12.4/4.4.8's --limit
// counting rule: only an outcome that actually reached Sandbox.Exec
// consumes a limit slot (a failed exec counts; a draft skip or a
// size-cap skip, both resolved before Exec, do not).
func TestLimitCountsOnlyExecAndFailedExec(t *testing.T) {
	homeDir := t.TempDir()

	remote3, head3, base3 := makeRemote(t, "refs/merge-requests/%d/head", 3)
	remoteSized, headSized, baseSized := makeSizedRemote(t, "refs/merge-requests/%d/head", 2, 20)

	item4Fetched := false

	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "1", Label: "team/draft", Number: 1, Title: "Draft", Draft: true, State: "open"},
				{RepoRef: "2", Label: "team/big", Number: 2, Title: "Big", State: "open"},
				{RepoRef: "3", Label: "team/app", Number: 3, Title: "Fails", State: "open"},
				{RepoRef: "4", Label: "team/next", Number: 4, Title: "Next", State: "open"},
			}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			switch n {
			case 2:
				return testChange("Big", "a", "main", headSized, baseSized), nil
			case 3:
				return testChange("Fails", "a", "main", head3, base3), nil
			case 4:
				item4Fetched = true
				return forge.Change{}, fmt.Errorf("should never be reached")
			}
			t.Fatalf("unexpected Get for change #%d", n)
			return forge.Change{}, nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			switch r {
			case "2":
				return forge.Repo{ID: 2, Path: "team/big", CloneURL: remoteSized}, nil
			case "3":
				return forge.Repo{ID: 3, Path: "team/app", CloneURL: remote3}, nil
			}
			t.Fatalf("unexpected GetRepo for %q", r)
			return forge.Repo{}, nil
		},
	}

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0, true), nil // is_error -> a failed review
	}

	opts := pendingOptions(t, homeDir, client, sb)
	opts.Settings.MaxLines = 5
	opts.Limit = 1

	var stdout strings.Builder
	opts.Stdout = &stdout

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1: %s", rc, stdout.String())
	}
	if item4Fetched {
		t.Fatalf("item 4 should never have been fetched once the limit was reached")
	}
	if sb.execCalls != 1 {
		t.Fatalf("execCalls = %d, want 1 (only the failing review's Exec counts)", sb.execCalls)
	}
	if !strings.Contains(stdout.String(), "limit reached: 1 change(s) left for the next run") {
		t.Fatalf("stdout = %q, want a limit-reached line for the one remaining item", stdout.String())
	}
}
