package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jwbonnell/nightshift/internal/home"
)

// specGraph is the dependency graph built from every spec's frontmatter
// across specs/draft/, specs/ready/ and specs/done/: which slugs exist
// (and in which of the three they live), and which of them declare
// depends_on. A spec whose depends_on doesn't parse contributes no edges
// but still registers its slug, so it never falsely becomes an "unknown
// dependency" for someone else — see loadSpecGraph.
type specGraph struct {
	state map[string]string   // slug -> "draft"/"ready"/"done"
	edges map[string][]string // slug -> depends_on, frontmatter order
}

// loadSpecGraph scans specs/draft/, specs/ready/ and specs/done/ under
// homeDir. A spec that can't be read, or whose depends_on doesn't parse,
// is skipped as an edge source and doesn't hide the others — its slug is
// still recorded as known.
func loadSpecGraph(homeDir string) *specGraph {
	g := &specGraph{state: map[string]string{}, edges: map[string][]string{}}
	dirs := []struct{ dir, state string }{
		{home.DraftDir(homeDir), "draft"},
		{home.ReadyDir(homeDir), "ready"},
		{home.DoneDir(homeDir), "done"},
	}
	for _, d := range dirs {
		entries, err := os.ReadDir(d.dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
				continue
			}
			slug := slugFromFilename(e.Name())
			g.state[slug] = d.state
			raw, err := os.ReadFile(filepath.Join(d.dir, e.Name()))
			if err != nil {
				continue
			}
			fm := home.ParseFrontmatter(raw)
			deps, err := parseDependsOn(fm["depends_on"])
			if err != nil || len(deps) == 0 {
				continue
			}
			g.edges[slug] = deps
		}
	}
	return g
}

// findCycle breadth-first searches edges starting at start, returning the
// shortest path start -> ... -> start (both ends included), or nil if
// start can't reach itself.
func findCycle(edges map[string][]string, start string) []string {
	type node struct {
		slug string
		path []string
	}
	visited := map[string]bool{start: true}
	queue := []node{{start, []string{start}}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range edges[cur.slug] {
			path := append(append([]string{}, cur.path...), next)
			if next == start {
				return path
			}
			if visited[next] {
				continue
			}
			visited[next] = true
			queue = append(queue, node{next, path})
		}
	}
	return nil
}

// depState reports d's status for satisfaction and display: "complete"
// when specs/done/d.md exists or <StateRoot>/d/run.json has status
// complete, "not started" when there's no run.json, or the recorded
// run.json status otherwise (e.g. "blocked", "red", "rate-limited").
// opts.Home and opts.StateRoot must both be set.
func depState(opts Options, d string) string {
	if _, ok := findCaseInsensitive(home.DoneDir(opts.Home), d+".md"); ok {
		return "complete"
	}
	st := loadPriorState(opts, d)
	if st == nil || st.Status == "" {
		return "not started"
	}
	return st.Status
}

// firstUnmet returns the first dependency in deps (in depends_on order)
// whose depState isn't "complete", along with that state. ok is false when
// every dependency is satisfied (or there are none).
func firstUnmet(opts Options, deps []string) (d, state string, ok bool) {
	for _, dep := range deps {
		if s := depState(opts, dep); s != "complete" {
			return dep, s, true
		}
	}
	return "", "", false
}

// checkDeps is the graph + satisfaction check Run()/runNext() run on a
// resolved spec before anything is created: an unknown dependency or a
// cycle is always an error; an unsatisfied dependency is an error unless
// opts.IgnoreDeps.
func checkDeps(opts Options, sp *Spec) error {
	if len(sp.DependsOn) == 0 {
		return nil
	}
	g := loadSpecGraph(opts.Home)
	if fails, _ := depsCheck(opts, g, sp.Slug, sp.DependsOn); len(fails) > 0 {
		return errors.New(fails[0])
	}
	if opts.IgnoreDeps {
		return nil
	}
	if d, state, ok := firstUnmet(opts, sp.DependsOn); ok {
		return fmt.Errorf("%s waits on %s (%s) — run it first, or pass --ignore-deps", sp.Slug, d, state)
	}
	return nil
}

// checkDepTipsResume is setup()'s resume-time counterpart to the
// fresh-clone integration path. If a previous invocation never finished
// integrating depends_on (interrupted or blocked before BaseResolved was
// recorded, so no agent iteration ran either), it sets r.needIntegrate so
// runSpec retries integrateDeps with freshly resolved tips. Otherwise it
// refuses if a recorded tip has moved, or a newly added dependency now
// resolves to one — the agent's own commits may already sit on top of the
// old integrated base and can't be rebased mechanically.
func checkDepTipsResume(r *run, sp *Spec) error {
	if len(sp.DependsOn) == 0 || r.opts.IgnoreDeps {
		return nil
	}
	if r.st.BaseResolved == "" && r.st.Iterations == 0 {
		r.needIntegrate = true
		return nil
	}
	for _, t := range sp.depTips {
		old, existed := r.st.DepTips[t.slug]
		if old == t.sha {
			continue
		}
		oldDisp, newDisp := "(none)", "(none)"
		if existed {
			oldDisp = short(old)
		}
		if t.sha != "" {
			newDisp = short(t.sha)
		}
		return fmt.Errorf("dependency %s changed since this run started (%s → %s); re-run with --fresh", t.slug, oldDisp, newDisp)
	}
	return nil
}

// depsCheck runs the lint depends check for one spec: an unknown
// dependency or a cycle through slug produces FAIL text (one line each,
// unknown deps first, then at most one cycle line); otherwise an unmet
// dependency produces a warn line, in depends_on order.
func depsCheck(opts Options, g *specGraph, slug string, deps []string) (fails, warns []string) {
	for _, d := range deps {
		if _, ok := g.state[d]; !ok {
			fails = append(fails, fmt.Sprintf("%s depends on %s, which isn't in specs/draft, specs/ready or specs/done", slug, d))
		}
	}

	edges := make(map[string][]string, len(g.edges)+1)
	for k, v := range g.edges {
		edges[k] = v
	}
	edges[slug] = deps
	if cycle := findCycle(edges, slug); cycle != nil {
		fails = append(fails, "dependency cycle: "+strings.Join(cycle, " -> "))
	}
	if len(fails) > 0 {
		return fails, nil
	}

	for _, d := range deps {
		if s := depState(opts, d); s != "complete" {
			warns = append(warns, fmt.Sprintf("waits on %s (%s)", d, s))
		}
	}
	return nil, warns
}
