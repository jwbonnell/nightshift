package runner

import (
	"fmt"
	"strings"
)

// isScaffoldPath reports whether path is one of the agent's scratch files,
// always exempt from an owns check — the same four names excluded from the
// synced branch via .git/info/exclude (state.go's cloneRepo) and checked
// elsewhere by nonScaffoldDirty and checkScaffold.
func isScaffoldPath(path string) bool {
	switch path {
	case "PROGRESS.md", "QUESTION.md", ".spec-complete", "REVIEW.md":
		return true
	}
	return false
}

// ownsAllows reports whether path is covered by owns: an exact match for a
// file entry, or a prefix match for a directory entry (one ending in "/").
func ownsAllows(owns []string, path string) bool {
	for _, e := range owns {
		if strings.HasSuffix(e, "/") {
			if strings.HasPrefix(path, e) {
				return true
			}
			continue
		}
		if path == e {
			return true
		}
	}
	return false
}

// porcelainPaths extracts the paths touched by a `git status --porcelain
// --untracked-files=all` listing, including both sides of a rename
// ("R  old -> new").
func porcelainPaths(status string) []string {
	var paths []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		rest := line[3:]
		if i := strings.Index(rest, " -> "); i >= 0 {
			paths = append(paths, rest[:i], rest[i+len(" -> "):])
			continue
		}
		paths = append(paths, rest)
	}
	return paths
}

// formatPathList joins paths for a log line, truncating to max entries
// ("a, b, c, +2 more").
func formatPathList(paths []string, max int) string {
	if len(paths) <= max {
		return strings.Join(paths, ", ")
	}
	return strings.Join(paths[:max], ", ") + fmt.Sprintf(", +%d more", len(paths)-max)
}

// ownsViolations returns the paths outside sp.Owns changed by the current
// iteration: committed changes since last_green, plus anything from
// status (a `git status --porcelain --untracked-files=all` listing taken
// before captureDirty wiped the tree, so an uncommitted-only violation is
// still caught). Scaffold files are always exempt. Returns nil when
// nothing is out of bounds.
func (r *run) ownsViolations(sp *Spec, status string) ([]string, error) {
	diffOut, err := r.git("diff", "--name-only", r.st.LastGreen+"..HEAD")
	if err != nil {
		return nil, fmt.Errorf("git diff --name-only %s..HEAD: %w", short(r.st.LastGreen), err)
	}

	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || isScaffoldPath(p) || seen[p] || ownsAllows(sp.Owns, p) {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range strings.Split(diffOut, "\n") {
		add(p)
	}
	for _, p := range porcelainPaths(status) {
		add(p)
	}
	return out, nil
}
