package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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

func TestCompleteFlagEqualsValue(t *testing.T) {
	completeHome(t)
	if got := complete(t, "run", "--until=06:00", ""); got != lines("Demo", "alpha", "shared") {
		t.Errorf("run --until=06:00: %q", got)
	}
	if got := complete(t, "review", "--lens="); got != lines("--lens=general", "--lens=mine", "--lens=quality", "--lens=requirements", "--lens=security") {
		t.Errorf("review --lens=: %q", got)
	}
	if got := complete(t, "review", "--lens=general,q"); got != lines("--lens=general,quality") {
		t.Errorf("--lens=general,q: %q", got)
	}
	if got := complete(t, "run", "--bogus=1", ""); got != lines("Demo", "alpha", "shared") {
		t.Errorf("run --bogus=1: %q", got)
	}
	if got := complete(t, "run", "--until="); got != "" {
		t.Errorf("run --until=: %q", got)
	}
	if got := complete(t, "new", "--repo="); got != "__dirs__\n" {
		t.Errorf("new --repo=: %q", got)
	}
	if got := complete(t, "run", "--next=true", ""); got != "" {
		t.Errorf("run --next=true: %q", got)
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

	// Draft and ready slugs that differ only in case list once, draft spelling.
	h := t.TempDir()
	t.Setenv("NIGHTSHIFT_HOME", h)
	for _, rel := range []string{"specs/draft/Shared.md", "specs/ready/shared.md"} {
		p := filepath.Join(h, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, cur := range []string{"", "s", "S"} {
		if got := complete(t, "lint", cur); got != lines("Shared") {
			t.Errorf("lint %q: got %q", cur, got)
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

func TestCompletionBashScript(t *testing.T) {
	var out, errb bytes.Buffer
	if code := dispatch([]string{"completion", "bash"}, &out, &errb, strings.NewReader("")); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	script := out.String()
	for _, want := range []string{"complete -F", "__complete"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "__NIGHTSHIFT_BIN__") {
		t.Fatalf("placeholder left in script")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	f := filepath.Join(t.TempDir(), "c.bash")
	if err := os.WriteFile(f, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	if o, err := exec.Command(bash, "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v: %s", err, o)
	}
}

func TestCompletionBashEndToEnd(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	bin := buildNightshiftBinary(t)
	h := t.TempDir()
	for _, rel := range []string{"specs/ready/demo.md", "specs/draft/dummy.md"} {
		p := filepath.Join(h, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(), "NIGHTSHIFT_HOME="+h, "HOME="+h)
	gen := exec.Command(bin, "completion", "bash")
	gen.Env = env
	script, err := gen.Output()
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "c.bash")
	if err := os.WriteFile(f, script, 0o644); err != nil {
		t.Fatal(err)
	}
	const stubDef = `_get_comp_words_by_ref() {
  [[ "$*" == "-n =: cur words cword" ]] || return 1
  words=(nightshift run --until 06:00 d); cword=4; cur=d; STUB_USED=1
}
`
	run := func(line, wordsExpr string, cword int, stub bool) (string, string) {
		pre := ""
		if stub {
			pre = stubDef
		}
		c := exec.Command(bash, "--norc", "--noprofile", "-c",
			`source "$1"; `+pre+`STUB_USED=0; COMP_LINE=$2; COMP_POINT=${#COMP_LINE}; COMP_WORDS=(`+wordsExpr+`); COMP_CWORD=`+strconv.Itoa(cword)+`; _nightshift; printf "%s\n" "${COMPREPLY[@]}"; echo "stub=$STUB_USED"`, "_", f, line)
		c.Env = env
		o, err := c.Output()
		if err != nil {
			t.Fatalf("bash: %v", err)
		}
		i := strings.LastIndex(string(o), "stub=")
		return string(o[:i]), strings.TrimSpace(string(o[i+5:]))
	}
	for _, stub := range []bool{false, true} {
		want := "0"
		if stub {
			want = "1"
		}
		cases := []struct {
			line, words string
			cword       int
			want        string
		}{
			{"nightshift run d", "nightshift run d", 2, "demo\n"},
			{"nightshift run --until 06:00 d", "nightshift run --until 06 : 00 d", 6, "demo\n"},
			{"nightshift run --until=06:00 d", "nightshift run --until = 06 : 00 d", 7, "demo\n"},
		}
		for _, tc := range cases {
			got, used := run(tc.line, tc.words, tc.cword, stub)
			if tc.line == "nightshift run d" && stub {
				continue // the stub always supplies the --until line
			}
			if got != tc.want {
				t.Errorf("stub=%v %q = %q, want %q", stub, tc.line, got, tc.want)
			}
			if used != want {
				t.Errorf("stub=%v %q: stub used = %s", stub, tc.line, used)
			}
		}
	}
	if got, _ := run("nightshift ", `nightshift ""`, 1, false); !strings.Contains(got, "run\n") {
		t.Fatalf("subcommands = %q", got)
	}
	if got, _ := run("nightshift review --lens=", "nightshift review --lens =", 3, false); !strings.HasPrefix(got, "general\n") {
		t.Fatalf("--lens= = %q", got)
	}
}

func TestCompletionUsageError(t *testing.T) {
	for _, args := range [][]string{{"completion"}, {"completion", "zsh"}, {"completion", "bash", "extra"}} {
		var out, errb bytes.Buffer
		code := dispatch(args, &out, &errb, strings.NewReader(""))
		if code != 2 || out.Len() != 0 || errb.String() != "usage: nightshift completion bash\n" {
			t.Fatalf("%v: code %d stdout %q stderr %q", args, code, out.String(), errb.String())
		}
	}
	if !strings.Contains(usage, "nightshift completion bash") || !strings.Contains(usage, "print a bash completion script") {
		t.Fatal("usage missing completion line")
	}
	if strings.Contains(usage, "__complete") {
		t.Fatal("usage lists __complete")
	}
}

var helpFlagRE = regexp.MustCompile(`(?:^|[\s\[(|])(--?[A-Za-z][A-Za-z0-9-]*)`)

// helpFlags returns the bare flag names found in a subcommand's -h output.
func helpFlags(out string) map[string]bool {
	set := map[string]bool{}
	for _, m := range helpFlagRE.FindAllStringSubmatch(out, -1) {
		name := strings.TrimLeft(m[1], "-")
		if name == "h" || name == "help" {
			continue
		}
		set[name] = true
	}
	return set
}

func sortedKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestCompleteCoversEveryFlag(t *testing.T) {
	bin := buildNightshiftBinary(t)
	home := t.TempDir()
	for _, c := range completionCommands {
		args := []string{c.name, "-h"}
		if c.name == "done" {
			// done parses its args by hand, so "-h" would be taken as a
			// slug; no arguments prints its usage line instead.
			args = []string{"done"}
		}
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(), "NIGHTSHIFT_HOME="+home, "HOME="+home)
		out, _ := cmd.CombinedOutput() // -h exits non-zero for some commands
		got := helpFlags(string(out))
		if c.name == "run" && (!got["follow"] || !got["plan-only"]) {
			t.Fatalf("run: flag extraction looks broken: %v", sortedKeys(got))
		}
		want := map[string]bool{}
		for _, f := range c.flags {
			want[strings.TrimSuffix(f, "=")] = true
		}
		var missing, extra []string
		for f := range got {
			if !want[f] {
				missing = append(missing, f)
			}
		}
		for f := range want {
			if !got[f] {
				extra = append(extra, f)
			}
		}
		sort.Strings(missing)
		sort.Strings(extra)
		if len(missing) > 0 || len(extra) > 0 {
			t.Errorf("%s: flags in -h but not registered for completion: %v; registered but not in -h: %v",
				c.name, missing, extra)
		}
	}
}

func TestCompleteCoversEverySubcommand(t *testing.T) {
	if strings.Contains(usage, "__complete") {
		t.Error("__complete must not appear in usage")
	}
	inUsage := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+nightshift (\S+)`).FindAllStringSubmatch(usage, -1) {
		inUsage[m[1]] = true
	}
	inTable := map[string]bool{}
	for _, c := range completionCommands {
		inTable[c.name] = true
	}
	for _, n := range sortedKeys(inUsage) {
		if !inTable[n] {
			t.Errorf("subcommand %q is in usage but has no completion entry", n)
		}
	}
	for _, n := range sortedKeys(inTable) {
		if !inUsage[n] {
			t.Errorf("subcommand %q has a completion entry but is not in usage", n)
		}
	}
}
