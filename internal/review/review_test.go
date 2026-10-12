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
	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/usagelimit"
)

// fakeClient implements forge.Client with func fields, one Kind per
// instance, counting calls so tests can assert what was (or wasn't)
// invoked.
type fakeClient struct {
	kind forge.Kind

	currentUserFn func(ctx context.Context) (string, error)
	getFn         func(ctx context.Context, repo string, n int) (forge.Change, error)
	getRepoFn     func(ctx context.Context, repo string) (forge.Repo, error)
	listFn        func(ctx context.Context, user string) ([]forge.Listed, error)

	getCalls     int
	getRepoCalls int
	currentCalls int
	listCalls    int
}

var _ forge.Client = (*fakeClient)(nil)

func (c *fakeClient) Kind() forge.Kind { return c.kind }

func (c *fakeClient) WebHost() string {
	if c.kind == forge.KindGitLab {
		return "gitlab.example.com"
	}
	return "github.com"
}

func (c *fakeClient) GitAuthUser() string {
	if c.kind == forge.KindGitLab {
		return "oauth2"
	}
	return "x-access-token"
}

func (c *fakeClient) SlugSuffix() string {
	if c.kind == forge.KindGitLab {
		return "mr"
	}
	return "pr"
}

func (c *fakeClient) Label(repoPath string, n int) string {
	if c.kind == forge.KindGitLab {
		return fmt.Sprintf("%s!%d", repoPath, n)
	}
	return fmt.Sprintf("%s#%d", repoPath, n)
}

func (c *fakeClient) HeadRefspec(n int) string {
	if c.kind == forge.KindGitLab {
		return fmt.Sprintf("+refs/merge-requests/%d/head:refs/cr/%d", n, n)
	}
	return fmt.Sprintf("+refs/pull/%d/head:refs/cr/%d", n, n)
}

func (c *fakeClient) CurrentUser(ctx context.Context) (string, error) {
	c.currentCalls++
	return c.currentUserFn(ctx)
}

func (c *fakeClient) Get(ctx context.Context, repo string, n int) (forge.Change, error) {
	c.getCalls++
	return c.getFn(ctx, repo, n)
}

func (c *fakeClient) GetRepo(ctx context.Context, repo string) (forge.Repo, error) {
	c.getRepoCalls++
	return c.getRepoFn(ctx, repo)
}

func (c *fakeClient) ListReviewRequests(ctx context.Context, user string) ([]forge.Listed, error) {
	c.listCalls++
	return c.listFn(ctx, user)
}

// stubSandbox implements Sandbox entirely in-process (no real sbx, no
// fake binary), recording every call for assertions and letting tests
// script Exec's behavior via execFn.
type stubSandbox struct {
	available bool

	createErr error
	allowErr  map[string]error

	createCalls []struct{ workDir, agent string }
	createEnvs  [][]string
	allowCalls  []string
	execCalls   int
	execEnvs    [][]string
	stopCalls   int
	removeCalls int

	execFn func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error)
}

func (s *stubSandbox) Available() bool { return s.available }

func (s *stubSandbox) Create(ctx context.Context, workDir, sbxAgent string, env []string) error {
	s.createCalls = append(s.createCalls, struct{ workDir, agent string }{workDir, sbxAgent})
	s.createEnvs = append(s.createEnvs, env)
	return s.createErr
}

func (s *stubSandbox) AllowNetwork(ctx context.Context, hostName string, env []string) error {
	s.allowCalls = append(s.allowCalls, hostName)
	if s.allowErr != nil {
		if err, ok := s.allowErr[hostName]; ok {
			return err
		}
	}
	return nil
}

func (s *stubSandbox) Exec(ctx context.Context, dir string, argv []string, stdin string, env []string, stderr io.Writer) (string, error) {
	s.execCalls++
	s.execEnvs = append(s.execEnvs, env)
	if s.execFn != nil {
		return s.execFn(ctx, s.execCalls, dir, argv, stdin)
	}
	return "", nil
}

func (s *stubSandbox) Stop(env []string) error {
	s.stopCalls++
	return nil
}

