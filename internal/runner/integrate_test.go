package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// TestDepAlreadyMergedOrDeletedSkipped covers the two cases D-g skips
// without merging: a tip that's already an ancestor of the base (the
// owner merged it upstream already), and a missing branch (the owner
// merged and deleted it).
func TestDepAlreadyMergedOrDeletedSkipped(t *testing.T) {
	t.Run("already an ancestor", func(t *testing.T) {
		repo := newSourceRepo(t)
		mustGit(t, repo, "checkout", "-b", "spec/dep")
		commitFile(t, repo, "dep.go", "package dep\n", "dep work")
		mustGit(t, repo, "checkout", "main")
		mustGit(t, repo, "merge", "--no-ff", "-m", "merge dep", "spec/dep")

		sb := newStubSandbox()
		r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)
		baseBefore := r.st.Base
		depTip := mustGit(t, repo, "rev-parse", "spec/dep")

		if status := r.integrateDeps(); status != "" {
			t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
		}
		if sb.verifyN != 0 {
			t.Fatalf("verifyN = %d, want 0 (nothing applied)", sb.verifyN)
		}
		if r.st.Base != baseBefore {
			t.Fatalf("st.Base = %s, want unchanged %s", r.st.Base, baseBefore)
		}
		if got := r.st.DepTips["dep"]; got != depTip {
			t.Fatalf("st.DepTips[dep] = %q, want %q (recorded even though already in base)", got, depTip)
		}
		if r.st.BaseResolved != baseBefore {
			t.Fatalf("st.BaseResolved = %s, want %s", r.st.BaseResolved, baseBefore)
		}
	})

	t.Run("branch deleted", func(t *testing.T) {
		repo := newSourceRepo(t)
		mustGit(t, repo, "checkout", "-b", "spec/dep")
		commitFile(t, repo, "dep.go", "package dep\n", "dep work")
		mustGit(t, repo, "checkout", "main")
		mustGit(t, repo, "branch", "-D", "spec/dep")

		sb := newStubSandbox()
		r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)
		baseBefore := r.st.Base

		if status := r.integrateDeps(); status != "" {
			t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
		}
		if sb.verifyN != 0 {
			t.Fatalf("verifyN = %d, want 0 (nothing applied)", sb.verifyN)
		}
		if r.st.Base != baseBefore {
			t.Fatalf("st.Base = %s, want unchanged %s", r.st.Base, baseBefore)
		}
		if r.st.DepTips != nil {
			t.Fatalf("st.DepTips = %v, want nil", r.st.DepTips)
		}
	})
}

// TestDepConflictBlocks covers two depends_on tips that conflict with each
// other: the first (a) fast-forwards in; the second (b) diverged from the
// same base and edits the same line, so the --no-ff merge conflicts. That
// must abort the merge, leave no remotes, run no agent iteration, and
// write the conflict question with the path.
func TestDepConflictBlocks(t *testing.T) {
	repo := newSourceRepo(t)

	mustGit(t, repo, "checkout", "-b", "spec/a")
	commitFile(t, repo, "main.go", "package main\n\nvar X = \"a\"\n", "a work")

	mustGit(t, repo, "checkout", "main")
	mustGit(t, repo, "checkout", "-b", "spec/b")
	commitFile(t, repo, "main.go", "package main\n\nvar X = \"b\"\n", "b work")

	mustGit(t, repo, "checkout", "main")

	sb := newStubSandbox()
	r := newTestRun(t, repo, "depends_on: a, b\n", Options{}, sb)

	status := r.integrateDeps()
	if status != StatusBlocked {
		t.Fatalf("integrateDeps() = %q, want StatusBlocked", status)
	}
	if sb.verifyN != 0 {
		t.Fatalf("verifyN = %d, want 0 (blocked before any verify)", sb.verifyN)
	}
	if sb.agentN != 0 {
		t.Fatalf("agentN = %d, want 0 (no agent iteration ran)", sb.agentN)
	}

	mergeHead, _ := r.git("rev-parse", "--verify", "--quiet", "MERGE_HEAD")
	if strings.TrimSpace(mergeHead) != "" {
		t.Fatalf("MERGE_HEAD still set, want merge aborted")
	}
	remotes, err := r.git("remote")
	if err != nil {
		t.Fatalf("remote: %v", err)
	}
	if strings.TrimSpace(remotes) != "" {
		t.Fatalf("remotes = %q, want none", remotes)
	}

	q, err := os.ReadFile(r.questionPath())
	if err != nil {
		t.Fatalf("read QUESTION.md: %v", err)
	}
	want := "Merging the dependencies conflicts on main.go. Merge them by hand on a branch and set base:, or narrow their owns:.\n"
	if string(q) != want {
		t.Fatalf("QUESTION.md = %q, want %q", q, want)
	}
	if r.st.BaseResolved != "" {
		t.Fatalf("st.BaseResolved = %s, want empty (integration never completed)", r.st.BaseResolved)
	}
}

