package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpencodeConfigValidation(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.json")
		_, err := LoadOpencodeConfig(path)
		if err == nil || !strings.HasPrefix(err.Error(), "opencode config "+path+": ") {
			t.Fatalf("err = %v, want prefix %q", err, "opencode config "+path+": ")
		}
	})

	t.Run("bad JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadOpencodeConfig(path)
		if err == nil || !strings.HasPrefix(err.Error(), "opencode config "+path+": ") {
			t.Fatalf("err = %v, want prefix %q", err, "opencode config "+path+": ")
		}
	})

	t.Run("inline apiKey refused", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		secret := "sk-super-secret-value"
		content := `{"provider":{"x":{"options":{"apiKey":"` + secret + `"}}}}`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadOpencodeConfig(path)
		if err == nil {
			t.Fatalf("expected an error for an inline secret")
		}
		if !strings.Contains(err.Error(), "provider.x.options.apiKey holds an inline secret") {
			t.Fatalf("err = %q, want it to name the key path", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("err = %q, must never contain the secret value", err)
		}
	})

	t.Run("env reference accepted and output compacted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		content := "{\n  \"provider\": {\n    \"x\": {\n      \"options\": {\n        \"apiKey\": \"{env:HOMELAB_KEY}\"\n      }\n    }\n  }\n}"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		got, err := LoadOpencodeConfig(path)
		if err != nil {
			t.Fatalf("LoadOpencodeConfig: %v", err)
		}
		want := `{"provider":{"x":{"options":{"apiKey":"{env:HOMELAB_KEY}"}}}}`
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("token/password/api_key also checked, case-insensitively", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		content := `{"a":{"Token":"inline"},"b":{"PASSWORD":"inline"},"c":{"api_key":"inline"}}`
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := LoadOpencodeConfig(path)
		if err == nil || !strings.Contains(err.Error(), "a.Token holds an inline secret") {
			t.Fatalf("err = %v, want it to flag the first (sorted) secret key a.Token", err)
		}
	})
}