func (s *stubSandbox) Remove(env []string) error {
	s.removeCalls++
	return nil
}

var _ Sandbox = (*stubSandbox)(nil)

// testSettings returns a minimal, fast Settings suitable for driving
// Run() directly in tests (bypassing ResolveSettings).
func testSettings(homeDir string) Settings {
	return Settings{
		Home:          homeDir,
		Model:         "sonnet",
		ModelSrc:      "default",
		Selector:      agent.Selector{Agent: "claude", Model: "sonnet"},
		MaxTurns:      5,
		Timeout:       5 * time.Second,
		MaxLines:      3000,
		SbxBin:        "sbx",
		ClaudeBin:     "claude",
		OpencodeBin:   "opencode",
		GitLabURL:     "https://gitlab.example.com",
		GitLabToken:   "gitlab-test-token",
		GitHubURL:     "https://github.com",
		Notify:        "off",
		LimitFallback: time.Hour,
	}
}

func noSleep(ctx context.Context, d time.Duration) error { return nil }

// testChange builds a minimal open, non-draft forge.Change ready to
// review.
func testChange(title, author, target, headSHA, baseSHA string) forge.Change {
	return forge.Change{
		Title:        title,
		State:        "open",
		Description:  "a description",
		HeadSHA:      headSHA,
		DiffBaseSHA:  baseSHA,
		TargetBranch: target,
		Author:       author,
		WebURL:       "https://example.invalid/change/1",
		CreatedAt:    "2024-01-01T00:00:00Z",
	}
}

func claudeResultJSON(verdict string, cost float64, isError bool) string {
	return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":%v,"result":"VERDICT: %s\nOne sentence summary.","total_cost_usd":%g}`, isError, verdict, cost)
}

func claudeRateLimitJSON() string {
	return `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your session limit · resets 7am (UTC)","total_cost_usd":0}`
}

func opencodeSuccessJSON(verdict string) string {
	return `{"type":"text","part":{"text":"VERDICT: ` + verdict + `\nOne sentence summary."}}` + "\n" +
		`{"type":"step_finish","part":{}}`
}

func opencodeErrorJSON() string {
	return `{"type":"error","message":"rate limit exceeded","status":429}`
}

// baseOptions builds Options for one test, wired to a single gitlab
// fakeClient and a stub sandbox, with no sleeping and a fixed clock.
func baseOptions(t *testing.T, homeDir string, client *fakeClient, sb *stubSandbox) Options {
	t.Helper()
	return Options{
		Settings:     testSettings(homeDir),
		Ctx:          context.Background(),
		Sandbox:      sb,
		Clients:      map[forge.Kind]forge.Client{forge.KindGitLab: client},
		AllowFileURL: true,
		Now:          func() time.Time { return time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC) },
		Stdout:       io.Discard,
		Stderr:       io.Discard,
		Sleep:        noSleep,
	}
}

