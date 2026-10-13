package home

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigFileParse(t *testing.T) {
	t.Run("missing file is not an error", func(t *testing.T) {
		cfg, err := ParseConfig(filepath.Join(t.TempDir(), "does-not-exist"))
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}
		if cfg != nil {
			t.Fatalf("cfg = %+v, want nil", cfg)
		}
	})

	t.Run("comments blanks quotes and duplicates", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		content := strings.Join([]string{
			"# a full-line comment",
			"",
			"   ",
			"  # indented comment",
			`NIGHTSHIFT_MODEL="opus"`,
			"NIGHTSHIFT_MODEL=sonnet",
			"NIGHTSHIFT_SBX_BIN='/usr/local/bin/sbx'",
			"NIGHTSHIFT_NOTIFY = off ",
			`NIGHTSHIFT_BASE="mismatched'`,
			"",
		}, "\n")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		cfg, err := ParseConfig(path)
		if err != nil {
			t.Fatalf("ParseConfig: %v", err)
		}

		want := map[string]string{
			"NIGHTSHIFT_MODEL":   "sonnet", // duplicate key: later wins
			"NIGHTSHIFT_SBX_BIN": "/usr/local/bin/sbx",
			"NIGHTSHIFT_NOTIFY":  "off",
			"NIGHTSHIFT_BASE":    `"mismatched'`, // mismatched quotes kept
		}
		if len(cfg) != len(want) {
			t.Fatalf("cfg = %+v, want %+v", cfg, want)
		}
		for k, v := range want {
			if cfg[k] != v {
				t.Fatalf("cfg[%s] = %q, want %q", k, cfg[k], v)
			}
		}
	})

	t.Run("unknown key is a hard error with line number", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		content := "NIGHTSHIFT_MODEL=sonnet\nNIGHTSHIFT_BOGUS=1\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		_, err := ParseConfig(path)
		if err == nil {
			t.Fatalf("expected an error")
		}
		want := "config " + path + ` line 2: unknown key "NIGHTSHIFT_BOGUS"`
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("missing equals is a hard error with line number", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config")
		content := "NIGHTSHIFT_MODEL=sonnet\nthis line has no equals\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		_, err := ParseConfig(path)
		if err == nil {
			t.Fatalf("expected an error")
		}
		want := "config " + path + " line 2: expected KEY=value"
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})
}

func TestConfigAcceptsInterviewKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := "NIGHTSHIFT_HOST_CLAUDE_BIN=/usr/local/bin/claude\nNIGHTSHIFT_INTERVIEW_MODEL=sonnet\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg["NIGHTSHIFT_HOST_CLAUDE_BIN"] != "/usr/local/bin/claude" {
		t.Fatalf("NIGHTSHIFT_HOST_CLAUDE_BIN = %q", cfg["NIGHTSHIFT_HOST_CLAUDE_BIN"])
	}
	if cfg["NIGHTSHIFT_INTERVIEW_MODEL"] != "sonnet" {
		t.Fatalf("NIGHTSHIFT_INTERVIEW_MODEL = %q", cfg["NIGHTSHIFT_INTERVIEW_MODEL"])
	}

	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_HOST_CLAUDE_BIN=claude") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_HOST_CLAUDE_BIN line:\n%s", commentedConfig)
	}
	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_INTERVIEW_MODEL=opus") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_INTERVIEW_MODEL line:\n%s", commentedConfig)
	}
}

func TestConfigAcceptsReviewKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := "NIGHTSHIFT_REVIEW=true\nNIGHTSHIFT_REVIEW_MODEL=sonnet\nNIGHTSHIFT_REVIEW_EGRESS=a.lan,b.lan:8080\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg["NIGHTSHIFT_REVIEW"] != "true" {
		t.Fatalf("NIGHTSHIFT_REVIEW = %q", cfg["NIGHTSHIFT_REVIEW"])
	}
	if cfg["NIGHTSHIFT_REVIEW_MODEL"] != "sonnet" {
		t.Fatalf("NIGHTSHIFT_REVIEW_MODEL = %q", cfg["NIGHTSHIFT_REVIEW_MODEL"])
	}
	if cfg["NIGHTSHIFT_REVIEW_EGRESS"] != "a.lan,b.lan:8080" {
		t.Fatalf("NIGHTSHIFT_REVIEW_EGRESS = %q", cfg["NIGHTSHIFT_REVIEW_EGRESS"])
	}

	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_REVIEW=false") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_REVIEW line:\n%s", commentedConfig)
	}
	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_REVIEW_MODEL=opus") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_REVIEW_MODEL line:\n%s", commentedConfig)
	}
	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_REVIEW_EGRESS=") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_REVIEW_EGRESS line:\n%s", commentedConfig)
	}
}

func TestConfigAcceptsLimitKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := "NIGHTSHIFT_LIMIT_FALLBACK=30m\nNIGHTSHIFT_LIMIT_MAX_WAIT=2h\nNIGHTSHIFT_LIMIT_MAX_HITS=5\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg["NIGHTSHIFT_LIMIT_FALLBACK"] != "30m" {
		t.Fatalf("NIGHTSHIFT_LIMIT_FALLBACK = %q", cfg["NIGHTSHIFT_LIMIT_FALLBACK"])
	}
	if cfg["NIGHTSHIFT_LIMIT_MAX_WAIT"] != "2h" {
		t.Fatalf("NIGHTSHIFT_LIMIT_MAX_WAIT = %q", cfg["NIGHTSHIFT_LIMIT_MAX_WAIT"])
	}
	if cfg["NIGHTSHIFT_LIMIT_MAX_HITS"] != "5" {
		t.Fatalf("NIGHTSHIFT_LIMIT_MAX_HITS = %q", cfg["NIGHTSHIFT_LIMIT_MAX_HITS"])
	}

	for _, line := range []string{
		"# NIGHTSHIFT_LIMIT_FALLBACK=60m",
		"# NIGHTSHIFT_LIMIT_MAX_WAIT=6h",
		"# NIGHTSHIFT_LIMIT_MAX_HITS=3",
	} {
		if !strings.Contains(commentedConfig, line) {
			t.Fatalf("commentedConfig missing %q line:\n%s", line, commentedConfig)
		}
	}
}

func TestOpencodeKeysInConfigAllowList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := "NIGHTSHIFT_OPENCODE_BIN=/usr/local/bin/opencode\nNIGHTSHIFT_OPENCODE_CONFIG=~/opencode.json\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	if cfg["NIGHTSHIFT_OPENCODE_BIN"] != "/usr/local/bin/opencode" {
		t.Fatalf("NIGHTSHIFT_OPENCODE_BIN = %q", cfg["NIGHTSHIFT_OPENCODE_BIN"])
	}
	if cfg["NIGHTSHIFT_OPENCODE_CONFIG"] != "~/opencode.json" {
		t.Fatalf("NIGHTSHIFT_OPENCODE_CONFIG = %q", cfg["NIGHTSHIFT_OPENCODE_CONFIG"])
	}

	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_OPENCODE_BIN=opencode") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_OPENCODE_BIN line:\n%s", commentedConfig)
	}
	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_OPENCODE_CONFIG=") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_OPENCODE_CONFIG line:\n%s", commentedConfig)
	}
}

func TestConfigAcceptsPRKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	content := strings.Join([]string{
		"NIGHTSHIFT_PR_MODEL=opus",
		"NIGHTSHIFT_PR_MAX_TURNS=10",
		"NIGHTSHIFT_PR_TIMEOUT=5m",
		"NIGHTSHIFT_PR_MAX_LINES=500",
		"NIGHTSHIFT_PR_PROMPT=/tmp/prompt.md",
		"NIGHTSHIFT_PR_MIRROR=/tmp/mirror",
		"NIGHTSHIFT_PR_EGRESS=a.lan,b.lan:8080",
		"NIGHTSHIFT_GITLAB_URL=https://gitlab.example.com",
		"NIGHTSHIFT_GITLAB_TOKEN_FILE=~/.gitlab-token",
		"NIGHTSHIFT_GITHUB_URL=https://ghe.example.com",
		"NIGHTSHIFT_GITHUB_TOKEN_FILE=~/.github-token",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := ParseConfig(path)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	want := map[string]string{
		"NIGHTSHIFT_PR_MODEL":          "opus",
		"NIGHTSHIFT_PR_MAX_TURNS":      "10",
		"NIGHTSHIFT_PR_TIMEOUT":        "5m",
		"NIGHTSHIFT_PR_MAX_LINES":      "500",
		"NIGHTSHIFT_PR_PROMPT":         "/tmp/prompt.md",
		"NIGHTSHIFT_PR_MIRROR":         "/tmp/mirror",
		"NIGHTSHIFT_PR_EGRESS":         "a.lan,b.lan:8080",
		"NIGHTSHIFT_GITLAB_URL":        "https://gitlab.example.com",
		"NIGHTSHIFT_GITLAB_TOKEN_FILE": "~/.gitlab-token",
		"NIGHTSHIFT_GITHUB_URL":        "https://ghe.example.com",
		"NIGHTSHIFT_GITHUB_TOKEN_FILE": "~/.github-token",
	}
	for k, v := range want {
		if cfg[k] != v {
			t.Fatalf("cfg[%s] = %q, want %q", k, cfg[k], v)
		}
	}

	for _, line := range []string{
		"# NIGHTSHIFT_PR_MODEL=sonnet",
		"# free opencode reviewer: NIGHTSHIFT_PR_MODEL=opencode/opencode/longcat-2.5-preview-free",
		"# NIGHTSHIFT_PR_MAX_TURNS=30",
		"# NIGHTSHIFT_PR_TIMEOUT=15m",
		"# NIGHTSHIFT_PR_MAX_LINES=3000",
		"# NIGHTSHIFT_PR_PROMPT=",
		"# NIGHTSHIFT_PR_MIRROR=",
		"# NIGHTSHIFT_PR_EGRESS=",
		"# NIGHTSHIFT_GITLAB_URL=",
		"# NIGHTSHIFT_GITLAB_TOKEN_FILE=",
		"# NIGHTSHIFT_GITHUB_URL=https://github.com",
		"# NIGHTSHIFT_GITHUB_TOKEN_FILE=",
	} {
		if !strings.Contains(commentedConfig, line) {
			t.Fatalf("commentedConfig missing %q line:\n%s", line, commentedConfig)
		}
	}

	// Verify the opencode-reviewer comment sits directly under the
	// NIGHTSHIFT_PR_MODEL default line (P3).
	idx := strings.Index(commentedConfig, "# NIGHTSHIFT_PR_MODEL=sonnet\n")
	if idx < 0 {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_PR_MODEL default line")
	}
	rest := commentedConfig[idx+len("# NIGHTSHIFT_PR_MODEL=sonnet\n"):]
	if !strings.HasPrefix(rest, "# free opencode reviewer: NIGHTSHIFT_PR_MODEL=opencode/opencode/longcat-2.5-preview-free\n") {
		t.Fatalf("opencode-reviewer comment doesn't directly follow NIGHTSHIFT_PR_MODEL default:\n%s", commentedConfig)
	}
}

func TestConfigRejectsInlineTokens(t *testing.T) {
	for _, key := range []string{"NIGHTSHIFT_GITLAB_TOKEN", "NIGHTSHIFT_GITHUB_TOKEN"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			content := "NIGHTSHIFT_MODEL=sonnet\n" + key + "=sekrit\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			_, err := ParseConfig(path)
			if err == nil {
				t.Fatalf("expected an error")
			}
			want := fmt.Sprintf("config %s line 2: tokens don't belong in the config file — put it in a file and set %s_FILE", path, key)
			if err.Error() != want {
				t.Fatalf("err = %q, want %q", err.Error(), want)
			}
		})

		t.Run(key+"_FILE accepted", func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			content := key + "_FILE=~/token\n"
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}

			cfg, err := ParseConfig(path)
			if err != nil {
				t.Fatalf("ParseConfig: %v", err)
			}
			if cfg[key+"_FILE"] != "~/token" {
				t.Fatalf("cfg[%s_FILE] = %q", key, cfg[key+"_FILE"])
			}
		})
	}
}

