package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

// reviewRefFor is reviewRef's logic without a live *run, for the status
// command (which only ever has run.json on disk).
func reviewRefFor(st *runState, slug string) string {
	pending := "refs/nightshift/" + slug
	if st.SyncRef == pending {
		return pending
	}
	return st.Branch
}

// PrintStatusTable prints the no-argument `status` table plus the
// ready-not-started/drafts trailer lines, or the "no runs yet" line if
// there is nothing at all to show.
func PrintStatusTable(w io.Writer, homeDir, stateRoot string) error {
	matches, err := filepath.Glob(filepath.Join(stateRoot, "*", "run.json"))
	if err != nil {
		return err
	}

	type row struct {
		slug, status, updated, branch string
		iterations, commits           int
	}
	rows := map[string]row{}
	for _, m := range matches {
		slug := filepath.Base(filepath.Dir(m))
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var st runState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}

		status := st.Status
		if info, held := lock.Probe(specLockPath(stateRoot, slug)); held {
			status = fmt.Sprintf("running (pid %d)", info.PID)
		} else if status == "" {
			status = "new"
		}

		updated := st.Started
		if len(st.History) > 0 {
			last := st.History[len(st.History)-1]
			if len(last) >= 16 {
				updated = last[:16]
			} else {
				updated = last
			}
		}

		rows[slug] = row{slug: slug, status: status, updated: updated, branch: st.Branch, iterations: st.Iterations, commits: st.Commits}
	}

	readyDir := home.ReadyDir(homeDir)
	readyEntries, _ := os.ReadDir(readyDir)
	var readyNotStarted []string
	for _, e := range readyEntries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		slug := slugFromFilename(e.Name())
		if _, ok := rows[slug]; !ok {
			readyNotStarted = append(readyNotStarted, slug)
		}
	}
	sort.Strings(readyNotStarted)

	draftDir := home.DraftDir(homeDir)
	draftEntries, _ := os.ReadDir(draftDir)
	var drafts []string
	for _, e := range draftEntries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		drafts = append(drafts, slugFromFilename(e.Name()))
	}
	sort.Strings(drafts)

	if len(rows) == 0 && len(readyNotStarted) == 0 && len(drafts) == 0 {
		fmt.Fprint(w, "no runs yet — nightshift new <slug> to start a spec\n")
		return nil
	}

	slugs := make([]string, 0, len(rows))
	for slug := range rows {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprint(tw, "SLUG\tSTATUS\tITER\tCOMMITS\tUPDATED\tBRANCH\n")
	for _, slug := range slugs {
		r := rows[slug]
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%s\t%s\n", r.slug, r.status, r.iterations, r.commits, r.updated, r.branch)
	}
	_ = tw.Flush()

	if len(readyNotStarted) > 0 {
		fmt.Fprintf(w, "ready, not started: %s\n", strings.Join(readyNotStarted, ", "))
	}
	if len(drafts) > 0 {
		fmt.Fprintf(w, "drafts: %s\n", strings.Join(drafts, ", "))
	}
	return nil
}

// PrintStatusDetail prints `status <slug>`'s single-run detail view.
func PrintStatusDetail(w io.Writer, homeDir, stateRoot, slug string) error {
	data, err := os.ReadFile(filepath.Join(stateRoot, slug, "run.json"))
	if err != nil {
		return fmt.Errorf("no run state for %s", slug)
	}
	var st runState
	if err := json.Unmarshal(data, &st); err != nil {
		return fmt.Errorf("no run state for %s", slug)
	}

	fmt.Fprintf(w, "slug: %s\n", st.Slug)
	fmt.Fprintf(w, "repo: %s\n", st.Repo)
	fmt.Fprintf(w, "branch: %s\n", st.Branch)
	fmt.Fprintf(w, "base: %s\n", st.Base)
	fmt.Fprintf(w, "base_ref: %s\n", st.BaseRef)
	fmt.Fprintf(w, "last_green: %s\n", st.LastGreen)
	fmt.Fprintf(w, "sync_ref: %s\n", st.SyncRef)
	fmt.Fprintf(w, "sync_owed: %v\n", st.SyncOwed)
	fmt.Fprintf(w, "spec_hash: %s\n", st.SpecHash)
	fmt.Fprintf(w, "planned: %v\n", st.Planned)
	fmt.Fprintf(w, "iterations: %d\n", st.Iterations)
	fmt.Fprintf(w, "commits: %d\n", st.Commits)
	fmt.Fprintf(w, "status: %s\n", st.Status)
	fmt.Fprintf(w, "started: %s\n", st.Started)
	fmt.Fprintf(w, "history: %v\n", st.History)

	dir := filepath.Join(stateRoot, slug)
	fmt.Fprintf(w, "state dir: %s\n", dir)
	fmt.Fprintf(w, "clone: %s\n", filepath.Join(dir, "repo"))
	fmt.Fprintf(w, "runner log: %s\n", filepath.Join(dir, "runner.log"))
	fmt.Fprintf(w, "agent log: %s\n", filepath.Join(dir, "agent.log"))

	latest := "none"
	if matches, err := filepath.Glob(filepath.Join(home.ResultsDir(homeDir), slug+"-*.md")); err == nil && len(matches) > 0 {
		var newest string
		var newestMod int64
		for _, m := range matches {
			fi, err := os.Stat(m)
			if err != nil {
				continue
			}
			if mod := fi.ModTime().UnixNano(); newest == "" || mod > newestMod {
				newest, newestMod = m, mod
			}
		}
		if newest != "" {
			latest = newest
		}
	}
	fmt.Fprintf(w, "latest result: %s\n", latest)

	fmt.Fprintf(w, "review: git -C %s log --oneline %s..%s\n", st.Repo, short(st.Base), reviewRefFor(&st, slug))
	return nil
}