func mustAgent(t *testing.T, sel agent.Selector) agent.Agent {
	t.Helper()
	ag, err := agent.New(sel, agent.Settings{})
	if err != nil {
		t.Fatalf("agent.New: %v", err)
	}
	return ag
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// gitlabChangeClient builds a fakeClient wired to one real git remote
// (makeRemote) for a single GitLab change, project id 1.
func gitlabChangeClient(t *testing.T, n int, title, author string) (*fakeClient, string /*remote*/, string /*head*/, string /*base*/) {
	t.Helper()
	remote, head, base := makeRemote(t, "refs/merge-requests/%d/head", n)
	change := testChange(title, author, "main", head, base)
	repo := forge.Repo{ID: 1, Path: "team/app", CloneURL: remote}
	c := &fakeClient{
		kind:          forge.KindGitLab,
		currentUserFn: func(ctx context.Context) (string, error) { return "alice", nil },
		getFn:         func(ctx context.Context, repoArg string, num int) (forge.Change, error) { return change, nil },
		getRepoFn:     func(ctx context.Context, repoArg string) (forge.Repo, error) { return repo, nil },
	}
	return c, remote, head, base
}

func TestPRClaudeArgvGolden(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 12, "Fix auth", "bob")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.41, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 12}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}

	if len(sb.createCalls) != 1 || sb.createCalls[0].agent != "claude" {
		t.Fatalf("create calls = %+v, want one claude create", sb.createCalls)
	}
	if sb.execCalls != 1 {
		t.Fatalf("execCalls = %d, want 1", sb.execCalls)
	}

	wantArgv := []string{
		"claude", "-p",
		"--output-format", "json",
		"--model", "sonnet",
		"--max-turns", "5",
		"--tools", "Read,Grep,Glob",
		"--permission-mode", "dontAsk",
		"--no-session-persistence",
		"--setting-sources", "user",
		"--disable-slash-commands",
	}
	gotArgv := opts.Agent.Argv(agent.Invocation{Role: agent.RoleReview, Model: "sonnet", ReadOnly: true, MaxTurns: 5, Hardened: true})
	if len(gotArgv) != len(wantArgv) {
		t.Fatalf("argv = %q, want %q", gotArgv, wantArgv)
	}
	for i := range wantArgv {
		if gotArgv[i] != wantArgv[i] {
			t.Fatalf("argv[%d] = %q, want %q (full: %q)", i, gotArgv[i], wantArgv[i], gotArgv)
		}
	}

	notePath := filepath.Join(home.ReviewsDir(homeDir), "team-app-mr12.md")
	content := readFile(t, notePath)
	if !strings.Contains(content, "verdict: LGTM") || !strings.Contains(content, "cost_usd: 0.41") {
		t.Fatalf("note content = %s, missing verdict/cost", content)
	}
}

func TestPROpencodeReviewer(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 3, "Add feature", "carol")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return opencodeSuccessJSON("CONCERNS"), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 3}}
	opts.Settings.Selector = agent.Selector{Agent: "opencode", Model: "opencode/longcat-2.5-preview-free"}
	opts.Settings.Egress = []string{"opencode.ai"}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}

	if len(sb.createCalls) != 1 || sb.createCalls[0].agent != "opencode" {
		t.Fatalf("create calls = %+v, want one opencode create", sb.createCalls)
	}
	if len(sb.allowCalls) != 1 || sb.allowCalls[0] != "opencode.ai" {
		t.Fatalf("allow calls = %v, want [opencode.ai] before the first exec", sb.allowCalls)
	}

	notePath := filepath.Join(home.ReviewsDir(homeDir), "team-app-mr3.md")
	content := readFile(t, notePath)
	if !strings.Contains(content, "verdict: CONCERNS") {
		t.Fatalf("note content = %s, missing verdict", content)
	}
	if !strings.Contains(content, "cost_usd: n/a") {
		t.Fatalf("note content = %s, want cost_usd: n/a", content)
	}
	if !strings.Contains(content, "model: opencode/opencode/longcat-2.5-preview-free") {
		t.Fatalf("note content = %s, missing model", content)
	}
}

func TestPREgressRunLevelClaudeNoPolicyCalls(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Settings.Egress = nil
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if len(sb.allowCalls) != 0 {
		t.Fatalf("allowCalls = %v, want none for claude with no egress", sb.allowCalls)
	}
}

func TestPREgressRunLevelPolicyFailureExitsOneAndRemovesSandbox(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	sb := &stubSandbox{available: true, allowErr: map[string]error{"a.lan": fmt.Errorf("boom")}}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Settings.Egress = []string{"a.lan"}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if sb.execCalls != 0 {
		t.Fatalf("execCalls = %d, want 0 (the egress failure must abort before Exec)", sb.execCalls)
	}
	if sb.removeCalls != 1 {
		t.Fatalf("removeCalls = %d, want 1 (sandbox must still be removed)", sb.removeCalls)
	}
}

func TestClaudeIsErrorFails(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0, true), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	notePath := filepath.Join(home.ReviewsDir(homeDir), "team-app-mr1.md")
	if _, err := os.Stat(notePath); err == nil {
		t.Fatalf("note was written for a failed (is_error) review")
	}
}

