package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/runner"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func waitFor(t *testing.T, b *syncBuf, sub string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), sub) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in:\n%s", sub, b.String())
}

type logsEnv struct {
	runs   string
	active map[string]*atomic.Bool
	polls  atomic.Int64
}

func newLogsEnv(t *testing.T) *logsEnv {
	t.Helper()
	h := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", h)
	e := &logsEnv{runs: filepath.Join(h, "runs"), active: map[string]*atomic.Bool{}}
	t.Cleanup(func() { logsOptsHook = nil })
	logsOptsHook = func(o *runner.LogsOptions) {
		o.Poll = time.Millisecond
		o.Loc = time.UTC
		o.Active = func(slug string) bool { b := e.active[slug]; return b != nil && b.Load() }
		o.Sleep = func(ctx context.Context, d time.Duration) error {
			e.polls.Add(1)
			time.Sleep(d)
			return ctx.Err()
		}
	}
	return e
}

func (e *logsEnv) mk(t *testing.T, slug, status string, active bool) string {
	t.Helper()
	d := filepath.Join(e.runs, slug)
	if err := os.MkdirAll(filepath.Join(d, "attempts"), 0o755); err != nil {
		t.Fatal(err)
	}
	e.write(t, slug, "run.json", fmt.Sprintf(`{"slug":%q,"status":%q}`, slug, status))
	b := &atomic.Bool{}
	b.Store(active)
	e.active[slug] = b
	return d
}

func (e *logsEnv) write(t *testing.T, slug, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(e.runs, slug, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *logsEnv) appendTo(t *testing.T, slug, name, content string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(e.runs, slug, name), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

func runLogs(args ...string) (string, string, int) {
	var out, errb bytes.Buffer
	code := dispatch(append([]string{"logs"}, args...), &out, &errb, strings.NewReader(""))
	return out.String(), errb.String(), code
}

func numbered(n int) string {
	var sb strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	return sb.String()
}

const agentFixture = `=== iter 1 (session 1) model=m planning=true 2026-10-25T10:00:00Z ===
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}
=== iter 2 (session 2) model=m planning=false 2026-10-25T10:05:00Z ===
{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}
=== iter 2 (session 3) model=m planning=false 2026-10-25T10:06:00Z ===
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"foo"}}]}}
`

func TestLogsTail(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "demo", "complete", false)
	e.write(t, "demo", "runner.log", numbered(50))
	e.write(t, "demo", "agent.log", agentFixture)

	out, _, code := runLogs("demo")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 40 || lines[0] != "line 11" || lines[39] != "line 50" {
		t.Fatalf("default: code=%d lines=%d\n%s", code, len(lines), out)
	}
	out, _, _ = runLogs("demo", "-n", "3")
	if out != "line 48\nline 49\nline 50\n" {
		t.Fatalf("-n 3: %q", out)
	}
	out, _, _ = runLogs("demo", "-n", "0")
	if strings.Count(out, "\n") != 50 {
		t.Fatalf("-n 0: %q", out)
	}

	out, _, _ = runLogs("demo", "--agent", "-n", "0")
	want := "── iter 1 (session 1) m 10:00 · planning ──\n" +
		"iter 1 · Bash go test ./...\n" +
		"── iter 2 (session 2) m 10:05 ──\n" +
		"iter 2 · hello\n" +
		"── iter 2 (session 3) m 10:06 ──\n" +
		"iter 2 · Grep foo\n"
	if out != want {
		t.Fatalf("--agent:\n%s", out)
	}
	out, _, _ = runLogs("demo", "--agent", "-n", "2")
	if out != "── iter 2 (session 3) m 10:06 ──\niter 2 · Grep foo\n" {
		t.Fatalf("--agent -n 2:\n%s", out)
	}
	out, _, _ = runLogs("demo", "--agent", "--raw", "-n", "0")
	if out != agentFixture {
		t.Fatalf("--raw:\n%s", out)
	}
	out, _, _ = runLogs("demo", "--agent", "--iter", "2", "-n", "0")
	if strings.Contains(out, "iter 1") || !strings.Contains(out, "session 3") || !strings.Contains(out, "iter 2 · hello") {
		t.Fatalf("--iter 2:\n%s", out)
	}
	out, _, _ = runLogs("demo", "--agent", "--raw", "--iter", "1", "-n", "0")
	if strings.Count(out, "\n") != 2 || !strings.HasPrefix(out, "=== iter 1") {
		t.Fatalf("--raw --iter 1:\n%s", out)
	}
}

func TestLogsVerify(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "demo", "red", false)
	e.write(t, "demo", "attempts/iter-3.log", "FAIL something\n")

	out, _, code := runLogs("demo", "--verify", "3")
	if code != 0 || out != "FAIL something\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
	_, errs, code := runLogs("demo", "--verify", "4")
	if code != 1 || !strings.Contains(errs, "no verify log for iter 4 (only red iterations keep one)") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
	for _, args := range [][]string{
		{"demo", "--verify", "3", "--agent"},
		{"demo", "--verify", "3", "-f"},
		{"demo", "--raw"},
		{"demo", "--iter", "1"},
		{"demo", "--agent", "--iter", "0"},
		{"demo", "-n", "-1"},
		{"a", "b"},
	} {
		if _, _, code := runLogs(args...); code != 2 {
			t.Errorf("%v: code=%d, want 2", args, code)
		}
	}
}

