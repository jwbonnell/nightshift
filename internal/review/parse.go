package review

import "regexp"

// checkboxRe matches a leading markdown checkbox on a list item, e.g.
// "- [ ] foo" or "* [x] bar" — the prompt forbids checkboxes, but a
// reviewer might still emit one.
var checkboxRe = regexp.MustCompile(`(?m)^(\s*[-*]) \[[ xX]\]\s?`)

// stripCheckboxes rewrites "- [ ] foo" into "- foo" (and "* [x] bar" into
// "* bar"), leaving everything else untouched.
func stripCheckboxes(s string) string {
	return checkboxRe.ReplaceAllString(s, "$1 ")
}

// verdictRe matches the reviewer's mandated VERDICT line, unanchored, so
// it matches wherever in the body it appears.
var verdictRe = regexp.MustCompile(`VERDICT:\s*(LGTM|CONCERNS|BLOCKING)`)

// parseVerdict extracts the first VERDICT: line's value from body, or
// "UNKNOWN" when none matches. Callers that care about checkboxes
// confusing the match should pass stripCheckboxes(body).
func parseVerdict(body string) string {
	m := verdictRe.FindStringSubmatch(body)
	if m == nil {
		return "UNKNOWN"
	}
	return m[1]
}