func TestPRTransientRetryOnce(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return opencodeErrorJSON(), nil
	}

	slept := 0
	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Settings.Selector = agent.Selector{Agent: "opencode", Model: "opencode/longcat-2.5-preview-free"}
	opts.Agent = mustAgent(t, opts.Settings.Selector)
	opts.Sleep = func(ctx context.Context, d time.Duration) error {
		slept++
		if d != time.Minute {
			t.Fatalf("Sleep(%v), want 1m", d)
		}
		return nil
	}

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if sb.execCalls != 2 {
		t.Fatalf("execCalls = %d, want 2 (one retry)", sb.execCalls)
	}
	if slept != 1 {
		t.Fatalf("slept %d times, want 1", slept)
	}
}

func TestPRRateLimitStopsPass(t *testing.T) {
	homeDir := t.TempDir()
	remote1, head1, base1 := makeRemote(t, "refs/merge-requests/%d/head", 1)
	remote2, head2, base2 := makeRemote(t, "refs/merge-requests/%d/head", 2)

	repos := map[int]forge.Repo{1: {ID: 1, Path: "team/app", CloneURL: remote1}, 2: {ID: 2, Path: "team/other", CloneURL: remote2}}
	changes := map[int]forge.Change{
		1: testChange("First", "a", "main", head1, base1),
		2: testChange("Second", "b", "main", head2, base2),
	}
	client := &fakeClient{
		kind: forge.KindGitLab,
		getFn: func(ctx context.Context, repo string, n int) (forge.Change, error) {
			return changes[n], nil
		},
		getRepoFn: func(ctx context.Context, repo string) (forge.Repo, error) {
			if repo == "team/app" {
				return repos[1], nil
			}
			return repos[2], nil
		},
	}

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeRateLimitJSON(), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{
		{Forge: forge.KindGitLab, Repo: "team/app", Number: 1},
		{Forge: forge.KindGitLab, Repo: "team/other", Number: 2},
	}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	rc := Run(opts)
	if rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if sb.execCalls != 1 {
		t.Fatalf("execCalls = %d, want 1 (the pass must stop at the first rate limit)", sb.execCalls)
	}
	if client.getCalls != 1 {
		t.Fatalf("Get calls = %d, want 1 (the second target must never be fetched)", client.getCalls)
	}

	marker := usagelimit.ReadActive(home.RunsDir(homeDir), opts.Now().Add(-time.Hour), false)
	if marker == nil {
		t.Fatalf("expected a usage-limit marker to be written")
	}
}

func TestPRLimitMarkerSkipsClaude(t *testing.T) {
	homeDir := t.TempDir()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := usagelimit.Write(home.RunsDir(homeDir), usagelimit.Hit{ResetAt: now.Add(time.Hour)}, now, "pr", time.Hour); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")
	sb := &stubSandbox{available: true}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)
	opts.Now = func() time.Time { return now }

	var stderr strings.Builder
	opts.Stderr = &stderr

	rc := Run(opts)
	if rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !strings.Contains(stderr.String(), "usage limit active until") {
		t.Fatalf("stderr = %q, want a usage-limit message", stderr.String())
	}
	if client.getCalls != 0 {
		t.Fatalf("Get calls = %d, want 0 (no API calls under an active marker)", client.getCalls)
	}
}

func TestPRLimitMarkerIgnoredByOpencode(t *testing.T) {
	homeDir := t.TempDir()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	if err := usagelimit.Write(home.RunsDir(homeDir), usagelimit.Hit{ResetAt: now.Add(time.Hour)}, now, "pr", time.Hour); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return opencodeSuccessJSON("LGTM"), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Settings.Selector = agent.Selector{Agent: "opencode", Model: "opencode/longcat-2.5-preview-free"}
	opts.Agent = mustAgent(t, opts.Settings.Selector)
	opts.Now = func() time.Time { return now }

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if client.getCalls != 1 {
		t.Fatalf("Get calls = %d, want 1 (opencode must ignore the claude-only marker)", client.getCalls)
	}
}