func TestTokenFileLoadingAndWarning(t *testing.T) {
	t.Run("inline env wins over file", func(t *testing.T) {
		dir := t.TempDir()
		tokenPath := filepath.Join(dir, "token")
		if err := os.WriteFile(tokenPath, []byte("from-file\n"), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		getenv := func(k string) string {
			switch k {
			case "NIGHTSHIFT_GITHUB_TOKEN":
				return "from-env"
			case "NIGHTSHIFT_GITHUB_TOKEN_FILE":
				return tokenPath
			}
			return ""
		}
		tok, warning, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, nil)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		if tok != "from-env" || warning != "" {
			t.Fatalf("tok=%q warning=%q, want from-env/empty", tok, warning)
		}
	})

	t.Run("tilde expansion and trim, env file path wins over config", func(t *testing.T) {
		hd := t.TempDir()
		t.Setenv("HOME", hd)
		if err := os.MkdirAll(filepath.Join(hd, "secrets"), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		tokenPath := filepath.Join(hd, "secrets", "gh-token")
		if err := os.WriteFile(tokenPath, []byte("  tok-value  \n"), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		getenv := func(k string) string {
			if k == "NIGHTSHIFT_GITHUB_TOKEN_FILE" {
				return "~/secrets/gh-token"
			}
			return ""
		}
		cfg := map[string]string{"NIGHTSHIFT_GITHUB_TOKEN_FILE": "/should/not/be/used"}
		tok, warning, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, cfg)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		if tok != "tok-value" {
			t.Fatalf("tok = %q, want %q", tok, "tok-value")
		}
		if warning != "" {
			t.Fatalf("warning = %q, want empty for a 0600 file", warning)
		}
	})

	t.Run("falls back to config when env file path unset", func(t *testing.T) {
		dir := t.TempDir()
		tokenPath := filepath.Join(dir, "token")
		if err := os.WriteFile(tokenPath, []byte("cfg-tok"), 0o600); err != nil {
			t.Fatalf("write token file: %v", err)
		}
		getenv := func(string) string { return "" }
		cfg := map[string]string{"NIGHTSHIFT_GITHUB_TOKEN_FILE": tokenPath}
		tok, _, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, cfg)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		if tok != "cfg-tok" {
			t.Fatalf("tok = %q, want %q", tok, "cfg-tok")
		}
	})

	t.Run("neither set is not configured", func(t *testing.T) {
		getenv := func(string) string { return "" }
		tok, warning, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, nil)
		if err != nil || tok != "" || warning != "" {
			t.Fatalf("tok=%q warning=%q err=%v, want all empty/nil", tok, warning, err)
		}
	})

	t.Run("unreadable file is an error naming the _FILE key", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope")
		getenv := func(k string) string {
			if k == "NIGHTSHIFT_GITHUB_TOKEN_FILE" {
				return missing
			}
			return ""
		}
		tok, _, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, nil)
		if tok != "" {
			t.Fatalf("tok = %q, want empty", tok)
		}
		if err == nil {
			t.Fatalf("expected an error")
		}
		want := fmt.Sprintf("NIGHTSHIFT_GITHUB_TOKEN_FILE=%s: ", missing)
		if !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("err = %q, want prefix %q", err.Error(), want)
		}
	})

	t.Run("world-readable file warns once, 0600 is silent", func(t *testing.T) {
		dir := t.TempDir()
		loosePath := filepath.Join(dir, "loose")
		if err := os.WriteFile(loosePath, []byte("tok"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		getenv := func(k string) string {
			if k == "NIGHTSHIFT_GITHUB_TOKEN_FILE" {
				return loosePath
			}
			return ""
		}
		_, warning, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv, nil)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		want := fmt.Sprintf("warning: %s is readable by other users — chmod 600 it", loosePath)
		if warning != want {
			t.Fatalf("warning = %q, want %q", warning, want)
		}

		tightPath := filepath.Join(dir, "tight")
		if err := os.WriteFile(tightPath, []byte("tok"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		getenv2 := func(k string) string {
			if k == "NIGHTSHIFT_GITHUB_TOKEN_FILE" {
				return tightPath
			}
			return ""
		}
		_, warning2, err := ResolveToken("NIGHTSHIFT_GITHUB_TOKEN", getenv2, nil)
		if err != nil {
			t.Fatalf("ResolveToken: %v", err)
		}
		if warning2 != "" {
			t.Fatalf("warning = %q, want empty for a 0600 file", warning2)
		}
	})
}
