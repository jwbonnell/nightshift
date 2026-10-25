package runner

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRenderAgentLog(t *testing.T) {
	data, err := os.ReadFile("testdata/logview/agent.log")
	if err != nil {
		t.Fatal(err)
	}
	r := &Renderer{Roots: []string{"/home/u/.nightshift/runs/demo/repo"}, Loc: time.UTC}
	var got []string
	for _, l := range strings.Split(string(data), "\n") {
		got = append(got, r.Line(l)...)
	}
	gotS := strings.Join(got, "\n") + "\n"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile("testdata/logview/agent.golden", []byte(gotS), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/logview/agent.golden")
	if err != nil {
		t.Fatal(err)
	}
	if gotS != string(want) {
		t.Fatalf("got:\n%s\nwant:\n%s", gotS, want)
	}
}