func TestRefuseWithoutSbx(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")
	sb := &stubSandbox{available: false}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stderr strings.Builder
	opts.Stderr = &stderr

	rc := Run(opts)
	if rc != 2 {
		t.Fatalf("Run() = %d, want 2", rc)
	}
	if !strings.Contains(stderr.String(), "never runs the reviewer on the host") {
		t.Fatalf("stderr = %q, missing expected message", stderr.String())
	}
}

func TestStartupClearsWork(t *testing.T) {
	homeDir := t.TempDir()
	leftover := filepath.Join(home.PRDir(homeDir), "work", "leftover")
	if err := os.MkdirAll(leftover, 0o700); err != nil {
		t.Fatalf("seed leftover work dir: %v", err)
	}

	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("leftover work dir should have been cleared at startup")
	}
}

func TestSandboxRemovedOnError(t *testing.T) {
	homeDir := t.TempDir()
	client := &fakeClient{
		kind: forge.KindGitLab,
		getFn: func(ctx context.Context, repo string, n int) (forge.Change, error) {
			return forge.Change{}, fmt.Errorf("boom")
		},
	}
	sb := &stubSandbox{available: true}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if sb.removeCalls != 1 {
		t.Fatalf("removeCalls = %d, want 1", sb.removeCalls)
	}
}

func TestTimeoutKillsReviewAndStopsSandbox(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Settings.Timeout = 20 * time.Millisecond
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if sb.stopCalls != 1 {
		t.Fatalf("stopCalls = %d, want 1", sb.stopCalls)
	}
	if !strings.Contains(stdout.String(), "FAILED:") || !strings.Contains(stdout.String(), "timed out") {
		t.Fatalf("stdout = %q, want a timeout FAILED line", stdout.String())
	}
}

func TestNoCheckboxesInNoteBody(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return fmt.Sprintf(`{"type":"result","subtype":"success","is_error":false,"result":"VERDICT: LGTM\n- [ ] one\n* [x] two","total_cost_usd":0.1}`), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	content := readFile(t, filepath.Join(home.ReviewsDir(homeDir), "team-app-mr1.md"))
	if strings.Contains(content, "[ ]") || strings.Contains(content, "[x]") {
		t.Fatalf("note body still has checkboxes: %s", content)
	}
}

func TestSizeCapSkippedNoSandbox(t *testing.T) {
	homeDir := t.TempDir()
	bare := t.TempDir()
	mustGit(t, bare, "init", "--bare", "-q", "-b", "main")

	work := t.TempDir()
	mustGitInit(t, work)
	mustGit(t, work, "commit", "-q", "--allow-empty", "-m", "base")
	base := mustGit(t, work, "rev-parse", "HEAD")
	mustGit(t, work, "remote", "add", "origin", bare)
	mustGit(t, work, "push", "-q", "origin", "HEAD:refs/heads/main")

	// Write a file with more than MaxLines changed lines.
	var b strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	if err := os.WriteFile(filepath.Join(work, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write big.txt: %v", err)
	}
	mustGit(t, work, "add", "-A")
	mustGit(t, work, "commit", "-q", "-m", "big change")
	head := mustGit(t, work, "rev-parse", "HEAD")
	mustGit(t, work, "push", "-q", "origin", "HEAD:refs/merge-requests/1/head")

	change := testChange("Big", "a", "main", head, base)
	repo := forge.Repo{ID: 1, Path: "team/app", CloneURL: bare}
	client := &fakeClient{
		kind:      forge.KindGitLab,
		getFn:     func(ctx context.Context, r string, n int) (forge.Change, error) { return change, nil },
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) { return repo, nil },
	}
	sb := &stubSandbox{available: true}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Settings.MaxLines = 5
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if sb.execCalls != 0 || len(sb.createCalls) != 0 {
		t.Fatalf("sandbox should never be created/exec'd for a size-cap skip")
	}
	if !strings.Contains(stdout.String(), "SKIPPED (size)") {
		t.Fatalf("stdout = %q, want SKIPPED (size)", stdout.String())
	}
	content := readFile(t, filepath.Join(home.ReviewsDir(homeDir), "team-app-mr1.md"))
	if !strings.Contains(content, "verdict: SKIPPED") || !strings.Contains(content, "cost_usd: 0.00") {
		t.Fatalf("note content = %s, want SKIPPED verdict and cost_usd: 0.00", content)
	}
}

