package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGateVerifier scripts the gate verifier: fn gets the call number
// (1-based), the dir and the command.
type fakeGateVerifier struct {
	calls []string
	fn    func(n int, dir, cmd string) (string, error)
}

func (f *fakeGateVerifier) Verify(ctx context.Context, dir string, egress []string, image, cmd string) (string, error) {
	f.calls = append(f.calls, cmd)
	return f.fn(len(f.calls), dir, cmd)
}

func exit3() error { return exec.Command("sh", "-c", "exit 3").Run() }

func gateAgent(t *testing.T) func(ctx context.Context, iter int, dir string) error {
	return func(ctx context.Context, iter int, dir string) error {
		switch iter {
		case 2:
			commitFile(t, dir, "work.go", "package main\n", "work")
			touchComplete(t, dir)
		case 3:
			touchComplete(t, dir)
		}
		return nil
	}
}

func TestGatesPass(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	var holdSeen []bool
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) {
		_, err := os.Stat(filepath.Join(dir, "zz_hold_test.go"))
		holdSeen = append(holdSeen, err == nil)
		return "ok", nil
	}}
	r := newTestRun(t, repo, "accept: ./accept.sh\n", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv}, sb)
	writeHoldout(t, holdoutSlugDir(r.opts.Home, "demo"), "zz_hold_test.go", "package main\n")

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v", status)
	}
	if len(fv.calls) != 2 || fv.calls[0] != r.spec.Verify || fv.calls[1] != "./accept.sh" {
		t.Fatalf("calls = %q", fv.calls)
	}
	if len(holdSeen) != 2 || !holdSeen[0] || holdSeen[1] {
		t.Fatalf("holdout visibility = %v, want [true false]", holdSeen)
	}
	if _, err := os.Stat(r.gateWSDir()); !os.IsNotExist(err) {
		t.Fatalf("gate-ws still exists: %v", err)
	}
	if refExists(r.spec.Repo, reviewRoundRef("demo")) {
		t.Fatal("review ref left behind")
	}
	if _, err := r.finish(status); err != nil {
		t.Fatal(err)
	}
	if body := r.resultBody(StatusComplete); !strings.Contains(body, "gate: pass\n") || !strings.Contains(body, "## Acceptance gate") {
		t.Fatalf("note:\n%s", body)
	}
}

func TestGatesBothRunAfterFailure(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) {
		if n == 1 {
			return "boom", exit3()
		}
		return "fine", nil
	}}
	r := newTestRun(t, repo, "accept: ./accept.sh\ngate_rounds: 0\n", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv}, sb)
	writeHoldout(t, holdoutSlugDir(r.opts.Home, "demo"), "zz_hold_test.go", "package main\n")
	if status := r.loop(); status != StatusComplete {
		t.Fatalf("status = %v", status)
	}
	if len(fv.calls) != 2 {
		t.Fatalf("calls = %q, want both gates", fv.calls)
	}
	if r.gate.result != "fail" {
		t.Fatalf("gate = %+v", r.gate)
	}
}

func TestGateInterrupted(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) {
		cancel()
		return "", ctx.Err()
	}}
	r := newTestRun(t, repo, "accept: ./accept.sh\n", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv, Ctx: ctx}, sb)
	if status := r.loop(); status != StatusInterrupted {
		t.Fatalf("status = %v, want interrupted", status)
	}
	if _, err := os.Stat(r.gateWSDir()); !os.IsNotExist(err) {
		t.Fatal("gate-ws left behind")
	}
	if refExists(r.spec.Repo, reviewRoundRef("demo")) {
		t.Fatal("review ref left behind")
	}
}

func TestGateHandBackAccept(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = gateAgent(t)
	sb.verify = alwaysGreenVerify
	var reviewMD string
	var r *run
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) {
		if n == 1 {
			return "line1\nbad\x1b[31m output\n", exit3()
		}
		return "ok", nil
	}}
	sbAgent := sb.agent
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 3 {
			reviewMD = r.scaffoldRead(r.reviewPath())
		}
		return sbAgent(ctx, iter, dir)
	}
	r = newTestRun(t, repo, "accept: ./accept.sh\n", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v", status)
	}
	want := "# Acceptance gate — failed\n\nFix each failure below, note what you changed in PROGRESS.md, then touch .spec-complete again.\n\n" +
		"- **accept** failed (exit 3): ./accept.sh\n\n```\nline1\nbad?[31m output\n```\n\n"
	if reviewMD != want {
		t.Fatalf("REVIEW.md = %q, want %q", reviewMD, want)
	}
	if r.st.GateHandbacks != 1 || r.gate.result != "pass" {
		t.Fatalf("handbacks=%d gate=%+v", r.st.GateHandbacks, r.gate)
	}
	data, _ := os.ReadFile(r.statePath())
	if !strings.Contains(string(data), `"gate_handbacks": 1`) && !strings.Contains(string(data), `"gate_handbacks":1`) {
		t.Fatalf("run.json lacks gate_handbacks: %s", data)
	}
}

