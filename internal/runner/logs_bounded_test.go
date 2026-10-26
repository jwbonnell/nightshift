package runner

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type countingFile struct {
	*os.File
	n *atomic.Int64
}

func (c countingFile) Read(p []byte) (int, error) {
	n, err := c.File.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c countingFile) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.File.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

func countingOpen(n *atomic.Int64) func(string) (logFile, error) {
	return func(p string) (logFile, error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		return countingFile{f, n}, nil
	}
}

const (
	bigHeader1 = "=== iter 1 (session 1) model=m planning=false 2026-10-24T10:00:00Z ==="
	bigHeader3 = "=== iter 3 (session 3) model=m planning=false 2026-10-24T11:00:00Z ==="
)

func boundedBashEvent(i int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cmd%d"}}]}}`+"\n", i)
}

// writeBigAgentLog writes ~20 MiB of filler between two headers, then a
// few hundred filler lines and 8 Bash events after the last header.
func writeBigAgentLog(t *testing.T, root, slug string) {
	t.Helper()
	dir := filepath.Join(root, slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(`{"status":"complete"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	b.WriteString(bigHeader1 + "\n")
	filler := `{"type":"system"}` + "\n"
	for b.Len() < 20<<20 {
		b.WriteString(filler)
	}
	b.WriteString(bigHeader3 + "\n")
	for i := 0; i < 300; i++ {
		b.WriteString(filler)
	}
	for i := 0; i < 8; i++ {
		b.WriteString(boundedBashEvent(i))
	}
	if err := os.WriteFile(filepath.Join(dir, "agent.log"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLogsTailBoundedRead(t *testing.T) {
	root := t.TempDir()
	writeBigAgentLog(t, root, "s")
	var n atomic.Int64
	var out bytes.Buffer
	err := Logs(LogsOptions{StateRoot: root, Slug: "s", Agent: true, N: 5, Stdout: &out, open: countingOpen(&n)})
	if err != nil {
		t.Fatal(err)
	}
	if n.Load() > 256<<10 {
		t.Fatalf("read %d bytes, want <= 256KiB", n.Load())
	}
	var want []string
	for i := 3; i < 8; i++ {
		want = append(want, fmt.Sprintf("iter 3 · Bash cmd%d", i))
	}
	if got := strings.TrimSpace(out.String()); got != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

func TestFollowStartAtEndBoundedRead(t *testing.T) {
	root := t.TempDir()
	writeBigAgentLog(t, root, "s")
	var n atomic.Int64
	o := LogsOptions{StateRoot: root, Agent: true, open: countingOpen(&n)}
	s := o.newStream("s", "")
	s.startAtEnd()
	if n.Load() > 1<<20+64<<10 {
		t.Fatalf("read %d bytes, want ~<= 1MiB", n.Load())
	}
	if got := s.poll(); len(got) != 0 {
		t.Fatalf("existing content rendered: %v", got)
	}
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(boundedBashEvent(9)); err != nil {
		t.Fatal(err)
	}
	got := s.poll()
	if len(got) != 1 || got[0] != "iter 3 · Bash cmd9" {
		t.Fatalf("got %v", got)
	}
}

func TestLogsRawIterResetOnReopen(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "agent.log")
	hdr := "=== iter 2 (session 2) model=m planning=false 2026-10-24T10:00:00Z ===\n"
	if err := os.WriteFile(p, []byte(hdr+"one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := LogsOptions{StateRoot: root, Agent: true, Raw: true, Iter: 2}
	s := o.newStream("s", "")
	if got := s.poll(); len(got) != 2 {
		t.Fatalf("first poll = %v", got)
	}
	// Replace the file (new inode, so it is reopened) with header-less lines.
	tmp := filepath.Join(dir, "new")
	if err := os.WriteFile(tmp, []byte("a\nb\nc\nd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	if got := s.poll(); len(got) != 0 {
		t.Fatalf("stale rawIter leaked lines: %v", got)
	}
}

func TestRenderResultErrorNoMessage(t *testing.T) {
	for _, raw := range []string{
		`{"type":"result","is_error":true,"result":""}`,
		`{"type":"result","is_error":true}`,
	} {
		got := (&Renderer{}).Line(raw)
		if len(got) != 1 || got[0] != "result: error" {
			t.Errorf("%s -> %q", raw, got)
		}
	}
}

// The backwards read must agree with rendering the whole file.
func TestLogsTailMatchesFullRender(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.json"), []byte(`{"status":"complete"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	for it := 1; it <= 6; it++ {
		fmt.Fprintf(&b, "=== iter %d (session %d) model=m planning=false 2026-10-24T10:00:00Z ===\n", it, it)
		for i := 0; i < it*700; i++ {
			if i%50 == 0 {
				b.WriteString(boundedBashEvent(i))
			} else {
				b.WriteString(`{"type":"system","pad":"xxxxxxxxxxxxxxxxxxxxxxxx"}` + "\n")
			}
		}
	}
	b.WriteString("partial tail")
	if err := os.WriteFile(filepath.Join(dir, "agent.log"), b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(n int, raw bool) string {
		var out bytes.Buffer
		if err := Logs(LogsOptions{StateRoot: root, Slug: "s", Agent: true, Raw: raw, N: n, Stdout: &out}); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	for _, raw := range []bool{false, true} {
		full := strings.Split(strings.TrimSuffix(run(0, raw), "\n"), "\n")
		for _, n := range []int{1, 5, 30, 200, 2000} {
			if n > len(full) {
				continue
			}
			want := strings.Join(full[len(full)-n:], "\n") + "\n"
			if got := run(n, raw); got != want {
				t.Fatalf("raw=%v n=%d mismatch:\n%s\nwant:\n%s", raw, n, got, want)
			}
		}
	}
}

func TestFollowStartAtEndLongLine(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "runner.log")
	if err := os.WriteFile(p, []byte("a\n"+strings.Repeat("x", 2<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	o := LogsOptions{StateRoot: root}
	s := o.newStream("s", "")
	s.startAtEnd()
	if got := s.poll(); len(got) != 0 {
		t.Fatalf("existing content printed: %d lines", len(got))
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("zzz"); err != nil {
		t.Fatal(err)
	}
	if got := s.poll(); len(got) != 0 {
		t.Fatalf("fragment printed: %v", got)
	}
	if _, err := f.WriteString("yyy\nnext\n"); err != nil {
		t.Fatal(err)
	}
	got := s.poll()
	if len(got) != 1 || got[0] != "next" {
		t.Fatalf("got %v, want [next]", got)
	}
}

func TestFollowStartAtEndRunnerLogSmallRead(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "s")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("y", 99) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "runner.log"), []byte(strings.Repeat(line, 20000)), 0o644); err != nil {
		t.Fatal(err)
	}
	var n atomic.Int64
	o := LogsOptions{StateRoot: root, open: countingOpen(&n)}
	s := o.newStream("s", "")
	s.startAtEnd()
	if n.Load() > tailChunk {
		t.Fatalf("read %d bytes, want <= %d", n.Load(), tailChunk)
	}
}
