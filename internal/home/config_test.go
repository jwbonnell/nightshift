package home

import (
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
	content := "NIGHTSHIFT_REVIEW=true\nNIGHTSHIFT_REVIEW_MODEL=sonnet\n"
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

	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_REVIEW=false") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_REVIEW line:\n%s", commentedConfig)
	}
	if !strings.Contains(commentedConfig, "# NIGHTSHIFT_REVIEW_MODEL=opus") {
		t.Fatalf("commentedConfig missing NIGHTSHIFT_REVIEW_MODEL line:\n%s", commentedConfig)
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