// TestIntegratedBaseRedBlocks covers a dependency merge that applies
// cleanly but leaves verify red on the combined base: the clone is reset
// back to the original base, the question explains the block, and
// run.json records nothing (BaseResolved stays empty).
func TestIntegratedBaseRedBlocks(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-b", "spec/dep")
	commitFile(t, repo, "dep.go", "package dep\n", "dep work")
	mustGit(t, repo, "checkout", "main")

	sb := newStubSandbox()
	sb.verify = func(iter int, dir string) error { return errors.New("red") }
	r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)
	baseBefore := r.st.Base

	status := r.integrateDeps()
	if status != StatusBlocked {
		t.Fatalf("integrateDeps() = %q, want StatusBlocked", status)
	}
	if sb.verifyN != 1 {
		t.Fatalf("verifyN = %d, want 1", sb.verifyN)
	}

	head, err := r.git("rev-parse", "HEAD")
	if err != nil {
		t.Fatalf("rev-parse HEAD: %v", err)
	}
	if strings.TrimSpace(head) != baseBefore {
		t.Fatalf("HEAD = %s, want reset back to base %s", head, baseBefore)
	}
	if r.st.Base != baseBefore {
		t.Fatalf("st.Base = %s, want unchanged %s", r.st.Base, baseBefore)
	}
	if r.st.BaseResolved != "" {
		t.Fatalf("st.BaseResolved = %s, want empty (integration never completed)", r.st.BaseResolved)
	}

	logData, err := os.ReadFile(filepath.Join(r.attemptsDir(), "integrate.log"))
	if err != nil {
		t.Fatalf("read integrate.log: %v", err)
	}
	if len(logData) == 0 {
		t.Fatalf("integrate.log is empty")
	}

	q, err := os.ReadFile(r.questionPath())
	if err != nil {
		t.Fatalf("read QUESTION.md: %v", err)
	}
	want := "The dependencies merged cleanly but verify is red on the combined base. Fix the dependency specs first.\n"
	if string(q) != want {
		t.Fatalf("QUESTION.md = %q, want %q", q, want)
	}
}

// TestResumeDepMovedRefuses covers D-h's resume check: once integration has
// completed and a recorded dependency tip later moves (the owner pushed
// more work to spec/dep), setup() must refuse the resume rather than
// silently re-integrating on top of commits the agent may already have
// made — and must leave run.json untouched.
func TestResumeDepMovedRefuses(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-b", "spec/dep")
	commitFile(t, repo, "dep.go", "package dep\n", "dep work v1")
	mustGit(t, repo, "checkout", "main")

	sb := newStubSandbox()
	r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)
	if status := r.integrateDeps(); status != "" {
		t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
	}
	savedBase := r.st.Base
	if r.logFile != nil {
		r.logFile.Close()
	}
	if r.agentLog != nil {
		r.agentLog.Close()
	}

	mustGit(t, repo, "checkout", "spec/dep")
	commitFile(t, repo, "dep2.go", "package dep\n", "dep work v2")
	mustGit(t, repo, "checkout", "main")

	sp, err := resolveSpec(r.opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	if _, err := setup(r.opts, sp); err == nil {
		t.Fatalf("setup() = nil, want refusal error")
	} else {
		if !strings.Contains(err.Error(), "dependency dep changed since this run started") {
			t.Fatalf("err = %v, want dep-moved message", err)
		}
		if !strings.Contains(err.Error(), "re-run with --fresh") {
			t.Fatalf("err = %v, want --fresh hint", err)
		}
	}

	st2 := loadPriorState(r.opts, "demo")
	if st2 == nil {
		t.Fatalf("run.json missing")
	}
	if st2.Base != savedBase {
		t.Fatalf("st.Base = %s, want unchanged %s", st2.Base, savedBase)
	}
}

