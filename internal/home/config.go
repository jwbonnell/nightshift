package home

import (
	"fmt"
	"os"
	"strings"
)

// configKeys are the env var names from D1 (minus NIGHTSHIFT_HOME, which
// locates the config file instead of appearing inside it), plus
// NIGHTSHIFT_NOTIFY (D6).
var configKeys = map[string]bool{
	"NIGHTSHIFT_BASE":            true,
	"NIGHTSHIFT_MODEL":           true,
	"NIGHTSHIFT_PLANNER_MODEL":   true,
	"NIGHTSHIFT_MAX_ITER":        true,
	"NIGHTSHIFT_ITER_TIMEOUT":    true,
	"NIGHTSHIFT_MAX_CONCURRENT":  true,
	"NIGHTSHIFT_SBX_BIN":         true,
	"NIGHTSHIFT_CLAUDE_BIN":      true,
	"NIGHTSHIFT_NOTIFY":          true,
	"NIGHTSHIFT_HOST_CLAUDE_BIN": true,
	"NIGHTSHIFT_INTERVIEW_MODEL": true,
	"NIGHTSHIFT_REVIEW":          true,
	"NIGHTSHIFT_REVIEW_MODEL":    true,
}

// ParseConfig reads KEY=value pairs from path. A missing file is not an
// error and returns a nil map. Blank lines and lines whose first
// non-space character is a hash are skipped. Each remaining line is split
// at its first equals sign, both sides TrimSpace'd; a later duplicate key
// overwrites an earlier one. One pair of matching surrounding double or
// single quotes is stripped from the value. An unknown key, or a line with
// no equals sign, is an error naming the file and the 1-based line number.
func ParseConfig(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	cfg := make(map[string]string)
	for i, line := range strings.Split(string(data), "\n") {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("config %s line %d: %s", path, lineNo, "expected KEY=value")
		}
		key = strings.TrimSpace(key)
		if !configKeys[key] {
			return nil, fmt.Errorf("config %s line %d: %s", path, lineNo, fmt.Sprintf("unknown key %q", key))
		}
		cfg[key] = Unquote(strings.TrimSpace(value))
	}
	return cfg, nil
}

// Unquote strips one pair of matching surrounding double or single quotes
// from s. Mismatched quotes are left exactly as written.
func Unquote(s string) string {
	if len(s) >= 2 {
		first, last := s[0], s[len(s)-1]
		if (first == '"' || first == '\'') && first == last {
			return s[1 : len(s)-1]
		}
	}
	return s
}
