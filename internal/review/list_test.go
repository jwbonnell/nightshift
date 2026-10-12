package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/home"
)

// TestListTargetNoSideEffects is the P1-renamed reference
// TestListTargetNoSideEffects: --list on one explicit target makes only
// read-only forge GETs and prints the would-review line and footer,
// without ever touching sbx, the lock, or the state dir.
func TestListTargetNoSideEffects(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return testChange("target title", "a", "main", strings.Repeat("f", 40), strings.Repeat("0", 40)), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 1, Path: "team/app"}, nil
		},
	}
	sb := &stubSandbox{available: true}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 7}}
	opts.List = true
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	if !strings.Contains(stdout.String(), "team/app!7  would review  target title") {
		t.Errorf("stdout = %q, missing the would-review line", stdout.String())
	}
	if !strings.Contains(stdout.String(), "would review: 1 · queued: 0 · skipped: 0") {
		t.Errorf("stdout = %q, missing the expected footer tally", stdout.String())
	}

	if len(sb.createCalls) != 0 || sb.execCalls != 0 {
		t.Errorf("sandbox touched: create=%d exec=%d, want none", len(sb.createCalls), sb.execCalls)
	}
	if _, err := os.Stat(home.PRDir(homeDir)); !os.IsNotExist(err) {
		t.Errorf("state dir exists after --list, want it never created: %v", err)
	}
}

// TestListPendingNoSideEffects is the P1-renamed reference
// TestListPendingNoSideEffects: --pending --list classifies every listed
// change (skip reasons for draft/already-reviewed/non-open, would-review
// otherwise) without creating the state dir or touching the sandbox.
func TestListPendingNoSideEffects(t *testing.T) {
	homeDir := t.TempDir()
	shaReviewed := strings.Repeat("c", 40)

	writeMinimalNote(t, homeDir, Slug("team/reviewed", 2, "mr"), forge.KindGitLab, "team/reviewed", shaReviewed)

	paths := map[string]string{
		"10": "team/draft", "11": "team/reviewed", "12": "team/closed", "13": "team/four", "14": "team/five",
	}
	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "10", Label: "team/draft", Number: 1, Title: "draft title", Draft: true, State: "open", HeadSHA: strings.Repeat("a", 40)},
				{RepoRef: "11", Label: "team/reviewed", Number: 2, Title: "reviewed title", State: "open", HeadSHA: shaReviewed},
				{RepoRef: "12", Label: "team/closed", Number: 3, Title: "closed title", State: "closed", HeadSHA: strings.Repeat("b", 40)},
				{RepoRef: "13", Label: "team/four", Number: 4, Title: "four title", State: "open", HeadSHA: strings.Repeat("d", 40)},
				{RepoRef: "14", Label: "team/five", Number: 5, Title: "five title", State: "open", HeadSHA: strings.Repeat("e", 40)},
			}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			t.Fatalf("Get called for %s#%d — every listing already carries a head sha", r, n)
			return forge.Change{}, nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			id, _ := strconv.Atoi(r)
			return forge.Repo{ID: int64(id), Path: paths[r]}, nil
		},
	}
	sb := &stubSandbox{available: true}

	opts := pendingOptions(t, homeDir, client, sb)
	opts.List = true
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"team/draft!1  skip (draft)  draft title",
		"team/reviewed!2  skip (already reviewed at cccccccc)  reviewed title",
		"team/closed!3  skip (state closed)  closed title",
		"team/four!4  would review  four title",
		"team/five!5  would review  five title",
		"would review: 2 · queued: 0 · skipped: 3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, missing %q", out, want)
		}
	}

	if len(sb.createCalls) != 0 || sb.execCalls != 0 {
		t.Errorf("sandbox touched: create=%d exec=%d, want none", len(sb.createCalls), sb.execCalls)
	}
	if _, err := os.Stat(home.PRDir(homeDir)); !os.IsNotExist(err) {
		t.Errorf("state dir exists after --list, want it never created: %v", err)
	}
}

