package runner

import (
	"os"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestLimitConfigDefaults(t *testing.T) {
	homeDir := t.TempDir()

	cfg, err := resolveLimitConfig(Options{Home: homeDir})
	if err != nil {
		t.Fatalf("resolveLimitConfig: %v", err)
	}
	if cfg.Fallback != 60*time.Minute || cfg.MaxWait != 6*time.Hour || cfg.MaxHits != 3 {
		t.Fatalf("cfg = %+v, want {60m 6h 3}", cfg)
	}
}

func TestLimitConfigPrecedence(t *testing.T) {
	homeDir := t.TempDir()

	if err := os.WriteFile(home.ConfigPath(homeDir), []byte(
		"NIGHTSHIFT_LIMIT_FALLBACK=30m\nNIGHTSHIFT_LIMIT_MAX_WAIT=2h\nNIGHTSHIFT_LIMIT_MAX_HITS=5\n",
	), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := resolveLimitConfig(Options{Home: homeDir})
	if err != nil {
		t.Fatalf("resolveLimitConfig: %v", err)
	}
	if cfg.Fallback != 30*time.Minute || cfg.MaxWait != 2*time.Hour || cfg.MaxHits != 5 {
		t.Fatalf("cfg = %+v, want {30m 2h 5} from config", cfg)
	}

	t.Setenv("NIGHTSHIFT_LIMIT_FALLBACK", "45m")
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_WAIT", "3h")
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_HITS", "7")

	cfg, err = resolveLimitConfig(Options{Home: homeDir})
	if err != nil {
		t.Fatalf("resolveLimitConfig: %v", err)
	}
	if cfg.Fallback != 45*time.Minute || cfg.MaxWait != 3*time.Hour || cfg.MaxHits != 7 {
		t.Fatalf("cfg = %+v, want {45m 3h 7} from env", cfg)
	}
}

func TestLimitConfigInvalid(t *testing.T) {
	homeDir := t.TempDir()

	t.Setenv("NIGHTSHIFT_LIMIT_FALLBACK", "0s")
	_, err := resolveLimitConfig(Options{Home: homeDir})
	if err == nil || err.Error() != `NIGHTSHIFT_LIMIT_FALLBACK="0s": want a positive duration` {
		t.Fatalf("err = %v, want NIGHTSHIFT_LIMIT_FALLBACK=\"0s\": want a positive duration", err)
	}
	t.Setenv("NIGHTSHIFT_LIMIT_FALLBACK", "")

	t.Setenv("NIGHTSHIFT_LIMIT_MAX_WAIT", "nope")
	_, err = resolveLimitConfig(Options{Home: homeDir})
	if err == nil || err.Error() != `NIGHTSHIFT_LIMIT_MAX_WAIT="nope": want a positive duration` {
		t.Fatalf("err = %v, want NIGHTSHIFT_LIMIT_MAX_WAIT=\"nope\": want a positive duration", err)
	}
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_WAIT", "")

	t.Setenv("NIGHTSHIFT_LIMIT_MAX_HITS", "0")
	_, err = resolveLimitConfig(Options{Home: homeDir})
	if err == nil || err.Error() != `NIGHTSHIFT_LIMIT_MAX_HITS="0": want a positive integer` {
		t.Fatalf("err = %v, want NIGHTSHIFT_LIMIT_MAX_HITS=\"0\": want a positive integer", err)
	}
	t.Setenv("NIGHTSHIFT_LIMIT_MAX_HITS", "abc")
	_, err = resolveLimitConfig(Options{Home: homeDir})
	if err == nil || err.Error() != `NIGHTSHIFT_LIMIT_MAX_HITS="abc": want a positive integer` {
		t.Fatalf("err = %v, want NIGHTSHIFT_LIMIT_MAX_HITS=\"abc\": want a positive integer", err)
	}
}
