package home

import "strings"

// ParseFrontmatter extracts the YAML-ish frontmatter block (between a
// leading "---\n" and the next "\n---") into a map, splitting each line at
// its first colon. A file with no recognizable frontmatter block yields an
// empty map. Values are returned exactly as written, including any
// surrounding quotes; callers that need the D11.3 quote-stripped value
// should run it through unquote themselves.
func ParseFrontmatter(data []byte) map[string]string {
	md := string(data)
	fm := map[string]string{}
	if !strings.HasPrefix(md, "---\n") {
		return fm
	}
	rest := md[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return fm
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		k, v, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		fm[key] = strings.TrimSpace(v)
	}
	return fm
}
