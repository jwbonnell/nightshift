package runner

import (
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