// TestListDryRunConflict is the P1-renamed reference TestListDryRunConflict.
func TestListDryRunConflict(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{kind: forge.KindGitLab}
	sb := &stubSandbox{available: true}

	opts := baseOptions(t, homeDir, client, sb)
	opts.List = true
	opts.DryRun = true
	var stdout, stderr strings.Builder
	opts.Stdout = &stdout
	opts.Stderr = &stderr

	if rc := Run(opts); rc != 2 {
		t.Fatalf("Run() = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "--list or --dry-run, not both") {
		t.Errorf("stderr = %q, missing the conflict message", stderr.String())
	}
	if strings.Contains(stdout.String(), "resolved config") {
		t.Errorf("stdout = %q, should never print the dry-run plan", stdout.String())
	}
}

// TestListLimitQueued is the P1-renamed reference TestListLimitQueued.
func TestListLimitQueued(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "1", Label: "team/one", Number: 1, Title: "one", State: "open", HeadSHA: strings.Repeat("a", 40)},
				{RepoRef: "2", Label: "team/two", Number: 2, Title: "two", State: "open", HeadSHA: strings.Repeat("b", 40)},
				{RepoRef: "3", Label: "team/three", Number: 3, Title: "three", State: "open", HeadSHA: strings.Repeat("c", 40)},
			}, nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			id, _ := strconv.Atoi(r)
			return forge.Repo{ID: int64(id), Path: fmt.Sprintf("team/%d", id)}, nil
		},
	}
	sb := &stubSandbox{available: true}

	opts := pendingOptions(t, homeDir, client, sb)
	opts.List = true
	opts.Limit = 1
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	out := stdout.String()
	if got := strings.Count(out, "  would review  "); got != 1 {
		t.Errorf("would-review lines = %d, want 1 (stdout: %s)", got, out)
	}
	if got := strings.Count(out, "queued (over --limit)"); got != 2 {
		t.Errorf("queued lines = %d, want 2 (stdout: %s)", got, out)
	}
	if !strings.Contains(out, "would review: 1 · queued: 2 · skipped: 0") {
		t.Errorf("stdout = %q, missing the expected footer tally", out)
	}
}

// TestListPendingCanonical is P8.1's new test: --list --pending resolves
// GetRepo for every listed change so the label and the already-reviewed
// check use the canonical path, not the listing's own (possibly
// differently-cased) one, and calls Get for a listing whose HeadSHA is
// empty (GitHub's search API never carries one).
func TestListPendingCanonical(t *testing.T) {
	homeDir := t.TempDir()
	shaX := strings.Repeat("a", 40)
	writeMinimalNote(t, homeDir, Slug("apps/x", 5, "mr"), forge.KindGitLab, "apps/x", shaX)

	gitlabClient := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "99", Label: "Apps/X", Number: 5, Title: "apps x", State: "open", HeadSHA: shaX}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			t.Fatalf("Get called for gitlab %s#%d — its listing already carries a head sha", r, n)
			return forge.Change{}, nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 7, Path: "apps/x"}, nil
		},
	}
	githubClient := &fakeClient{
		kind:          forge.KindGitHub,
		currentUserFn: func(ctx context.Context) (string, error) { return "bob", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{{RepoRef: "o/r", Label: "o/r", Number: 9, Title: "listed title", State: "open", HeadSHA: ""}}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return testChange("gh title", "b", "main", strings.Repeat("d", 40), strings.Repeat("0", 40)), nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return forge.Repo{ID: 50, Path: "o/r"}, nil
		},
	}

	sb := &stubSandbox{available: true}
	opts := baseOptions(t, homeDir, gitlabClient, sb)
	opts.Clients[forge.KindGitHub] = githubClient
	opts.Settings.GitHubToken = "github-test-token"
	opts.Pending = true
	opts.List = true
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "apps/x!5  skip (already reviewed at aaaaaaaa)  apps x") {
		t.Errorf("stdout = %q, want the canonical-path label and skip reason", out)
	}
	if !strings.Contains(out, "o/r#9  would review  gh title") {
		t.Errorf("stdout = %q, want a would-review line using the Get-fetched title", out)
	}
	if githubClient.getCalls != 1 {
		t.Errorf("github Get calls = %d, want 1 (the listing's empty HeadSHA must trigger one)", githubClient.getCalls)
	}
	if gitlabClient.getCalls != 0 {
		t.Errorf("gitlab Get calls = %d, want 0 (its listing already carried a head sha)", gitlabClient.getCalls)
	}
}

