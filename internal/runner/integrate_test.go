package runner

import (
	"strings"
	"testing"
)

// TestBaseFastForwardsToDep covers the simplest integrated-base case: a
// single depends_on tip that descends straight from the current base, so
// integrateDeps fast-forwards onto it with no merge commit.
func TestBaseFastForwardsToDep(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-b", "spec/dep")
	commitFile(t, repo, "dep.go", "package dep\n", "dep work")
	depSHA := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))
	mustGit(t, repo, "checkout", "main")

	sb := newStubSandbox()
	r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)

	if !r.needIntegrate {
		t.Fatalf("needIntegrate = false, want true")
	}
	baseBefore := r.st.Base

	if status := r.integrateDeps(); status != "" {
		t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
	}

	if sb.verifyN != 1 {
		t.Fatalf("verifyN = %d, want 1", sb.verifyN)
	}
	if r.st.Base != depSHA {
		t.Fatalf("st.Base = %s, want dep tip %s", r.st.Base, depSHA)
	}
	if r.st.LastGreen != depSHA {
		t.Fatalf("st.LastGreen = %s, want dep tip %s", r.st.LastGreen, depSHA)
	}
	if r.st.BaseResolved != baseBefore {
		t.Fatalf("st.BaseResolved = %s, want original base %s", r.st.BaseResolved, baseBefore)
	}
	if r.st.DepTips["dep"] != depSHA {
		t.Fatalf("st.DepTips[dep] = %s, want %s", r.st.DepTips["dep"], depSHA)
	}

	merges, err := r.git("rev-list", "--merges", baseBefore+".."+r.st.Base)
	if err != nil {
		t.Fatalf("rev-list --merges: %v", err)
	}
	if strings.TrimSpace(merges) != "" {
		t.Fatalf("rev-list --merges = %q, want empty (fast-forward, no merge commit)", merges)
	}
}

// TestBaseMergesTwoDeps covers two depends_on tips applied in order: the
// first fast-forwards (it descends from the base), the second has
// genuinely diverged and needs a --no-ff merge.
func TestBaseMergesTwoDeps(t *testing.T) {
	repo := newSourceRepo(t)

	mustGit(t, repo, "checkout", "-b", "spec/a")
	commitFile(t, repo, "a.go", "package a\n", "a work")
	aSHA := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))

	mustGit(t, repo, "checkout", "main")
	mustGit(t, repo, "checkout", "-b", "spec/b")
	commitFile(t, repo, "b.go", "package b\n", "b work")
	bSHA := strings.TrimSpace(mustGit(t, repo, "rev-parse", "HEAD"))

	mustGit(t, repo, "checkout", "main")

	sb := newStubSandbox()
	r := newTestRun(t, repo, "depends_on: a, b\n", Options{}, sb)
	baseBefore := r.st.Base

	if status := r.integrateDeps(); status != "" {
		t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
	}

	if sb.verifyN != 1 {
		t.Fatalf("verifyN = %d, want 1", sb.verifyN)
	}

	merges, err := r.git("rev-list", "--merges", baseBefore+".."+r.st.Base)
	if err != nil {
		t.Fatalf("rev-list --merges: %v", err)
	}
	mergeSHAs := strings.Fields(merges)
	if len(mergeSHAs) != 1 {
		t.Fatalf("rev-list --merges = %q, want exactly one merge commit", merges)
	}
	mergeSHA := mergeSHAs[0]

	msg, err := r.git("log", "-1", "--format=%s", mergeSHA)
	if err != nil {
		t.Fatalf("log --format=%%s: %v", err)
	}
	if want := "nightshift: merge spec/b for demo"; strings.TrimSpace(msg) != want {
		t.Fatalf("merge message = %q, want %q", msg, want)
	}

	identity, err := r.git("log", "-1", "--format=%an <%ae>|%cn <%ce>", mergeSHA)
	if err != nil {
		t.Fatalf("log --format=author/committer: %v", err)
	}
	if want := "nightshift <nightshift@localhost>|nightshift <nightshift@localhost>"; strings.TrimSpace(identity) != want {
		t.Fatalf("author|committer = %q, want %q", identity, want)
	}

	if r.st.Base != mergeSHA {
		t.Fatalf("st.Base = %s, want merge commit %s", r.st.Base, mergeSHA)
	}
	if r.st.LastGreen != mergeSHA {
		t.Fatalf("st.LastGreen = %s, want merge commit %s", r.st.LastGreen, mergeSHA)
	}
	if r.st.BaseResolved != baseBefore {
		t.Fatalf("st.BaseResolved = %s, want original base %s", r.st.BaseResolved, baseBefore)
	}
	if r.st.DepTips["a"] != aSHA {
		t.Fatalf("st.DepTips[a] = %s, want %s", r.st.DepTips["a"], aSHA)
	}
	if r.st.DepTips["b"] != bSHA {
		t.Fatalf("st.DepTips[b] = %s, want %s", r.st.DepTips["b"], bSHA)
	}
}
