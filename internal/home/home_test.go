package home

import (
	"path/filepath"
	"testing"
)

func TestHomeFromEnv(t *testing.T) {
	t.Run("env wins", func(t *testing.T) {
		t.Setenv("NIGHTSHIFT_HOME", "/custom/home")
		got, err := Home()
		if err != nil {
			t.Fatalf("Home: %v", err)
		}
		if got != "/custom/home" {
			t.Fatalf("Home = %q, want %q", got, "/custom/home")
		}
	})

	t.Run("unset defaults to tilde nightshift", func(t *testing.T) {
		t.Setenv("NIGHTSHIFT_HOME", "")
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)

		got, err := Home()
		if err != nil {
			t.Fatalf("Home: %v", err)
		}
		want := filepath.Join(tmp, ".nightshift")
		if got != want {
			t.Fatalf("Home = %q, want %q", got, want)
		}
	})
}