func TestSkipNotOpenedAndAlreadyReviewed(t *testing.T) {
	homeDir := t.TempDir()
	client, _, head, _ := gitlabChangeClient(t, 1, "T", "a")

	// Closed, not forced: skipped, no clone/sandbox work needed.
	closed := testChange("T", "a", "main", head, "")
	closed.State = "closed"
	client.getFn = func(ctx context.Context, r string, n int) (forge.Change, error) { return closed, nil }

	sb := &stubSandbox{available: true}
	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout
	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !strings.Contains(stdout.String(), "skipped (state closed)") {
		t.Fatalf("stdout = %q, want a state-closed skip", stdout.String())
	}

	// Now write a note at that head sha directly and re-review the open
	// change: already-reviewed skip.
	d := noteData{Kind: forge.KindGitLab, Slug: "team-app-mr1", Label: "team/app!1", Project: "team/app",
		Number: 1, Title: "T", Author: "a", URL: "https://x", Head: head, Base: "b",
		Verdict: "LGTM", Lines: 1, CostKnown: true, Model: "sonnet", Body: "ok"}
	if _, _, err := writeNote(homeDir, d, time.Now()); err != nil {
		t.Fatalf("writeNote: %v", err)
	}

	open := testChange("T", "a", "main", head, "")
	client.getFn = func(ctx context.Context, r string, n int) (forge.Change, error) { return open, nil }
	stdout.Reset()
	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !strings.Contains(stdout.String(), "already reviewed at") {
		t.Fatalf("stdout = %q, want an already-reviewed skip", stdout.String())
	}
}

func TestTargetUsesCanonicalPath(t *testing.T) {
	homeDir := t.TempDir()
	remote, head, base := makeRemote(t, "refs/merge-requests/%d/head", 1)
	change := testChange("T", "a", "main", head, base)
	repo := forge.Repo{ID: 1, Path: "Team/App", CloneURL: remote}
	client := &fakeClient{
		kind:      forge.KindGitLab,
		getFn:     func(ctx context.Context, r string, n int) (forge.Change, error) { return change, nil },
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) { return repo, nil },
	}
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	// Typed differently from the canonical path on purpose.
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/APP", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	notePath := filepath.Join(home.ReviewsDir(homeDir), "Team-App-mr1.md")
	content := readFile(t, notePath)
	if !strings.Contains(content, `project: "Team/App"`) {
		t.Fatalf("note content = %s, want the canonical project path, not the typed one", content)
	}
}

func TestHomeCommitOnReview(t *testing.T) {
	homeDir := t.TempDir()
	mustGitInit(t, homeDir)
	mustGit(t, homeDir, "commit", "-q", "--allow-empty", "-m", "init")

	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}
	if !gitx.IsRepo(homeDir) {
		t.Fatalf("home should still be a git repo")
	}
	log, err := gitx.Run(homeDir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	if log != "review: team/app!1 (LGTM)" {
		t.Fatalf("last commit message = %q", log)
	}
}

// mustGitInit and mustGit are small helpers for tests that need a plain
// git repo without makeRemote's two-commit MR shape.
func mustGitInit(t *testing.T, dir string) {
	t.Helper()
	mustGit(t, dir, "init", "-q", "-b", "main")
}

func TestSandboxEnvScrubbed(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	t.Setenv("NIGHTSHIFT_GITLAB_URL", "https://gitlab.example.com")
	t.Setenv("SOME_OTHER_VAR", "contains-gitlab-test-token-in-it")

	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0", rc)
	}

	check := func(env []string) {
		for _, e := range env {
			if strings.HasPrefix(e, "NIGHTSHIFT_GITLAB_") {
				t.Fatalf("env entry %q should have been scrubbed", e)
			}
			if strings.Contains(e, "gitlab-test-token") {
				t.Fatalf("env entry %q leaks the token", e)
			}
		}
	}
	for _, env := range append(append([][]string{}, sb.createEnvs...), sb.execEnvs...) {
		check(env)
	}
}

