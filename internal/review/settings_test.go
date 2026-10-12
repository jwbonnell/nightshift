package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/usagelimit"
)

// envMap builds a getenv func backed by a plain map, so tests never touch
// real process env vars.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestSettingsDefaults(t *testing.T) {
	home := t.TempDir()
	s, err := ResolveSettings(FlagValues{}, home, envMap(nil))
	if err != nil {
		t.Fatalf("ResolveSettings: %v", err)
	}

	if s.Model != "sonnet" || s.ModelSrc != "default" {
		t.Errorf("Model = %q (src %q), want sonnet (default)", s.Model, s.ModelSrc)
	}
	if s.Selector.Agent != "claude" || s.Selector.Model != "sonnet" {
		t.Errorf("Selector = %+v, want claude/sonnet", s.Selector)
	}
	if s.MaxTurns != 30 {
		t.Errorf("MaxTurns = %d, want 30", s.MaxTurns)
	}
	if s.Timeout != 15*time.Minute {
		t.Errorf("Timeout = %v, want 15m", s.Timeout)
	}
	if s.MaxLines != 3000 {
		t.Errorf("MaxLines = %d, want 3000", s.MaxLines)
	}
	if s.SbxBin != "sbx" {
		t.Errorf("SbxBin = %q, want sbx", s.SbxBin)
	}
	if s.ClaudeBin != "claude" {
		t.Errorf("ClaudeBin = %q, want claude", s.ClaudeBin)
	}
	if s.OpencodeBin != "opencode" {
		t.Errorf("OpencodeBin = %q, want opencode", s.OpencodeBin)
	}
	if s.GitHubURL != "https://github.com" {
		t.Errorf("GitHubURL = %q, want https://github.com", s.GitHubURL)
	}
	if s.GitLabURL != "" {
		t.Errorf("GitLabURL = %q, want empty", s.GitLabURL)
	}
	if s.Mirror != "" {
		t.Errorf("Mirror = %q, want empty", s.Mirror)
	}
	if len(s.Egress) != 0 {
		t.Errorf("Egress = %v, want empty", s.Egress)
	}
	if s.Notify != "on" {
		t.Errorf("Notify = %q, want on", s.Notify)
	}
	if s.LimitFallback != usagelimit.DefaultFallback {
		t.Errorf("LimitFallback = %v, want %v", s.LimitFallback, usagelimit.DefaultFallback)
	}
	if s.Configured(forge.KindGitLab) || s.Configured(forge.KindGitHub) {
		t.Errorf("no forge should be configured with no env/config set")
	}
}

func TestSettingsEnvOverridesDefault(t *testing.T) {
	home := t.TempDir()
	env := envMap(map[string]string{
		"NIGHTSHIFT_PR_MODEL":     "haiku",
		"NIGHTSHIFT_PR_MAX_TURNS": "10",
		"NIGHTSHIFT_PR_TIMEOUT":   "5m",
		"NIGHTSHIFT_PR_MAX_LINES": "500",
		"NIGHTSHIFT_SBX_BIN":      "/opt/sbx",
	})
	s, err := ResolveSettings(FlagValues{}, home, env)
	if err != nil {
		t.Fatalf("ResolveSettings: %v", err)
	}
	if s.Model != "haiku" || s.ModelSrc != "env" {
		t.Errorf("Model = %q (src %q), want haiku (env)", s.Model, s.ModelSrc)
	}
	if s.MaxTurns != 10 {
		t.Errorf("MaxTurns = %d, want 10", s.MaxTurns)
	}
	if s.Timeout != 5*time.Minute {
		t.Errorf("Timeout = %v, want 5m", s.Timeout)
	}
	if s.MaxLines != 500 {
		t.Errorf("MaxLines = %d, want 500", s.MaxLines)
	}
	if s.SbxBin != "/opt/sbx" {
		t.Errorf("SbxBin = %q, want /opt/sbx", s.SbxBin)
	}
}

func TestSettingsFlagOverridesEnv(t *testing.T) {
	home := t.TempDir()
	env := envMap(map[string]string{
		"NIGHTSHIFT_PR_MODEL":     "haiku",
		"NIGHTSHIFT_PR_MAX_TURNS": "10",
		"NIGHTSHIFT_PR_TIMEOUT":   "5m",
		"NIGHTSHIFT_PR_MAX_LINES": "500",
	})
	flags := FlagValues{Model: "opus", MaxTurns: 5, Timeout: "1m", MaxLines: 42}
	s, err := ResolveSettings(flags, home, env)
	if err != nil {
		t.Fatalf("ResolveSettings: %v", err)
	}
	if s.Model != "opus" || s.ModelSrc != "flag" {
		t.Errorf("Model = %q (src %q), want opus (flag)", s.Model, s.ModelSrc)
	}
	if s.MaxTurns != 5 {
		t.Errorf("MaxTurns = %d, want 5", s.MaxTurns)
	}
	if s.Timeout != time.Minute {
		t.Errorf("Timeout = %v, want 1m", s.Timeout)
	}
	if s.MaxLines != 42 {
		t.Errorf("MaxLines = %d, want 42", s.MaxLines)
	}
}