func TestLogsNoSlugPicksLatest(t *testing.T) {
	e := newLogsEnv(t)
	if _, errs, code := runLogs(); code != 1 || !strings.Contains(errs, "no runs yet") {
		t.Fatalf("empty: code=%d err=%q", code, errs)
	}
	e.mk(t, "old", "complete", false)
	e.mk(t, "new", "complete", false)
	e.mk(t, "act", "running", true)
	e.write(t, "old", "runner.log", "OLD\n")
	e.write(t, "new", "runner.log", "NEW\n")
	e.write(t, "act", "runner.log", "ACT\n")
	now := time.Now()
	os.Chtimes(filepath.Join(e.runs, "old", "run.json"), now, now.Add(-2*time.Hour))
	os.Chtimes(filepath.Join(e.runs, "new", "run.json"), now, now.Add(-time.Hour))
	os.Chtimes(filepath.Join(e.runs, "act", "run.json"), now, now.Add(-3*time.Hour))

	if out, _, _ := runLogs(); out != "ACT\n" {
		t.Fatalf("active should win: %q", out)
	}
	e.active["act"].Store(false)
	if out, _, _ := runLogs(); out != "NEW\n" {
		t.Fatalf("latest should win: %q", out)
	}
}

func TestLogsUnknownSlug(t *testing.T) {
	newLogsEnv(t)
	_, errs, code := runLogs("nope")
	if code != 1 || !strings.Contains(errs, "no run state for nope") {
		t.Fatalf("code=%d err=%q", code, errs)
	}
}

func startLogs(args ...string) (*syncBuf, chan int) {
	out := &syncBuf{}
	done := make(chan int, 1)
	go func() {
		done <- dispatch(append([]string{"logs"}, args...), out, &bytes.Buffer{}, strings.NewReader(""))
	}()
	return out, done
}

func waitExit(t *testing.T, done chan int) int {
	t.Helper()
	select {
	case c := <-done:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("logs did not exit")
		return -1
	}
}

func (e *logsEnv) waitPolls(t *testing.T, n int64) {
	t.Helper()
	target := e.polls.Load() + n
	deadline := time.Now().Add(5 * time.Second)
	for e.polls.Load() < target {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for polls")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestLogsFollow(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "demo", "running", true)
	e.write(t, "demo", "runner.log", "first\n")
	out, done := startLogs("demo", "-f")
	waitFor(t, out, "first\n")

	e.appendTo(t, "demo", "runner.log", "second\nthird\n")
	waitFor(t, out, "third\n")
	e.appendTo(t, "demo", "runner.log", "par")
	e.waitPolls(t, 5)
	if strings.Contains(out.String(), "par") {
		t.Fatalf("partial line shown early:\n%s", out.String())
	}
	e.appendTo(t, "demo", "runner.log", "tial\n")
	waitFor(t, out, "partial\n")

	e.write(t, "demo", "run.json", `{"slug":"demo","status":"complete"}`)
	e.active["demo"].Store(false)
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got, want := out.String(), "first\nsecond\nthird\npartial\n— run ended: complete\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestLogsFollowAgent(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "demo", "running", true)
	e.write(t, "demo", "agent.log", "=== iter 1 (session 1) model=m planning=false 2026-10-25T10:00:00Z ===\n")
	out, done := startLogs("demo", "-f", "--agent")
	waitFor(t, out, "── iter 1")
	e.appendTo(t, "demo", "agent.log", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"x.go"}}]}}`+"\n")
	waitFor(t, out, "iter 1 · Edit x.go\n")
	e.active["demo"].Store(false)
	waitExit(t, done)
}

func TestLogsFollowTruncated(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "demo", "running", true)
	e.write(t, "demo", "runner.log", "aaaaaaaaaa\nbbbbbbbbbb\n")
	out, done := startLogs("demo", "-f")
	waitFor(t, out, "bbbbbbbbbb\n")

	tmp := filepath.Join(e.runs, "demo", "new.tmp")
	os.WriteFile(tmp, []byte("fresh\n"), 0o644)
	if err := os.Rename(tmp, filepath.Join(e.runs, "demo", "runner.log")); err != nil {
		t.Fatal(err)
	}
	waitFor(t, out, "fresh\n")
	e.active["demo"].Store(false)
	waitExit(t, done)
	if strings.Count(out.String(), "fresh") != 1 {
		t.Fatalf("output:\n%s", out.String())
	}
}

func TestLogsFollowNotRunning(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "demo", "complete", false)
	e.write(t, "demo", "runner.log", "one\ntwo\n")
	out, done := startLogs("demo", "-f")
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if out.String() != "one\ntwo\n(not running)\n" {
		t.Fatalf("got %q", out.String())
	}
}

func TestLogsFollowMultiple(t *testing.T) {
	e := newLogsEnv(t)
	e.mk(t, "a", "running", true)
	e.mk(t, "b", "running", true)
	e.write(t, "a", "runner.log", "a1\n")
	e.write(t, "b", "runner.log", "b1\n")
	out, done := startLogs("-f")
	waitFor(t, out, "[a] a1\n")
	waitFor(t, out, "[b] b1\n")

	e.write(t, "a", "run.json", `{"slug":"a","status":"complete"}`)
	e.active["a"].Store(false)
	waitFor(t, out, "[a] — run ended: complete\n")
	select {
	case <-done:
		t.Fatal("exited before b ended")
	case <-time.After(30 * time.Millisecond):
	}
	e.appendTo(t, "b", "runner.log", "b2\n")
	waitFor(t, out, "[b] b2\n")
	e.write(t, "b", "run.json", `{"slug":"b","status":"red"}`)
	e.active["b"].Store(false)
	if code := waitExit(t, done); code != 0 {
		t.Fatalf("exit %d", code)
	}
	waitFor(t, out, "[b] — run ended: red\n")
}