func TestSlugCollisionFailsReview(t *testing.T) {
	homeDir := t.TempDir()
	client, _, _, _ := gitlabChangeClient(t, 1, "T", "a")

	// Both the base slug and its "-p<id>" suffix are owned by an
	// unrelated project, so preCloneSkip has nowhere left to fall back
	// to (P8.2).
	writeMinimalNote(t, homeDir, "team-app-mr1", forge.KindGitHub, "someone/else", "")
	writeMinimalNote(t, homeDir, "team-app-mr1-p1", forge.KindGitHub, "someone/else-too", "")

	sb := &stubSandbox{available: true}
	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{{Forge: forge.KindGitLab, Repo: "team/app", Number: 1}}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout

	rc := Run(opts)
	if rc != 1 {
		t.Fatalf("Run() = %d, want 1", rc)
	}
	if sb.execCalls != 0 || len(sb.createCalls) != 0 {
		t.Fatalf("a slug collision must fail before any sandbox work")
	}
	if !strings.Contains(stdout.String(), "FAILED:") || !strings.Contains(stdout.String(), "collides with") {
		t.Fatalf("stdout = %q, want a collision FAILED line", stdout.String())
	}
}

func TestCaseOnlyDifferenceNotCollision(t *testing.T) {
	// Slug() is deliberately case-preserving (not case-folding), so two
	// genuinely different projects whose canonical paths differ only in
	// letter case (e.g. two distinct GitLab projects with different
	// RepoIDs) land at two different note slugs — never forced through
	// the collision-suffix logic in the first place.
	homeDir := t.TempDir()
	remote1, head1, base1 := makeRemote(t, "refs/merge-requests/%d/head", 1)
	remote2, head2, base2 := makeRemote(t, "refs/merge-requests/%d/head", 1)

	repos := map[string]forge.Repo{
		"team/app": {ID: 1, Path: "team/app", CloneURL: remote1},
		"Team/App": {ID: 2, Path: "Team/App", CloneURL: remote2},
	}
	changes := map[string]forge.Change{
		"team/app": testChange("Lower", "a", "main", head1, base1),
		"Team/App": testChange("Upper", "b", "main", head2, base2),
	}
	client := &fakeClient{
		kind: forge.KindGitLab,
		getFn: func(ctx context.Context, r string, n int) (forge.Change, error) {
			return changes[r], nil
		},
		getRepoFn: func(ctx context.Context, r string) (forge.Repo, error) {
			return repos[r], nil
		},
	}
	sb := &stubSandbox{available: true}
	sb.execFn = func(ctx context.Context, call int, dir string, argv []string, stdin string) (string, error) {
		return claudeResultJSON("LGTM", 0.1, false), nil
	}

	opts := baseOptions(t, homeDir, client, sb)
	opts.Targets = []Target{
		{Forge: forge.KindGitLab, Repo: "team/app", Number: 1},
		{Forge: forge.KindGitLab, Repo: "Team/App", Number: 1},
	}
	opts.Agent = mustAgent(t, opts.Settings.Selector)

	var stdout strings.Builder
	opts.Stdout = &stdout
	if rc := Run(opts); rc != 0 {
		t.Fatalf("Run() = %d, want 0: %s", rc, stdout.String())
	}
	if strings.Contains(stdout.String(), "FAILED") || strings.Contains(stdout.String(), "collides") {
		t.Fatalf("stdout = %q, two distinct projects must never collide", stdout.String())
	}

	if _, err := os.Stat(filepath.Join(home.ReviewsDir(homeDir), "team-app-mr1.md")); err != nil {
		t.Fatalf("missing team-app-mr1.md note: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home.ReviewsDir(homeDir), "Team-App-mr1.md")); err != nil {
		t.Fatalf("missing Team-App-mr1.md note: %v", err)
	}
}