func TestSettingsBadEnvErrors(t *testing.T) {
	home := t.TempDir()
	cases := map[string]string{
		"NIGHTSHIFT_PR_TIMEOUT":   "not-a-duration",
		"NIGHTSHIFT_PR_MAX_TURNS": "abc",
		"NIGHTSHIFT_PR_MAX_LINES": "-5",
		"NIGHTSHIFT_PR_MODEL":     "opencode/missing-model",
	}
	for key, bad := range cases {
		t.Run(key, func(t *testing.T) {
			env := envMap(map[string]string{key: bad})
			if _, err := ResolveSettings(FlagValues{}, home, env); err == nil {
				t.Fatalf("%s=%q: want error", key, bad)
			}
		})
	}
}

func TestSettingsCarriesConfig(t *testing.T) {
	home := t.TempDir()
	tokDir := t.TempDir()
	glTok := filepath.Join(tokDir, "gitlab.tok")
	ghTok := filepath.Join(tokDir, "github.tok")
	if err := os.WriteFile(glTok, []byte("gl-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ghTok, []byte("gh-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := "NIGHTSHIFT_GITLAB_URL=https://gitlab.example.com\n" +
		"NIGHTSHIFT_GITLAB_TOKEN_FILE=" + glTok + "\n" +
		"NIGHTSHIFT_GITHUB_TOKEN_FILE=" + ghTok + "\n"
	if err := os.WriteFile(filepath.Join(home, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := ResolveSettings(FlagValues{}, home, envMap(nil))
	if err != nil {
		t.Fatalf("ResolveSettings: %v", err)
	}
	if s.GitLabURL != "https://gitlab.example.com" {
		t.Errorf("GitLabURL = %q", s.GitLabURL)
	}
	if s.GitLabToken != "gl-secret" {
		t.Errorf("GitLabToken = %q", s.GitLabToken)
	}
	if s.GitHubToken != "gh-secret" {
		t.Errorf("GitHubToken = %q", s.GitHubToken)
	}
	if !s.Configured(forge.KindGitLab) {
		t.Error("gitlab should be configured")
	}
	if !s.Configured(forge.KindGitHub) {
		t.Error("github should be configured")
	}
}

func TestPRSelectorErrors(t *testing.T) {
	home := t.TempDir()
	_, err := ResolveSettings(FlagValues{}, home, envMap(map[string]string{
		"NIGHTSHIFT_PR_MODEL": "bogus/thing/here/extra",
	}))
	if err == nil {
		t.Fatal("want error for a bad selector")
	}
	want := "NIGHTSHIFT_PR_MODEL (from env): "
	if got := err.Error(); len(got) < len(want) || got[:len(want)] != want {
		t.Errorf("error = %q, want prefix %q", got, want)
	}
}

func TestPREgress(t *testing.T) {
	home := t.TempDir()

	t.Run("claude no egress", func(t *testing.T) {
		s, err := ResolveSettings(FlagValues{}, home, envMap(nil))
		if err != nil {
			t.Fatalf("ResolveSettings: %v", err)
		}
		if len(s.Egress) != 0 {
			t.Errorf("Egress = %v, want empty", s.Egress)
		}
	})

	t.Run("opencode zen plus extra, deduped", func(t *testing.T) {
		env := envMap(map[string]string{
			"NIGHTSHIFT_PR_MODEL":  "opencode/opencode/longcat-2.5-preview-free",
			"NIGHTSHIFT_PR_EGRESS": "a.lan, b.lan:8080, opencode.ai, a.lan",
		})
		s, err := ResolveSettings(FlagValues{}, home, env)
		if err != nil {
			t.Fatalf("ResolveSettings: %v", err)
		}
		want := []string{"opencode.ai", "a.lan", "b.lan:8080"}
		if len(s.Egress) != len(want) {
			t.Fatalf("Egress = %v, want %v", s.Egress, want)
		}
		for i, h := range want {
			if s.Egress[i] != h {
				t.Errorf("Egress[%d] = %q, want %q", i, s.Egress[i], h)
			}
		}
	})

	t.Run("non-opencode provider gets no zen host", func(t *testing.T) {
		env := envMap(map[string]string{
			"NIGHTSHIFT_PR_MODEL": "opencode/anthropic/claude-3",
		})
		s, err := ResolveSettings(FlagValues{}, home, env)
		if err != nil {
			t.Fatalf("ResolveSettings: %v", err)
		}
		if len(s.Egress) != 0 {
			t.Errorf("Egress = %v, want empty", s.Egress)
		}
	})

	t.Run("bad entry errors", func(t *testing.T) {
		env := envMap(map[string]string{
			"NIGHTSHIFT_PR_EGRESS": "not a host!",
		})
		_, err := ResolveSettings(FlagValues{}, home, env)
		if err == nil {
			t.Fatal("want error for a bad egress entry")
		}
	})
}