// TestListMatchesRun is the P1-renamed reference TestListMatchesRun:
// --list's "would review" lines name exactly the changes, in order, that
// a real --pending run sends to the reviewer — the skip-reasoned ones are
// never exec'd.
func TestListMatchesRun(t *testing.T) {
	homeDir := t.TempDir()
	shaReviewed := strings.Repeat("c", 40)
	writeMinimalNote(t, homeDir, Slug("team/reviewed", 2, "mr"), forge.KindGitLab, "team/reviewed", shaReviewed)

	remote4, head4, base4 := makeRemote(t, "refs/merge-requests/%d/head", 4)
	remote5, head5, base5 := makeRemote(t, "refs/merge-requests/%d/head", 5)

	repos := map[string]forge.Repo{
		"10": {ID: 10, Path: "team/draft"},
		"11": {ID: 11, Path: "team/reviewed"},
		"12": {ID: 12, Path: "team/closed"},
		"13": {ID: 13, Path: "team/four", CloneURL: remote4},
		"14": {ID: 14, Path: "team/five", CloneURL: remote5},
	}
	changes := map[int]forge.Change{
		1: {Title: "draft title", Draft: true, State: "open", HeadSHA: strings.Repeat("a", 40)},
		2: {Title: "reviewed title", State: "open", HeadSHA: shaReviewed},
		3: {Title: "closed title", State: "closed", HeadSHA: strings.Repeat("b", 40)},
		4: testChange("four title", "a", "main", head4, base4),
		5: testChange("five title", "a", "main", head5, base5),
	}
	client := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		listFn: func(ctx context.Context, user string) ([]forge.Listed, error) {
			return []forge.Listed{
				{RepoRef: "10", Label: "team/draft", Number: 1, Title: "draft title", Draft: true, State: "open", HeadSHA: strings.Repeat("a", 40)},
				{RepoRef: "11", Label: "team/reviewed", Number: 2, Title: "reviewed title", State: "open", HeadSHA: shaReviewed},
				{RepoRef: "12", Label: "team/closed", Number: 3, Title: "closed title", State: "closed", HeadSHA: strings.Repeat("b", 40)},
				{RepoRef: "13", Label: "team/four", Number: 4, Title: "four title", State: "open", HeadSHA: strings.Repeat("d", 40)},
				{RepoRef: "14", Label: "team/five", Number: 5, Title: "five title", State: "open", HeadSHA: strings.Repeat("e", 40)},
			}, nil
		},
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return changes[n], nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) { return repos[r], nil },
	}

	listSB := &stubSandbox{available: true}
	listOpts := pendingOptions(t, homeDir, client, listSB)
	listOpts.List = true
	var listStdout strings.Builder
	listOpts.Stdout = &listStdout

	if rc := Run(listOpts); rc != 0 {
		t.Fatalf("list Run() = %d, want 0 (stdout: %s)", rc, listStdout.String())
	}
	var wouldReview []string
	for _, line := range strings.Split(listStdout.String(), "\n") {
		if strings.Contains(line, "  would review  ") {
			wouldReview = append(wouldReview, strings.SplitN(line, " ", 2)[0])
		}
	}
	if want := []string{"team/four!4", "team/five!5"}; !strEqual(wouldReview, want) {
		t.Fatalf("would-review labels = %v, want %v", wouldReview, want)
	}

	var execDirs []string
	runSB := &stubSandbox{available: true}
	runSB.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		execDirs = append(execDirs, dir)
		return claudeResultJSON("LGTM", 0.1, false), nil
	}
	runOpts := pendingOptions(t, homeDir, client, runSB)
	var runStdout strings.Builder
	runOpts.Stdout = &runStdout

	if rc := Run(runOpts); rc != 0 {
		t.Fatalf("real Run() = %d, want 0 (stdout: %s)", rc, runStdout.String())
	}
	if len(execDirs) != 2 {
		t.Fatalf("exec calls = %d, want 2 (exactly the would-review items)", len(execDirs))
	}
	wantSlugs := []string{Slug("team/four", 4, "mr"), Slug("team/five", 5, "mr")}
	for i, dir := range execDirs {
		gotSlug := filepath.Base(filepath.Dir(dir))
		if gotSlug != wantSlugs[i] {
			t.Errorf("exec[%d] slug = %q, want %q", i, gotSlug, wantSlugs[i])
		}
	}
}

func strEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestDryRunPR checks printPlan's exact P4 block for one explicit GitHub
// target: every labeled value, the sandbox/policy/exec command lines, and
// that no token value and no file ever gets created.
func TestDryRunPR(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{kind: forge.KindGitHub}
	sb := &stubSandbox{available: true}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Clients = map[forge.Kind]forge.Client{forge.KindGitHub: client}
	opts.Settings.GitLabURL = ""
	opts.Settings.GitLabToken = ""
	opts.Settings.GitHubToken = "github-super-secret-token"
	opts.Settings.Egress = []string{"a.lan"}
	opts.Settings.MaxLines = 3000
	opts.Settings.MaxTurns = 30
	opts.Targets = []Target{{Forge: forge.KindGitHub, Repo: "o/r", Number: 1}}
	opts.RunID = "test123"
	opts.DryRun = true
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	out := stdout.String()

	for _, want := range []string{
		"nightshift pr --dry-run: resolved config\n",
		"  home:         " + homeDir + "\n",
		"  reviews:      " + home.ReviewsDir(homeDir) + "\n",
		"  mirror:       off\n",
		"  gitlab:       unset (token unset)\n",
		"  github:       https://github.com (token set)\n",
		"  state dir:    " + home.PRDir(homeDir) + "\n",
		"  model:        claude/sonnet\n",
		"  egress:       a.lan\n",
		"  max turns:    30\n",
		"  max lines:    3000\n",
		"  sbx bin:      sbx\n",
		"  prompt:       embedded\n",
		"targets:\n  o/r#1\n",
		"commands that would run:\n",
		"  sbx create --name nightshift-pr-test123 claude " + filepath.Join(home.PRDir(homeDir), "work") + "\n",
		"  sbx policy allow network --sandbox nightshift-pr-test123 a.lan\n",
		"  sbx exec -w " + filepath.Join(home.PRDir(homeDir), "work", Slug("o/r", 1, "pr"), "tree") + " nightshift-pr-test123 ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q\nfull output:\n%s", want, out)
		}
	}

	if strings.Contains(out, "github-super-secret-token") {
		t.Fatal("dry-run output contains the raw token")
	}
	if _, err := os.Stat(home.PRDir(homeDir)); !os.IsNotExist(err) {
		t.Errorf("state dir exists after --dry-run, want it never created: %v", err)
	}
	if len(sb.createCalls) != 0 {
		t.Errorf("sandbox create called %d times, want 0", len(sb.createCalls))
	}
}

// TestDryRunPendingMakesNoAPICallsMarker is the P1-renamed reference
// TestDryRunPendingMakesNoAPICallsMarker: --pending --dry-run exits 0,
// mentions "pending" in the plan, and never calls the forge (DryRun returns
// before Run ever reaches the pending-listing code path).
func TestDryRunPendingMakesNoAPICallsMarker(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{kind: forge.KindGitLab}
	sb := &stubSandbox{available: true}

	opts := pendingOptions(t, homeDir, client, sb)
	opts.DryRun = true
	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0 (stdout: %s)", rc, stdout.String())
	}
	if !strings.Contains(stdout.String(), "pending") {
		t.Errorf("stdout = %q, want it to mention pending", stdout.String())
	}
	if client.listCalls != 0 {
		t.Errorf("list calls = %d, want 0 (--dry-run makes no API calls)", client.listCalls)
	}
	if _, err := os.Stat(home.PRDir(homeDir)); !os.IsNotExist(err) {
		t.Errorf("state dir exists after --dry-run, want it never created: %v", err)
	}
}
