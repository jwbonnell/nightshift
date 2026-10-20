package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// conflictPaths returns the conflicted paths from a merge currently in
// progress in the clone, sorted, deduplicated, and capped at 10 — the
// question text's "<paths>" (D-g).
func (r *run) conflictPaths() []string {
	out, _ := r.git("diff", "--name-only", "--diff-filter=U")
	seen := map[string]bool{}
	var paths []string
	for _, p := range strings.Split(out, "\n") {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	sort.Strings(paths)
	if len(paths) > 10 {
		paths = paths[:10]
	}
	return paths
}

// integrateDeps brings each depends_on tip into the clone's base, before
// any agent iteration runs: a fast-forward when the tip already descends
// from the base, a --no-ff merge otherwise, then one verify of the result
// if anything was merged. A tip that's already an ancestor of the base is
// recorded in st.DepTips too (only a missing spec/<dep> stays unrecorded),
// but whether a verify runs still depends only on whether anything was
// actually merged or fast-forwarded in. It returns "" to let runSpec
// continue into loop(), or a terminal Status (StatusBlocked on a conflict
// or a red combined base, StatusInterrupted, or whatever r.fail records).
func (r *run) integrateDeps() Status {
	sp := r.spec
	st := r.st
	clock := r.opts.clockFn()
	base := st.Base

	tips := map[string]string{}
	merged := 0
	for _, t := range sp.depTips {
		if t.sha == "" {
			r.logf("dep %s: spec/%s missing, skipped", t.slug, t.slug)
			continue
		}
		if _, err := r.git("merge-base", "--is-ancestor", t.sha, "HEAD"); err == nil {
			tips[t.slug] = t.sha
			continue // already in the base
		}
		if _, err := r.git("merge-base", "--is-ancestor", "HEAD", t.sha); err == nil {
			if out, err := r.git("merge", "--ff-only", t.sha); err != nil {
				return r.fail(fmt.Errorf("fast-forward to dep %s tip %s: %v: %s", t.slug, short(t.sha), err, out))
			}
			tips[t.slug] = t.sha
			merged++
			continue
		}

		msg := fmt.Sprintf("nightshift: merge spec/%s for %s", t.slug, sp.Slug)
		if _, err := r.git("merge", "--no-ff", "-m", msg, t.sha); err != nil {
			paths := r.conflictPaths()
			_, _ = r.git("merge", "--abort")
			r.logf("dep %s: merge conflict on %s", t.slug, strings.Join(paths, ", "))
			r.scaffoldWrite(r.questionPath(), fmt.Sprintf(
				"Merging the dependencies conflicts on %s. Merge them by hand on a branch and set base:, or narrow their owns:.\n",
				strings.Join(paths, ", ")))
			return StatusBlocked
		}
		tips[t.slug] = t.sha
		merged++
	}

	if merged > 0 {
		if err := r.sb.ResetVerify(); err != nil {
			r.logf("integrate: reset verify sandbox: %v", err)
		}
		ctx, cancel := r.iterCtx(time.Time{}, clock)
		vout, verr := r.sb.RunVerify(ctx, sp.Verify)
		cancel()

		if r.interrupted() {
			if err := r.resetTo(base); err != nil {
				return r.fail(err)
			}
			return StatusInterrupted
		}
		if verr != nil {
			_ = os.WriteFile(filepath.Join(r.attemptsDir(), "integrate.log"), []byte(tailLines(vout, 200)), 0o644)
			if err := r.resetTo(base); err != nil {
				return r.fail(err)
			}
			r.logf("integrate: verify red on combined base")
			r.scaffoldWrite(r.questionPath(), "The dependencies merged cleanly but verify is red on the combined base. Fix the dependency specs first.\n")
			return StatusBlocked
		}

		head, err := r.headSHA()
		if err != nil {
			return r.fail(err)
		}
		st.Base = head
		st.LastGreen = head
	}

	st.BaseResolved = base
	if len(tips) == 0 {
		st.DepTips = nil
	} else {
		st.DepTips = tips
	}
	if err := r.saveState(); err != nil {
		return r.fail(err)
	}
	return ""
}
