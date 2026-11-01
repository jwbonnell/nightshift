package runner

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/agent"
	"github.com/jwbonnell/nightshift/internal/home"
)

// TestRunWithOpencodeCoder covers an opencode coder end to end through the
// Ralph loop: the prompt bytes it receives must be identical to what a
// claude run gets for the same spec (the model only changes which agent
// and sandbox template run it, never the prompt), the run must complete,
// and its history must record the opencode selector. It also checks, at
// the argv level, that two same-agent opencode roles still collapse to
// one agent sandbox, created with the opencode template.
func TestRunWithOpencodeCoder(t *testing.T) {
	repo := newSourceRepo(t)

	runSpec := func(t *testing.T) (*stubSandbox, *run, Status) {
		sb := newStubSandbox()
		sb.agent = completingAgent(t)
		sb.verify = alwaysGreenVerify
		r := newTestRun(t, repo, "", Options{Stdout: io.Discard, Notifier: noopNotifierForTest{}}, sb)
		status := r.loop()
		if _, err := r.finish(status); err != nil {
			t.Fatalf("finish: %v", err)
		}
		return sb, r, status
	}

	sbDefault, _, statusDefault := runSpec(t)

	// Setting the model via NIGHTSHIFT_MODEL (rather than frontmatter) keeps
	// the spec file itself byte-identical between the two runs, so any
	// prompt difference below could only come from the agent switch, not
	// from the spec body changing.
	t.Setenv("NIGHTSHIFT_MODEL", "opencode/openai/gpt-5")
	sbOpen, rOpen, statusOpen := runSpec(t)

	if statusDefault != StatusComplete || statusOpen != StatusComplete {
		t.Fatalf("status default=%v opencode=%v, want both Complete", statusDefault, statusOpen)
	}
	if !equalLines(sbDefault.prompts, sbOpen.prompts) {
		t.Fatalf("prompts differ:\ndefault: %q\nopencode: %q", sbDefault.prompts, sbOpen.prompts)
	}

	found := false
	for _, h := range rOpen.st.History {
		if strings.Contains(h, "agent=opencode/openai/gpt-5") {
			found = true
		}
	}
	if !found {
		t.Fatalf("history = %v, want an entry with agent=opencode/openai/gpt-5", rOpen.st.History)
	}

	opencodeAg := mustAgent(t, agent.Selector{Agent: "opencode", Model: "openai/gpt-5"}, agent.Settings{OpencodeBin: "opencode"})
	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"
	sbx := newSbxSandboxAgents(bin, "demo", opencodeAg, opencodeAg)
	if err := sbx.Create(mountDir, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got := readLog(t, logPath)
	want := []string{
		"rm -f nightshift-demo nightshift-demo-ctl nightshift-demo-verify nightshift-demo-gate",
		"create --name nightshift-demo opencode " + mountDir,
		"create --name nightshift-demo-ctl claude " + mountDir,
	}
	if !equalLines(got, want) {
		t.Fatalf("argv =\n%v\nwant\n%v", got, want)
	}
}

// TestAgentsRecordedAndChanged covers run.json's agents field and the
// start:/resume: log suffix, including the "agents changed:" line a
// resume logs when the code model changed since the run started. Both
// runs are capped at one iteration (a planning pass) so the test never
// has to drive the loop past planning.
func TestAgentsRecordedAndChanged(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	stateRoot := home.RunsDir(homeDir)
	writeReadySpec(t, homeDir, "demo", "max_iter: 1\n", repo)

	sb1 := newStubSandbox()
	sb1.verify = alwaysGreenVerify
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb1,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status1, err1 := Run(opts)
	if err1 != nil || status1 != StatusCapped {
		t.Fatalf("run1: status=%v err=%v, want Capped/nil", status1, err1)
	}

	st1 := readRunState(t, stateRoot, "demo")
	if st1.Agents == nil || st1.Agents.Plan != "claude/opus" || st1.Agents.Code != "claude/sonnet" {
		t.Fatalf("run1 agents = %+v, want plan=claude/opus code=claude/sonnet", st1.Agents)
	}

	logData1, err := os.ReadFile(filepath.Join(stateRoot, "demo", "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	if !strings.Contains(string(logData1), "agents=plan:claude/opus,code:claude/sonnet") {
		t.Fatalf("runner.log missing start agents suffix: %s", logData1)
	}

	writeReadySpec(t, homeDir, "demo", "max_iter: 1\nmodel: opencode/openai/gpt-5\n", repo)
	sb2 := newStubSandbox()
	sb2.verify = alwaysGreenVerify
	opts2 := opts
	opts2.Sandbox = sb2
	status2, err2 := Run(opts2)
	if err2 != nil || status2 != StatusCapped {
		t.Fatalf("resume: status=%v err=%v, want Capped/nil", status2, err2)
	}

	st2 := readRunState(t, stateRoot, "demo")
	if st2.Agents == nil || st2.Agents.Plan != "claude/opus" || st2.Agents.Code != "opencode/openai/gpt-5" {
		t.Fatalf("run2 agents = %+v, want plan=claude/opus code=opencode/openai/gpt-5", st2.Agents)
	}

	logData2, err := os.ReadFile(filepath.Join(stateRoot, "demo", "runner.log"))
	if err != nil {
		t.Fatalf("read runner.log: %v", err)
	}
	wantChanged := "agents changed: plan claude/opus→claude/opus, code claude/sonnet→opencode/openai/gpt-5"
	if !strings.Contains(string(logData2), wantChanged) {
		t.Fatalf("runner.log missing %q: %s", wantChanged, logData2)
	}
	if !strings.Contains(string(logData2), "agents=plan:claude/opus,code:opencode/openai/gpt-5") {
		t.Fatalf("runner.log missing resume agents suffix: %s", logData2)
	}
}

// TestDryRunShowsAgents covers --dry-run's agent lines (and the opencode
// cfg path) for a mixed claude-planner/opencode-coder spec, and confirms
// the config file's own content — a sentinel string standing in for
// anything secret-shaped — never reaches the output.
func TestDryRunShowsAgents(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()

	cfgPath := filepath.Join(t.TempDir(), "opencode.json")
	cfgBody := `{"provider":{"homelab":{"options":{"baseURL":"http://SENTINEL"}}}}`
	if err := os.WriteFile(cfgPath, []byte(cfgBody), 0o644); err != nil {
		t.Fatalf("write opencode config: %v", err)
	}
	t.Setenv("NIGHTSHIFT_OPENCODE_CONFIG", cfgPath)

	writeReadySpec(t, homeDir, "demo", "model: opencode/openai/gpt-5\n", repo)

	opts := Options{
		SpecArg:     "demo",
		Home:        homeDir,
		StateRoot:   home.RunsDir(homeDir),
		SbxBin:      "sbx",
		ClaudeBin:   "claude",
		OpencodeBin: "opencode",
	}
	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	var buf strings.Builder
	printPlan(&buf, opts, sp, 2, "default")
	out := buf.String()

	for _, want := range []string{
		"plan agent:  claude/opus\n",
		"code agent:  opencode/openai/gpt-5\n",
		"opencode cfg: " + cfgPath + "\n",
		"create --name nightshift-demo-plan claude " + stateDirFor(opts, sp) + "/repo",
		"create --name nightshift-demo opencode " + stateDirFor(opts, sp) + "/repo",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q\nfull output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SENTINEL") {
		t.Fatalf("dry-run output leaked opencode config contents:\n%s", out)
	}
}
