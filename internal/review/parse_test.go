package review

import "testing"

func TestStripCheckboxes(t *testing.T) {
	cases := []struct{ in, want string }{
		{"- [ ] foo", "- foo"},
		{"* [x] bar", "* bar"},
		{"* [X] baz", "* baz"},
		{"  - [ ] indented", "  - indented"},
		{"no checkbox here", "no checkbox here"},
		{"- [ ]no space after", "- no space after"},
		{"- [ ] one\n* [x] two\nplain", "- one\n* two\nplain"},
	}
	for _, c := range cases {
		if got := stripCheckboxes(c.in); got != c.want {
			t.Errorf("stripCheckboxes(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseVerdict(t *testing.T) {
	cases := []struct{ in, want string }{
		{"VERDICT: LGTM\nbody", "LGTM"},
		{"VERDICT:CONCERNS", "CONCERNS"},
		{"intro text\nVERDICT: BLOCKING\nmore", "BLOCKING"},
		{"no verdict line here", "UNKNOWN"},
		{"VERDICT: approve", "UNKNOWN"}, // lowercase doesn't match
	}
	for _, c := range cases {
		if got := parseVerdict(c.in); got != c.want {
			t.Errorf("parseVerdict(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
