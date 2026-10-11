package runner

import (
	"fmt"
	"strings"

	"github.com/jwbonnell/nightshift/internal/gitx"
)

// checkLine is one line of a post-run checks report, in lint's
// ok/warn/FAIL/skip format (see lintLine).
type checkLine struct {
	label string // "ok", "warn", "FAIL", "skip"
	text  string
}

func (c checkLine) String() string {
	return fmt.Sprintf("%-6s%s", c.label, c.text)
}

// postRunReport is the result of running every Task 5 check once, cached
// on the run so printSummary, resultBody and notify all see the same tally
// without re-running the checks (several of which shell out to git).
type postRunReport struct {
	lines        []checkLine
	fails, warns int
}

// postRunChecks runs the six post-run checks in table order. It never
// changes the run's status — only the notification and the result note
// reflect a FAIL. It runs entirely against the host filesystem and the
// clone/source repo directly, never through r.sb: by the time finish()
// calls this, the sandboxes have already been torn down (see runSpec).
func (r *run) postRunChecks() postRunReport {
	lines := []checkLine{
		r.checkSync(),
		r.checkScaffold(),
		r.checkCloneRemotes(),
		countHardlinks(r.clone),
		r.checkSandboxes(),
		r.checkVersion(),
	}
	var rep postRunReport
	rep.lines = lines
	for _, c := range lines {
		switch c.label {
		case "FAIL":
			rep.fails++
		case "warn":
			rep.warns++
		}
	}
	return rep
}

// checkSync verifies the source repo's sync ref (refs/heads/<branch>, or
// the pending ref when sync is pending) resolves to last_green.
func (r *run) checkSync() checkLine {
	st := r.st
	if st.SyncOwed {
		return checkLine{"skip", "sync (sync failed — see above)"}
	}

	ref := "refs/heads/" + st.Branch
	if r.syncPending() {
		ref = r.pendingSyncRef()
	}

	out, err := gitx.Run(r.spec.Repo, "rev-parse", "--verify", ref)
	if err != nil {
		if st.LastGreen == st.Base {
			return checkLine{"ok", "sync (nothing to sync)"}
		}
		return checkLine{"FAIL", fmt.Sprintf("sync: %s is missing, expected %s", ref, short(st.LastGreen))}
	}
	sha := strings.TrimSpace(out)
	if sha == st.LastGreen {
		return checkLine{"ok", "sync"}
	}
	return checkLine{"FAIL", fmt.Sprintf("sync: %s is at %s, expected %s", ref, short(sha), short(st.LastGreen))}
}

// checkScaffold verifies no commit in base..last_green, in the clone,
// touches PROGRESS.md, QUESTION.md, .spec-complete or REVIEW.md — those are
// the agent's scratch files, excluded from the branch via .git/info/exclude,
// and should never appear in the history the spec's work lands as.
func (r *run) checkScaffold() checkLine {
	st := r.st
	out, err := gitx.Run(r.clone, "log", "--name-only", "--pretty=format:%H", st.Base+".."+st.LastGreen)
	if err != nil {
		return checkLine{"FAIL", "scaffold: git log: " + err.Error()}
	}

	type hit struct {
		sha   string
		files []string
	}
	var hits []hit
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case fullSHA.MatchString(line):
			hits = append(hits, hit{sha: line})
		case line == "PROGRESS.md", line == "QUESTION.md", line == ".spec-complete", line == "REVIEW.md":
			if len(hits) > 0 {
				hits[len(hits)-1].files = append(hits[len(hits)-1].files, line)
			}
		}
	}

	var bad []hit
	for _, h := range hits {
		if len(h.files) > 0 {
			bad = append(bad, h)
		}
	}
	if len(bad) == 0 {
		return checkLine{"ok", "scaffold"}
	}
	if len(bad) > 5 {
		bad = bad[:5]
	}
	parts := make([]string, len(bad))
	for i, h := range bad {
		parts[i] = fmt.Sprintf("%s (%s)", short(h.sha), strings.Join(h.files, ", "))
	}
	return checkLine{"FAIL", "scaffold: " + strings.Join(parts, ", ")}
}

// checkCloneRemotes verifies the clone has no remotes — it is reached only
// by the runner's own syncBranchBack, never by the agent pushing or
// pulling directly (see CLAUDE.md's trust model).
func (r *run) checkCloneRemotes() checkLine {
	out, err := gitx.Run(r.clone, "remote")
	if err != nil {
		return checkLine{"FAIL", "clone-remotes: git remote: " + err.Error()}
	}
	remotes := strings.Fields(out)
	if len(remotes) == 0 {
		return checkLine{"ok", "clone-remotes"}
	}
	return checkLine{"FAIL", "clone-remotes: " + strings.Join(remotes, ", ")}
}

// checkSandboxes reports the explicit, pre-finish sb.Remove() call's own
// result (runSpec stores it in r.removeErr before calling finish).
func (r *run) checkSandboxes() checkLine {
	if r.removeErr != nil {
		return checkLine{"FAIL", "sandboxes: " + r.removeErr.Error()}
	}
	return checkLine{"ok", "sandboxes"}
}

// checkVersion warns on a "dev" or "-dirty" build — both mean this run was
// not driven by a clean, tagged binary.
func (r *run) checkVersion() checkLine {
	text := "nightshift " + r.version
	if r.version == "dev" || strings.HasSuffix(r.version, "-dirty") {
		return checkLine{"warn", text}
	}
	return checkLine{"ok", text}
}
