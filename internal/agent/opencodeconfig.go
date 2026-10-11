package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// secretKeys are the JSON object keys LoadOpencodeConfig refuses to see a
// literal value under, compared case-insensitively.
var secretKeys = map[string]bool{
	"apikey":   true,
	"api_key":  true,
	"token":    true,
	"password": true,
}

// LoadOpencodeConfig reads path, an opencode JSON config file, validates
// that it holds no inline secret, and returns its content compacted (so
// the file's own key order survives) for OPENCODE_CONFIG_CONTENT.
//
// Any key that case-insensitively matches "apiKey", "api_key", "token" or
// "password" is checked: if its value is a non-empty string not prefixed
// "{env:", the file is refused without ever printing the value, naming
// only the key's dotted JSON path (e.g. "provider.homelab.options.apiKey").
func LoadOpencodeConfig(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("opencode config %s: %v", path, err)
	}

	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "", fmt.Errorf("opencode config %s: %v", path, err)
	}

	if badPath := findInlineSecret(v, ""); badPath != "" {
		return "", fmt.Errorf("opencode config %s: %s holds an inline secret — use 'sbx secret set' or {env:VAR}", path, badPath)
	}

	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return "", fmt.Errorf("opencode config %s: %v", path, err)
	}
	return buf.String(), nil
}

// findInlineSecret walks v depth-first with map keys visited in sorted
// order (deterministic), returning the dotted path of the first secret
// key holding an inline (non-{env:}) string value, or "" if none.
func findInlineSecret(v any, path string) string {
	switch val := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			childPath := k
			if path != "" {
				childPath = path + "." + k
			}
			if secretKeys[strings.ToLower(k)] {
				if s, ok := val[k].(string); ok && s != "" && !strings.HasPrefix(s, "{env:") {
					return childPath
				}
			}
			if found := findInlineSecret(val[k], childPath); found != "" {
				return found
			}
		}
	case []any:
		for i, item := range val {
			if found := findInlineSecret(item, path+"."+strconv.Itoa(i)); found != "" {
				return found
			}
		}
	}
	return ""
}
