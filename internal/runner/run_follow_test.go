package runner

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

const followBashEvent = `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}` + "\n"

type lockedBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestRunFollowShowsActivity(t *testing.T) {
	old := followPoll
	followPoll = 5 * time.Millisecond
	t.Cleanup(func() { followPoll = old })

	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	sb.output = func(int) string {
		time.Sleep(30 * time.Millisecond) // let the poller observe it live
		return followBashEvent
	}
	var out lockedBuf
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Follow: true,
		Stdout: &out, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}
	s := out.String()
	runnerAt := strings.Index(s, "runner · ")
	hdrAt := strings.Index(s, "── iter 1 ")
	agentAt := strings.Index(s, "iter 1 · Bash go test ./...")
	sumAt := strings.Index(s, "spec demo: ")
	if runnerAt < 0 || hdrAt < 0 || agentAt < 0 || sumAt < 0 {
		t.Fatalf("missing pieces (runner=%d hdr=%d agent=%d summary=%d):\n%s", runnerAt, hdrAt, agentAt, sumAt, s)
	}
	if !(hdrAt < agentAt && agentAt < sumAt) {
		t.Fatalf("out of order:\n%s", s)
	}
	if last := strings.LastIndex(s, "runner · "); last > sumAt {
		t.Fatalf("follower line after summary:\n%s", s)
	}
}

func TestRunFollowIsLive(t *testing.T) {
	old := followPoll
	followPoll = 5 * time.Millisecond
	t.Cleanup(func() { followPoll = old })

	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

	var out lockedBuf
	done := completingAgent(t)
	sb := newStubSandbox()
	sb.verify = alwaysGreenVerify
	sb.output = func(int) string { return followBashEvent }
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 1 {
			// The agent line is already in agent.log; block until the
			// follower has shown it while the run is still going.
			deadline := time.Now().Add(5 * time.Second)
			for !strings.Contains(out.String(), "iter 1 · Bash") {
				if time.Now().After(deadline) {
					return errors.New("agent line never shown live")
				}
				time.Sleep(5 * time.Millisecond)
			}
		}
		return done(ctx, iter, dir)
	}
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Follow: true,
		Stdout: &out, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil:\n%s", status, err, out.String())
	}
	s := out.String()
	runnerAt := strings.Index(s, "runner · ")
	agentAt := strings.Index(s, "iter 1 · Bash")
	if runnerAt < 0 || agentAt < 0 || runnerAt > agentAt {
		t.Fatalf("runner line should precede agent line (runner=%d agent=%d):\n%s", runnerAt, agentAt, s)
	}
}

func TestRunFollowCoversReview(t *testing.T) {
	old := followPoll
	followPoll = 5 * time.Millisecond
	t.Cleanup(func() { followPoll = old })

	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "review: true\n", repo)

	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	var out lockedBuf
	opts := Options{
		SpecArg: "demo", Home: homeDir, Sandbox: sb, Follow: true,
		Stdout: &out, Stderr: io.Discard, Stdin: strings.NewReader(""), Notifier: noopNotifierForTest{},
		Reviewer: stubReviewer{text: "VERDICT: approve\nSUMMARY: ok.\n", duringReview: func(string) {
			time.Sleep(30 * time.Millisecond)
		}},
	}
	status, err := Run(opts)
	if err != nil || status != StatusComplete {
		t.Fatalf("status=%v err=%v, want Complete/nil", status, err)
	}
	s := out.String()
	revAt := strings.Index(s, "review: start")
	sumAt := strings.Index(s, "spec demo: ")
	if revAt < 0 || sumAt < 0 || revAt > sumAt {
		t.Fatalf("review line should precede summary (review=%d summary=%d):\n%s", revAt, sumAt, s)
	}
}

func TestRunFollowJobsPrefixes(t *testing.T) {
	old := followPoll
	followPoll = 5 * time.Millisecond
	t.Cleanup(func() { followPoll = old })

	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "a", "", repo)
	writeReadySpec(t, homeDir, "b", "", repo)
	stateRoot := home.RunsDir(homeDir)

	var calls []string
	var callsMu sync.Mutex
	inner := fakeSchedulerSpawner(t, stateRoot, nil, &calls, &callsMu)
	spawn := func(ctx context.Context, args []string, stdout, stderr *linePrefixWriter) (Waiter, error) {
		slug := slugFromRunArgs(args)
		dir := filepath.Join(stateRoot, slug)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		appendFile(t, filepath.Join(dir, "runner.log"), "2026-01-01 00:00:00 INFO hello\n")
		appendFile(t, filepath.Join(dir, "agent.log"),
			"=== iter 1 (session 1) model=m planning=false 2026-01-01T00:00:00Z ===\n"+followBashEvent)
		return inner(ctx, args, stdout, stderr)
	}

	var out lockedBuf
	opts := Options{
		Next: true, Follow: true, Home: homeDir, StateRoot: stateRoot,
		Stdout: &out, Stderr: io.Discard, Stdin: strings.NewReader(""), Spawn: spawn,
	}
	status, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	s := out.String()
	for _, want := range []string{"[a] runner · ", "[b] runner · ", "[a] iter 1 · Bash go test ./...", "[b] iter 1 · Bash go test ./..."} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %q in:\n%s", want, s)
		}
	}
	// Same fixture without -f, in a fresh home: same status.
	repo2 := newSourceRepo(t)
	home2 := t.TempDir()
	writeReadySpec(t, home2, "a", "", repo2)
	writeReadySpec(t, home2, "b", "", repo2)
	state2 := home.RunsDir(home2)
	var calls2 []string
	var calls2Mu sync.Mutex
	plain := Options{
		Next: true, Home: home2, StateRoot: state2,
		Stdout: io.Discard, Stderr: io.Discard, Stdin: strings.NewReader(""),
		Spawn: fakeSchedulerSpawner(t, state2, nil, &calls2, &calls2Mu),
	}
	want, err := Run(plain)
	if err != nil {
		t.Fatalf("Run without -f: %v", err)
	}
	if status != want {
		t.Fatalf("status with -f = %v, without = %v", status, want)
	}
}

func appendFile(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		t.Fatal(err)
	}
}
