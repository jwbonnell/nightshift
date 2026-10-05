package main

import (
	"bytes"
	"testing"
)

func TestDispatchUsageAndHelp(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantExit   int
		wantStdout bool
		wantStderr bool
	}{
		{"no args", nil, 2, false, true},
		{"help flag", []string{"-h"}, 0, true, false},
		{"help word", []string{"help"}, 0, true, false},
		{"long help flag", []string{"--help"}, 0, true, false},
		{"unknown command", []string{"bogus"}, 2, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := dispatch(c.args, &stdout, &stderr)
			if got != c.wantExit {
				t.Fatalf("exit = %d, want %d", got, c.wantExit)
			}
			if c.wantStdout && stdout.String() != usage {
				t.Fatalf("stdout = %q, want usage text", stdout.String())
			}
			if c.wantStderr && stderr.String() != usage {
				t.Fatalf("stderr = %q, want usage text", stderr.String())
			}
		})
	}
}

func TestDispatchVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	got := dispatch([]string{"version"}, &stdout, &stderr)
	if got != 0 {
		t.Fatalf("exit = %d, want 0", got)
	}
	want := "nightshift dev\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDispatchStubsNotImplemented(t *testing.T) {
	for _, cmd := range []string{"init", "new", "ready", "done", "run", "status"} {
		var stdout, stderr bytes.Buffer
		got := dispatch([]string{cmd}, &stdout, &stderr)
		if got != 1 {
			t.Fatalf("%s: exit = %d, want 1", cmd, got)
		}
		wantErr := "nightshift: not implemented\n"
		if stderr.String() != wantErr {
			t.Fatalf("%s: stderr = %q, want %q", cmd, stderr.String(), wantErr)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%s: stdout = %q, want empty", cmd, stdout.String())
		}
	}
}
