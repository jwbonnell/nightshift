package runner

import (
	"strings"
	"testing"

	"github.com/jwbonnell/nightshift/internal/home"
)

func TestPrintPlanIncludesHomeLineFirst(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "egress: api.example.com\n", repo)

	opts := Options{
		SpecArg:   "demo",
		Home:      homeDir,
		StateRoot: home.RunsDir(homeDir),
		SbxBin:    "sbx",
		ClaudeBin: "claude",
	}
	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	var buf strings.Builder
	printPlan(&buf, opts, sp, 2, "default")
	out := buf.String()

	lines := strings.Split(out, "\n")
	wantFirst := "home:        " + homeDir
	if lines[0] != wantFirst {
		t.Fatalf("first line = %q, want %q", lines[0], wantFirst)
	}

	for _, want := range []string{
		"spec:        " + sp.Path,
		"slug:        demo",
		"repo:        " + repo,
		"verify:      true",
		"model:       sonnet (default)",
		"planner:     opus (default)",
		"max_iter:    10 (default)",
		"iter_timeout:30m0s (default)",
		"egress+:     api.example.com",
		"max_concurrent: 2 (default)",
		"held locks:  none",
		"branch:      spec/demo",
		"sandboxes (agent=claude, ctl=git ops, verify=recreated fresh each verify):",
		"  sbx create --name nightshift-demo claude " + stateDirFor(opts, sp) + "/repo",
		"  sbx policy allow network --sandbox nightshift-demo api.example.com",
		"  sbx create --name nightshift-demo-ctl claude " + stateDirFor(opts, sp) + "/repo",
		"--- planning prompt ---",
		"--- iteration prompt ---",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestDryRunShowsReview(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "review: true\nreview_model: haiku\n", repo)

	opts := Options{
		SpecArg:   "demo",
		Home:      homeDir,
		StateRoot: home.RunsDir(homeDir),
		SbxBin:    "sbx",
		ClaudeBin: "claude",
	}
	sp, err := resolveSpec(opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	var buf strings.Builder
	printPlan(&buf, opts, sp, 2, "default")
	out := buf.String()

	lines := strings.Split(out, "\n")
	idx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "iter_timeout:") {
			idx = i
			break
		}
	}
	if idx == -1 {
		t.Fatalf("no iter_timeout: line in output:\n%s", out)
	}
	if want := "usage limit: none"; lines[idx+1] != want {
		t.Fatalf("line after iter_timeout: = %q, want %q", lines[idx+1], want)
	}
	want := "review:      true (frontmatter), model haiku (frontmatter)"
	if lines[idx+2] != want {
		t.Fatalf("line after usage limit: = %q, want %q", lines[idx+2], want)
	}
}
