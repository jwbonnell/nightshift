package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandTilde expands a leading "~/" using the real home directory; any
// other path is returned unchanged.
func ExpandTilde(p string) (string, error) {
	if !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	hd, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(hd, p[2:]), nil
}

// ResolveToken resolves a forge token for the given env var key (e.g.
// "NIGHTSHIFT_GITHUB_TOKEN"). An inline env value wins outright. Otherwise
// key+"_FILE" (env, then cfg) names a file, read after "~/" expansion and
// TrimSpace'd. Neither set gives ("", "", nil): the forge is simply not
// configured. A file that exists but can't be read is an error naming
// key+"_FILE"; a file whose mode grants group or other access produces a
// one-line warning instead of a refusal.
func ResolveToken(key string, getenv func(string) string, cfg map[string]string) (tok, warning string, err error) {
	if v := getenv(key); v != "" {
		return v, "", nil
	}

	fileKey := key + "_FILE"
	path := getenv(fileKey)
	if path == "" {
		path = cfg[fileKey]
	}
	if path == "" {
		return "", "", nil
	}

	expanded, err := ExpandTilde(path)
	if err != nil {
		return "", "", fmt.Errorf("%s=%s: %v", fileKey, path, err)
	}

	data, err := os.ReadFile(expanded)
	if err != nil {
		return "", "", fmt.Errorf("%s=%s: %v", fileKey, path, err)
	}
	if info, statErr := os.Stat(expanded); statErr == nil && info.Mode().Perm()&0o077 != 0 {
		warning = fmt.Sprintf("warning: %s is readable by other users — chmod 600 it", expanded)
	}
	return strings.TrimSpace(string(data)), warning, nil
}
