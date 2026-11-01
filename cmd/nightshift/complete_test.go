package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func completeHome(t *testing.T) string {
	t.Helper()
	h := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", h)
	write := func(rel string) {
		p := filepath.Join(h, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("specs/ready/alpha.md")
	write("specs/ready/Demo.MD")
	write("specs/ready/shared.md")
	write("specs/ready/notes.txt")
	write("specs/draft/beta.md")
	write("specs/draft/shared.md")
	write("runs/alpha/run.json")
	write("runs/gamma/run.json")
	write("runs/empty/other.txt")
	write("lenses/mine.md")
	return h
}

func complete(t *testing.T, words ...string) string {
	t.Helper()
	var out, errb bytes.Buffer
	if code := dispatch(append([]string{"__complete"}, words...), &out, &errb, strings.NewReader("")); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if errb.Len() != 0 {
		t.Fatalf("stderr: %q", errb.String())
	}
	return out.String()
}

func lines(s ...string) string {
	if len(s) == 0 {
		return ""
	}
	return strings.Join(s, "\n") + "\n"
}

func TestCompleteSubcommands(t *testing.T) {
	completeHome(t)
	got := complete(t, "")
	want := lines("init", "new", "refine", "ready", "lint", "done", "run", "status", "logs", "pr", "review", "version", "completion")
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
	if strings.Contains(got, "__complete") {
		t.Error("lists __complete")
	}
	if got := complete(t, "re"); got != lines("refine", "ready", "review") {
		t.Errorf("re: %q", got)
	}
	if got := complete(t); got != want {
		t.Errorf("no words: %q", got)
	}
}

func TestCompleteSlugsPerCommand(t *testing.T) {
	completeHome(t)
	cases := []struct {
		words []string
		want  string
	}{
		{[]string{"run", ""}, lines("Demo", "alpha", "shared")},
		{[]string{"run", "d"}, lines("Demo")},
		{[]string{"run", "DE"}, lines("Demo")},
		{[]string{"run", "al"}, lines("alpha")},
		{[]string{"run", "alpha", ""}, ""},
		{[]string{"run", "--next", ""}, ""},
		{[]string{"run", "--until", "05:00", ""}, lines("Demo", "alpha", "shared")},
		{[]string{"ready", ""}, lines("beta", "shared")},
		{[]string{"refine", "b"}, lines("beta")},
		{[]string{"refine", "beta", ""}, ""},
		{[]string{"lint", ""}, lines("beta", "shared", "Demo", "alpha")},
		{[]string{"done", ""}, lines("Demo", "alpha", "shared")},
		{[]string{"status", ""}, lines("alpha", "gamma")},
		{[]string{"logs", "g"}, lines("gamma")},
		{[]string{"review", ""}, lines("alpha", "gamma")},
		{[]string{"review", "alpha", ""}, ""},
		{[]string{"init", ""}, ""},
		{[]string{"new", ""}, ""},
		{[]string{"pr", ""}, ""},
		{[]string{"version", ""}, ""},
		{[]string{"completion", ""}, lines("bash")},
		{[]string{"completion", "b"}, lines("bash")},
		{[]string{"completion", "z"}, ""},
		{[]string{"completion", "bash", ""}, ""},
		{[]string{"bogus", ""}, ""},
	}
	for _, tc := range cases {
		if got := complete(t, tc.words...); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.words, got, tc.want)
		}
	}
}

func TestCompleteDoneSkipsTyped(t *testing.T) {
	completeHome(t)
	if got := complete(t, "done", "alpha", "demo", ""); got != lines("shared") {
		t.Errorf("got %q", got)
	}
	if got := complete(t, "done", "--complete", ""); got != "" {
		t.Errorf("--complete: %q", got)
	}
}

func TestCompleteFlags(t *testing.T) {
	completeHome(t)
	cases := []struct {
		words []string
		want  string
	}{
		{[]string{"run", "--"}, lines("--next", "--fresh", "--yes", "--max-iter", "--extend-iter", "--until",
			"--dry-run", "--base", "--max-concurrent", "--ignore-deps", "--follow", "--plan-only", "--jobs")},
		{[]string{"run", "-"}, lines("--next", "--fresh", "--yes", "--max-iter", "--extend-iter", "--until",
			"--dry-run", "--base", "--max-concurrent", "--ignore-deps", "-f", "--follow", "--plan-only", "--jobs")},
		{[]string{"ready", "--p"}, lines("--plan")},
		{[]string{"status", "--"}, lines("--all", "--since", "--done", "--repo")},
		{[]string{"logs", "-"}, lines("-f", "--agent", "--raw", "--iter", "--verify", "-n")},
		{[]string{"version", "-"}, ""},
	}
	for _, tc := range cases {
		if got := complete(t, tc.words...); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.words, got, tc.want)
		}
	}
}

func TestCompleteValueFlags(t *testing.T) {
	completeHome(t)
	cases := []struct {
		words []string
		want  string
	}{
		{[]string{"new", "--repo", ""}, "__dirs__\n"},
		{[]string{"review", "--lens", ""}, lines("general", "mine", "quality", "requirements", "security")},
		{[]string{"review", "--lens", "general,q"}, lines("general,quality")},
		{[]string{"run", "--until", ""}, ""},
		{[]string{"status", "--repo", ""}, ""},
	}
	for _, tc := range cases {
		if got := complete(t, tc.words...); got != tc.want {
			t.Errorf("%q: got %q want %q", tc.words, got, tc.want)
		}
	}
}

func TestCompleteNoHome(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIGHTSHIFT_HOME", f)
	if got := complete(t, "run", ""); got != "" {
		t.Errorf("file home: %q", got)
	}
	t.Setenv("NIGHTSHIFT_HOME", "")
	t.Setenv("HOME", "")
	if got := complete(t, ""); got != "" {
		t.Errorf("no home: %q", got)
	}
}
