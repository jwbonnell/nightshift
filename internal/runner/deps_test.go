package runner

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestDependsOnAndOwnsParse(t *testing.T) {
	t.Run("depends_on list and dedup", func(t *testing.T) {
		got, err := parseDependsOn(` a, b , a,"c" `)
		if err != nil {
			t.Fatalf("parseDependsOn: %v", err)
		}
		want := []string{"a", "b", "c"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("depends_on empty", func(t *testing.T) {
		got, err := parseDependsOn("")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})

	t.Run("depends_on bad slug", func(t *testing.T) {
		_, err := parseDependsOn("Bad Slug!")
		if err == nil {
			t.Fatal("want error")
		}
		want := `depends_on entry "Bad Slug!": want a spec slug`
		if err.Error() != want {
			t.Fatalf("err = %q, want %q", err.Error(), want)
		}
	})

	t.Run("owns list and dedup", func(t *testing.T) {
		got, err := parseOwns(` internal/api/, go.mod ,internal/api/`)
		if err != nil {
			t.Fatalf("parseOwns: %v", err)
		}
		want := []string{"internal/api/", "go.mod"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("owns empty", func(t *testing.T) {
		got, err := parseOwns("")
		if err != nil || got != nil {
			t.Fatalf("got %v, %v; want nil, nil", got, err)
		}
	})

	for _, bad := range []string{"..", "../x", "/abs/path", "a/../b", ".", "./"} {
		t.Run("owns bad "+bad, func(t *testing.T) {
			_, err := parseOwns(bad)
			if err == nil {
				t.Fatalf("parseOwns(%q): want error", bad)
			}
			want := `owns entry "` + bad + `": want a clean repo-relative path`
			if err.Error() != want {
				t.Fatalf("err = %q, want %q", err.Error(), want)
			}
		})
	}
}

func TestLintAllowsDependsOwns(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeLintSpec(t, home.DraftDir(homeDir), "demo", "depends_on: other\nowns: internal/api/, go.mod\n", repo, fullSpecBody)

	var buf bytes.Buffer
	res, err := Lint(LintOptions{Home: homeDir, SpecArg: "demo", NoVerify: true, Stdout: &buf})
	if err != nil {
		t.Fatalf("Lint: %v", err)
	}
	if res.Warns != 0 {
		t.Fatalf("res = %+v, want zero warns; output:\n%s", res, buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte("unknown key")) {
		t.Fatalf("output unexpectedly warns about depends_on/owns:\n%s", buf.String())
	}
}
