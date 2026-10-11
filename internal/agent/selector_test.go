package agent

import "testing"

func TestSelectorParse(t *testing.T) {
	cases := []struct {
		in          string
		wantAgent   string
		wantModel   string
		wantString  string
		wantDisplay string
	}{
		{"opus", "claude", "opus", "claude/opus", "opus"},
		{"claude/opus", "claude", "opus", "claude/opus", "opus"},
		{"opencode/openai/gpt-5", "opencode", "openai/gpt-5", "opencode/openai/gpt-5", "opencode/openai/gpt-5"},
		{"opencode/homelab/qwen3-coder:30b", "opencode", "homelab/qwen3-coder:30b", "opencode/homelab/qwen3-coder:30b", "opencode/homelab/qwen3-coder:30b"},
		{"opencode/a/b/c", "opencode", "a/b/c", "opencode/a/b/c", "opencode/a/b/c"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			sel, err := ParseSelector(c.in)
			if err != nil {
				t.Fatalf("ParseSelector(%q): %v", c.in, err)
			}
			if sel.Agent != c.wantAgent || sel.Model != c.wantModel {
				t.Fatalf("ParseSelector(%q) = %+v, want agent=%q model=%q", c.in, sel, c.wantAgent, c.wantModel)
			}
			if got := sel.String(); got != c.wantString {
				t.Errorf("String() = %q, want %q", got, c.wantString)
			}
			if got := sel.Display(); got != c.wantDisplay {
				t.Errorf("Display() = %q, want %q", got, c.wantDisplay)
			}
		})
	}
}

func TestSelectorParseErrors(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "empty agent selector"},
		{"foo/bar", `unknown agent "foo" in "foo/bar" (want claude/<model> or opencode/<provider>/<model>)`},
		{"opencode/openai", `"opencode/openai": opencode needs <provider>/<model>`},
		{"opencode//x", `"opencode//x": opencode needs <provider>/<model>`},
		{"opencode/x/", `"opencode/x/": opencode needs <provider>/<model>`},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, err := ParseSelector(c.in)
			if err == nil {
				t.Fatalf("ParseSelector(%q): want error", c.in)
			}
			if err.Error() != c.want {
				t.Fatalf("ParseSelector(%q) error = %q, want %q", c.in, err.Error(), c.want)
			}
		})
	}
}
