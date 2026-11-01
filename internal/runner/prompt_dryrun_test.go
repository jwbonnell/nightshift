package runner

import (
	"path/filepath"
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
		"max_iter:    10 (default)\nextend_iter: 10 (default)\niter_timeout:",
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

func TestDryRunShowsLensesAndRounds(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "review: requirements, security\nreview_model: haiku\nreview_rounds: 2\n", repo)

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
	want := "review:      requirements, security (frontmatter), model haiku (frontmatter), rounds 2 (frontmatter)"
	if lines[idx+2] != want {
		t.Fatalf("line after usage limit: = %q, want %q", lines[idx+2], want)
	}
}

func TestDryRunShowsImage(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "image: last-light-flutter:3.27.1\n", repo)

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

	mountDir := stateDirFor(opts, sp) + "/repo"
	for _, want := range []string{
		// Written as a literal, not via CloneDir, so the test pins the bytes.
		"clone:       " + filepath.Join(home.RunsDir(homeDir), "demo", "repo") + "\n",
		"image:       last-light-flutter:3.27.1 (agent, verify)",
		"  sbx create --name nightshift-demo --template last-light-flutter:3.27.1 claude " + mountDir,
		"  sbx create --name nightshift-demo-ctl claude " + mountDir,
		"  sbx rm -f nightshift-demo-verify && sbx create --name nightshift-demo-verify --template last-light-flutter:3.27.1 claude " + mountDir,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q\nfull output:\n%s", want, out)
		}
	}
}

func TestDryRunNoImageOmitsLineAndTemplate(t *testing.T) {
	repo := newSourceRepo(t)
	homeDir := t.TempDir()
	writeReadySpec(t, homeDir, "demo", "", repo)

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

	if strings.Contains(out, "image:") {
		t.Fatalf("dry-run output should not contain an image: line without image:\n%s", out)
	}
	if strings.Contains(out, "--template") {
		t.Fatalf("dry-run output should not contain --template without image:\n%s", out)
	}
}
