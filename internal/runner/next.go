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
// oldest created: first (empty created: sorts last), then by filename.
// Matching is case-insensitive (D11.6); a missing ready dir is not an
// error.
func selectNextCandidates(opts Options) ([]nextCandidate, error) {
	readyDir := home.ReadyDir(opts.Home)
	entries, err := os.ReadDir(readyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	type scored struct {
		cand    nextCandidate
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
		all = append(all, scored{
			cand:    nextCandidate{path: filepath.Join(readyDir, name), slug: slugFromFilename(name)},
			created: home.Unquote(strings.TrimSpace(fm["created"])),
			name:    name,
		})
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

	cands := make([]nextCandidate, len(all))
	for i, a := range all {
		cands[i] = a.cand
	}
	return cands, nil
}

// selectNext returns the single oldest-eligible candidate's path (or ""
// if none), ignoring locks — used by the dry-run --next path.
func selectNext(opts Options) (string, error) {
	cands, err := selectNextCandidates(opts)
	if err != nil {
		return "", err
	}
	if len(cands) == 0 {
		return "", nil
	}
	return cands[0].path, nil
}