func TestGateRoundsExhausted(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = gateAgent(t)
	sb.verify = alwaysGreenVerify
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) { return "still bad\n", exit3() }}
	nr := &recordingNotifierForTest{}
	r := newTestRun(t, repo, "accept: ./accept.sh\ngate_rounds: 1\n", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv, Notifier: nr}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v", status)
	}
	if _, err := r.finish(status); err != nil {
		t.Fatal(err)
	}
	if r.gate.result != "fail" || len(fv.calls) != 2 {
		t.Fatalf("gate=%+v calls=%d", r.gate, len(fv.calls))
	}
	if !nr.urgent || !strings.HasSuffix(nr.title, " (gate failed)") {
		t.Fatalf("notify = %q urgent=%v", nr.title, nr.urgent)
	}
	body := r.resultBody(StatusComplete)
	if !strings.Contains(body, "gate: fail\n") || !strings.Contains(body, "## Acceptance gate") || !strings.Contains(body, "still bad") {
		t.Fatalf("note:\n%s", body)
	}
}

func TestHoldoutNeverLeaks(t *testing.T) {
	const name, content, outTok = "zz_canary_holdout_test.go", "CANARY_CONTENT_TOKEN", "CANARY_OUTPUT_TOKEN"
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = gateAgent(t)
	sb.verify = alwaysGreenVerify
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return outTok + " " + name, exit3()
		}
		return "ok", nil
	}}
	var r *run
	var reviewMD string
	inner := sb.agent
	sb.agent = func(ctx context.Context, iter int, dir string) error {
		if iter == 3 {
			reviewMD = r.scaffoldRead(r.reviewPath())
		}
		if iter == 4 {
			touchComplete(t, dir)
		}
		return inner(ctx, iter, dir)
	}
	r = newTestRun(t, repo, "", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv}, sb)
	writeHoldout(t, holdoutSlugDir(r.opts.Home, "demo"), name, content)

	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v", status)
	}
	if r.st.GateHandbacks != 2 {
		t.Fatalf("handbacks = %d, want 2", r.st.GateHandbacks)
	}
	if _, err := r.finish(status); err != nil {
		t.Fatal(err)
	}
	tokens := []string{name, content, outTok}
	check := func(what, text string) {
		for _, tok := range tokens {
			if strings.Contains(text, tok) {
				t.Errorf("%s contains %q", what, tok)
			}
		}
	}
	check("REVIEW.md", reviewMD)
	check("PROGRESS.md", r.scaffoldRead(r.progressPath()))
	for i, p := range sb.prompts {
		check("prompt "+string(rune('0'+i)), p)
	}
	for _, f := range []string{"agent.log", "runner.log"} {
		data, _ := os.ReadFile(filepath.Join(r.dir, f))
		check(f, string(data))
	}
	filepath.WalkDir(r.clone, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, _ := os.ReadFile(p)
		check(p, p+string(data))
		return nil
	})
	out, _ := exec.Command("git", "-C", r.clone, "log", "-p", "--all").CombinedOutput()
	check("git log", string(out))
	body := r.resultBody(StatusComplete)
	for _, tok := range []string{outTok} {
		if !strings.Contains(body, tok) {
			t.Errorf("result note lacks %q", tok)
		}
	}
	if !strings.Contains(body, "holdout") {
		t.Errorf("result note lacks holdout section")
	}
}

func TestGateOutputSanitized(t *testing.T) {
	in := "a\tb\nc\x1b[0m\rd\x00e\xffX\x7f\n"
	got := sanitizeGateOutput(in)
	if got != "a\tb\nc?[0m?d?e?X?\n" {
		t.Fatalf("got %q", got)
	}
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "l")
	}
	if n := strings.Count(tailLines(strings.Join(lines, "\n"), 40), "\n"); n != 40 {
		t.Fatalf("tail lines = %d", n)
	}
	if f := fenced("a ```` b\n"); !strings.HasPrefix(f, "`````\n") {
		t.Fatalf("fence = %q", f)
	}
}

func TestNoGateUnchanged(t *testing.T) {
	repo := newSourceRepo(t)
	sb := newStubSandbox()
	sb.agent = completingAgent(t)
	sb.verify = alwaysGreenVerify
	fv := &fakeGateVerifier{fn: func(n int, dir, cmd string) (string, error) {
		t.Fatal("gate verifier called")
		return "", errors.New("unreachable")
	}}
	nr := &recordingNotifierForTest{}
	r := newTestRun(t, repo, "", Options{Stdout: &bytes.Buffer{}, GateVerifier: fv, Notifier: nr}, sb)
	status := r.loop()
	if status != StatusComplete {
		t.Fatalf("status = %v", status)
	}
	if _, err := r.finish(status); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{r.gateWSDir(), r.gateLogDir()} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Fatalf("%s exists", d)
		}
	}
	if strings.Contains(nr.title, "gate") || strings.Contains(r.resultBody(StatusComplete), "gate") {
		t.Fatalf("gate leaked into output: %q", nr.title)
	}
	data, _ := os.ReadFile(filepath.Join(r.dir, "runner.log"))
	rj, _ := os.ReadFile(r.statePath())
	if strings.Contains(string(data), "gate") || strings.Contains(string(rj), "gate") {
		t.Fatal("gate in runner.log/run.json")
	}
}
