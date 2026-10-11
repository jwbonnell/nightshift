package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClaudeArgvGolden pins today's exact sbx argvs for the planner, the
// coding agent and sbxReviewer, before nightshift-agents moves any of
// this code behind internal/agent. It must stay green, unchanged, once
// that migration lands (see golden_helpers_test.go).
func TestClaudeArgvGolden(t *testing.T) {
	bin, logPath := newFakeSbx(t)
	mountDir := "/mnt/demo/repo"
	ws := "/mnt/demo/review-ws"
	egress := []string{"api.example.com"}

	stdinFile := filepath.Join(t.TempDir(), "stdin.txt")
	t.Setenv("FAKE_SBX_STDIN_FILE", stdinFile)

	sb := newSbxSandbox(bin, "claude", "demo")
	if err := sb.Create(mountDir, egress); err != nil {
		t.Fatalf("Create: %v", err)
	}

	planPrompt := "plan the thing"
	codePrompt := "do the thing"
	if err := goldenRunAgent(sb, true, "opus", planPrompt); err != nil {
		t.Fatalf("planner RunAgent: %v", err)
	}
	if err := goldenRunAgent(sb, false, "sonnet", codePrompt); err != nil {
		t.Fatalf("coder RunAgent: %v", err)
	}

	if err := sb.StopAgent(); err != nil {
		t.Fatalf("StopAgent: %v", err)
	}
	if err := sb.ResetVerify(); err != nil {
		t.Fatalf("ResetVerify: %v", err)
	}
	if _, err := sb.RunVerify(context.Background(), "echo hi"); err != nil {
		t.Fatalf("RunVerify: %v", err)
	}
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	t.Setenv("FAKE_SBX_STDOUT", `{"type":"result","result":"VERDICT: approve"}`)
	rv := sbxReviewer{bin: bin, claudeBin: "claude", slug: "demo"}
	reviewPrompt := "review this"
	text, err := goldenReview(rv, ws, "opus", reviewPrompt)
	if err != nil {
		t.Fatalf("Review: %v", err)
	}
	if text != "VERDICT: approve" {
		t.Fatalf("review text = %q", text)
	}

	got := readLog(t, logPath)
	wantData, err := os.ReadFile(filepath.Join("testdata", "golden", "claude-argv.golden"))
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	want := strings.Split(strings.TrimRight(string(wantData), "\n"), "\n")
	if !equalLines(got, want) {
		t.Fatalf("argv log mismatch:\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	gotStdin, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin capture: %v", err)
	}
	wantStdin := planPrompt + codePrompt + reviewPrompt
	if string(gotStdin) != wantStdin {
		t.Fatalf("stdin capture = %q, want %q", gotStdin, wantStdin)
	}
}
