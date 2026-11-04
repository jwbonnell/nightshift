package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

// stubWatch stubs the interrupt context, clock, terminal check and sleeper.
// The sleeper records each interval and allows frames-1 waits before cancelling.
func stubWatch(t *testing.T, tty bool, frames int) *[]time.Duration {
	t.Helper()
	oldCtx, oldSleep, oldNow, oldTTY := newInterruptContext, watchSleep, watchNow, stdoutIsTerminal
	t.Cleanup(func() {
		newInterruptContext, watchSleep, watchNow, stdoutIsTerminal = oldCtx, oldSleep, oldNow, oldTTY
	})
	newInterruptContext = func() (context.Context, func()) {
		return context.WithCancel(context.Background())
	}
	stdoutIsTerminal = func() bool { return tty }
	watchNow = func() time.Time { return time.Date(2026, 11, 4, 5, 30, 0, 0, time.Local) }
	var got []time.Duration
	watchSleep = func(ctx context.Context, d time.Duration) error {
		got = append(got, d)
		if len(got) >= frames {
			return context.Canceled
		}
		return nil
	}
	return &got
}

func TestStatusWatchInterval(t *testing.T) {
	doneFixture(t)
	for _, tc := range []struct {
		args []string
		want time.Duration
	}{
		{[]string{"-w"}, 2 * time.Second},
		{[]string{"--watch"}, 2 * time.Second},
		{[]string{"-w", "5"}, 5 * time.Second},
		{[]string{"-w=5"}, 5 * time.Second},
		{[]string{"--watch", "5"}, 5 * time.Second},
		{[]string{"--watch=5"}, 5 * time.Second},
	} {
		got := stubWatch(t, false, 1)
		code, _, errOut := runStatus(tc.args...)
		if code != 0 || len(*got) != 1 || (*got)[0] != tc.want {
			t.Errorf("status %v: code=%d sleeps=%v stderr=%q, want %v", tc.args, code, *got, errOut, tc.want)
		}
	}
}

func TestStatusWatchBadInterval(t *testing.T) {
	doneFixture(t)
	for _, tc := range []struct {
		args []string
		raw  string
	}{
		{[]string{"-w=0"}, "0"}, {[]string{"-w=-1"}, "-1"}, {[]string{"-w=x"}, "x"},
		{[]string{"-w", "9999"}, "9999"}, {[]string{"-w=1.5"}, "1.5"},
		{[]string{"-w=true"}, "true"}, {[]string{"--watch="}, ""},
	} {
		stubWatch(t, false, 1)
		code, _, errOut := runStatus(tc.args...)
		want := `nightshift status: --watch "` + tc.raw + `": want whole seconds from 1 to 3600`
		if code != 2 || !strings.Contains(errOut, want) {
			t.Errorf("status %v: code=%d stderr=%q, want %q", tc.args, code, errOut, want)
		}
	}
	code, _, errOut := runStatus("-w", "demo")
	if code != 2 || !strings.Contains(errOut, "usage: nightshift status [--all") {
		t.Errorf("status -w demo: code=%d stderr=%q", code, errOut)
	}
}

func TestStatusWatchRedraws(t *testing.T) {
	h := doneFixture(t)
	doneSpec(t, h, "r1", "")
	for _, tty := range []bool{true, false} {
		stubWatch(t, tty, 2)
		code, out, _ := runStatus("-w")
		if code != 0 {
			t.Fatalf("tty=%v code=%d", tty, code)
		}
		if n := strings.Count(out, "Every 2s: nightshift status"); n != 2 {
			t.Errorf("tty=%v headers = %d, want 2\n%s", tty, n, out)
		}
		if n := strings.Count(out, "SLUG"); n != 2 {
			t.Errorf("tty=%v tables = %d, want 2", tty, n)
		}
		line := strings.SplitN(strings.TrimPrefix(out, "\x1b[H\x1b[2J"), "\n", 2)[0]
		if len(line) != 80 || !strings.HasSuffix(line, "Wed Nov 4 05:30:00") {
			t.Errorf("tty=%v header %q not 80 cols with time at right", tty, line)
		}
		if tty {
			parts := strings.Split(out, "\x1b[H\x1b[2J")
			if len(parts) != 3 || parts[0] != "" || !strings.HasSuffix(out, "\n") {
				t.Errorf("tty frames malformed: %q", out)
			}
		} else if strings.Contains(out, "\x1b") {
			t.Errorf("escape codes in piped output: %q", out)
		}
	}
}

func TestStatusWatchPassesFlags(t *testing.T) {
	h := doneFixture(t)
	body := "---\ntype: spec\ncreated: 2026-01-01T00:00:00Z\n---\n# x\n"
	if err := os.WriteFile(filepath.Join(home.DoneDir(h), "d1.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stubWatch(t, false, 2)
	code, out, _ := runStatus("--all", "-w", "3")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if n := strings.Count(out, "Every 3s: nightshift status --all "); n != 2 {
		t.Errorf("headers = %d, want 2\n%s", n, out)
	}
	if n := strings.Count(out, "d1"); n != 2 {
		t.Errorf("done rows = %d, want 2\n%s", n, out)
	}
}

func TestStatusWatchKeepsGoingOnError(t *testing.T) {
	h := filepath.Join(t.TempDir(), "h[")
	if err := os.MkdirAll(home.RunsDir(h), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTSHIFT_HOME", h)
	stubWatch(t, false, 2)
	code, out, _ := runStatus("-w")
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	if n := strings.Count(out, "nightshift: "); n != 2 {
		t.Errorf("error lines = %d, want 2\n%s", n, out)
	}
	if n := strings.Count(out, "Every 2s"); n != 2 {
		t.Errorf("headers = %d, want 2", n)
	}
}