// TestResumeDepAlreadyInBaseThenMovedRefuses covers scenario (c): a
// dependency that was already an ancestor of the base when the run
// started (so it's tolerated on resume per checkDepTipsResume's inBase
// check) must still refuse once the owner pushes a *new* commit to
// spec/dep after the run started — the new tip is no longer an ancestor
// of the recorded base, so it falls through to today's refusal. Run with
// a nil DepTips (the pre-fix on-disk format) too, to prove the
// ancestor-tolerance added for D-h doesn't accidentally mask a real move.
func TestResumeDepAlreadyInBaseThenMovedRefuses(t *testing.T) {
	for _, nilDepTips := range []bool{false, true} {
		t.Run(fmt.Sprintf("nilDepTips=%v", nilDepTips), func(t *testing.T) {
			repo := newSourceRepo(t)
			mustGit(t, repo, "checkout", "-b", "spec/dep")
			commitFile(t, repo, "dep.go", "package dep\n", "dep work v1")
			depV1 := mustGit(t, repo, "rev-parse", "HEAD")
			mustGit(t, repo, "checkout", "main")
			mustGit(t, repo, "merge", "--no-ff", "-m", "merge dep", "spec/dep")

			sb := newStubSandbox()
			r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)
			if status := r.integrateDeps(); status != "" {
				t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
			}
			savedBase := r.st.Base
			if nilDepTips {
				r.st.DepTips = nil
				if err := r.saveState(); err != nil {
					t.Fatalf("saveState: %v", err)
				}
			}
			if r.logFile != nil {
				r.logFile.Close()
			}
			if r.agentLog != nil {
				r.agentLog.Close()
			}

			mustGit(t, repo, "checkout", "spec/dep")
			commitFile(t, repo, "dep2.go", "package dep\n", "dep work v2")
			depV2 := mustGit(t, repo, "rev-parse", "HEAD")
			mustGit(t, repo, "checkout", "main")

			sp, err := resolveSpec(r.opts)
			if err != nil {
				t.Fatalf("resolveSpec: %v", err)
			}
			sha, err := resolveBase(sp.Repo, sp.Base)
			if err != nil {
				t.Fatalf("resolveBase: %v", err)
			}
			sp.BaseSHA = sha

			wantOld := short(depV1)
			if nilDepTips {
				wantOld = "(none)"
			}
			wantMsg := fmt.Sprintf("dependency dep changed since this run started (%s → %s); re-run with --fresh", wantOld, short(depV2))
			if _, err := setup(r.opts, sp); err == nil {
				t.Fatalf("setup() = nil, want refusal error")
			} else if err.Error() != wantMsg {
				t.Fatalf("err = %q, want %q", err.Error(), wantMsg)
			}

			st2 := loadPriorState(r.opts, "demo")
			if st2 == nil {
				t.Fatalf("run.json missing")
			}
			if st2.Base != savedBase {
				t.Fatalf("st.Base = %s, want unchanged %s", st2.Base, savedBase)
			}
		})
	}
}

// TestResumeDepDeletedAfterRecordedInBase covers the extra guard 1a needs:
// a dependency already an ancestor of the base gets a recorded DepTips
// entry now, so if the owner deletes spec/<dep> *after* that recording,
// resolveDepTips on resume sees sha "" where DepTips has the old sha —
// without tolerating this, that would look like a newly-moved dependency.
// It must still resume, exactly as it did before 1a started recording
// already-in-base tips at all (both sides were "" then).
func TestResumeDepDeletedAfterRecordedInBase(t *testing.T) {
	repo := newSourceRepo(t)
	mustGit(t, repo, "checkout", "-b", "spec/dep")
	commitFile(t, repo, "dep.go", "package dep\n", "dep work")
	mustGit(t, repo, "checkout", "main")
	mustGit(t, repo, "merge", "--no-ff", "-m", "merge dep", "spec/dep")

	sb := newStubSandbox()
	r := newTestRun(t, repo, "depends_on: dep\n", Options{}, sb)
	if status := r.integrateDeps(); status != "" {
		t.Fatalf("integrateDeps() = %q, want \"\" (continue)", status)
	}
	if r.st.DepTips["dep"] == "" {
		t.Fatalf("st.DepTips[dep] should be recorded before the branch is deleted")
	}
	if r.logFile != nil {
		r.logFile.Close()
	}
	if r.agentLog != nil {
		r.agentLog.Close()
	}

	mustGit(t, repo, "branch", "-D", "spec/dep")

	sp, err := resolveSpec(r.opts)
	if err != nil {
		t.Fatalf("resolveSpec: %v", err)
	}
	sha, err := resolveBase(sp.Repo, sp.Base)
	if err != nil {
		t.Fatalf("resolveBase: %v", err)
	}
	sp.BaseSHA = sha

	if _, err := setup(r.opts, sp); err != nil {
		t.Fatalf("setup() = %v, want nil (deleted branch tolerated once already recorded in base)", err)
	}
}
