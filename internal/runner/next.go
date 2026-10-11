package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jwbonnell/nightshift/internal/home"
)

type nextCandidate struct {
	path, slug string
}

// waitSkip records a ready spec --next passed over because of an unmet
// dependency: slug is the waiting spec, dep/state its first unmet
// dependency (per firstUnmet).
type waitSkip struct {
	slug, dep, state string
}

// skipInNext reports whether filename should be skipped by --next: it has
// prior state whose status is blocked or complete, and whose content hash
// still matches (i.e. nothing has changed since that terminal status).
func skipInNext(opts Options, filename string, data []byte) bool {
	st := loadPriorState(opts, slugFromFilename(filename))
	if st == nil {
		return false
	}
	if st.Status != "blocked" && st.Status != "complete" {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == st.SpecHash
}

// selectNextCandidates lists every eligible spec in <Home>/specs/ready/,
// oldest created: first (empty created: sorts last), then by filename. A
// spec with an unmet dependency is excluded from the candidates and
// reported in waits instead (in the same oldest-first order), unless its
// depends_on doesn't parse — that one stays a candidate, so it fails later
// with the usual frontmatter error. Matching is case-insensitive (D11.6);
// a missing ready dir is not an error.
func selectNextCandidates(opts Options) (cands []nextCandidate, waits []waitSkip, err error) {
	readyDir := home.ReadyDir(opts.Home)
	entries, err := os.ReadDir(readyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	type scored struct {
		cand    nextCandidate
		wait    *waitSkip
		created string
		name    string
	}
	var all []scored
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(readyDir, name))
		if err != nil {
			continue
		}
		if skipInNext(opts, name, data) {
			continue
		}
		fm := home.ParseFrontmatter(data)
		slug := slugFromFilename(name)
		s := scored{
			cand:    nextCandidate{path: filepath.Join(readyDir, name), slug: slug},
			created: home.Unquote(strings.TrimSpace(fm["created"])),
			name:    name,
		}
		if deps, derr := parseDependsOn(fm["depends_on"]); derr == nil && len(deps) > 0 {
			if d, state, ok := firstUnmet(opts, deps); ok {
				s.wait = &waitSkip{slug: slug, dep: d, state: state}
			}
		}
		all = append(all, s)
	}

	sort.Slice(all, func(i, j int) bool {
		ci, cj := all[i].created, all[j].created
		if ci != cj {
			if ci == "" {
				return false
			}
			if cj == "" {
				return true
			}
			return ci < cj
		}
		return all[i].name < all[j].name
	})

	for _, a := range all {
		if a.wait != nil {
			waits = append(waits, *a.wait)
			continue
		}
		cands = append(cands, a.cand)
	}
	return cands, waits, nil
}

// selectNext returns the single oldest-eligible candidate's path (or ""
// if none), ignoring locks — used by the dry-run --next path. waits is
// the same list selectNextCandidates returns, for printing "skip" lines.
func selectNext(opts Options) (path string, waits []waitSkip, err error) {
	cands, waits, err := selectNextCandidates(opts)
	if err != nil {
		return "", waits, err
	}
	if len(cands) == 0 {
		return "", waits, nil
	}
	return cands[0].path, waits, nil
}
