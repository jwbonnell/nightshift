package runner

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/lock"
)

// latestNote returns the path of the newest note in dir matching slug
// exactly — a result note (<slug>-YYYY-MM-DD[-N].md) or, when review is
// true, a review note (<slug>-YYYY-MM-DD-review[-N].md) — or "" if none
// match. Exact matching keeps a longer slug that shares this one as a
// prefix (e.g. "demo-extra") from matching, and keeps result and review
// notes from matching each other.
func latestNote(dir, slug string, review bool) string {
	suffix := `-\d{4}-\d{2}-\d{2}(-\d+)?\.md$`
	if review {
		suffix = `-\d{4}-\d{2}-\d{2}-review(-\d+)?\.md$`
	}
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(slug) + suffix)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}

	var newest string
	var newestMod int64
	for _, e := range entries {
		if e.IsDir() || !pattern.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		if mod := fi.ModTime().UnixNano(); newest == "" || mod > newestMod {
			newest, newestMod = path, mod
		}
	}
	return newest
}

// reviewRefFor is reviewRef's logic without a live *run, for the status
// command (which only ever has run.json on disk).
func reviewRefFor(st *runState, slug string) string {
	pending := "refs/nightshift/" + slug
	if st.SyncRef == pending {
		return pending
	}
	return st.Branch
}

// specDepsOwns looks up slug's spec file across specs/draft/, specs/ready/
// and specs/done/ (in that order) and returns its parsed depends_on and
// owns frontmatter, or nil, nil if the spec file is gone or either key
// doesn't parse.
func specDepsOwns(homeDir, slug string) (dependsOn, owns []string) {
	for _, dir := range []string{home.DraftDir(homeDir), home.ReadyDir(homeDir), home.DoneDir(homeDir)} {
		p, ok := findCaseInsensitive(dir, slug+".md")
		if !ok {
			continue
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		fm := home.ParseFrontmatter(raw)
		dependsOn, _ = parseDependsOn(fm["depends_on"])
		owns, _ = parseOwns(fm["owns"])
		return dependsOn, owns
	}
	return nil, nil
}

// formatDepTips renders run.json's dep_tips map as "slug=sha, slug=sha",
// sorted by slug for a deterministic order.
func formatDepTips(tips map[string]string) string {
	keys := make([]string, 0, len(tips))
	for k := range tips {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + tips[k]
	}
	return strings.Join(parts, ", ")
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
			if waitingUntil, err := time.Parse(time.RFC3339, st.WaitingUntil); err == nil {
				status = fmt.Sprintf("waiting (until %s)", waitingUntil.Local().Format("15:04"))
			}
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
	opts := Options{Home: homeDir, StateRoot: stateRoot}
	type readyRow struct{ slug, display string }
	var readyRows []readyRow
	for _, e := range readyEntries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		slug := slugFromFilename(e.Name())
		if _, ok := rows[slug]; ok {
			continue
		}
		display := slug
		if data, err := os.ReadFile(filepath.Join(readyDir, e.Name())); err == nil {
			fm := home.ParseFrontmatter(data)
			if deps, derr := parseDependsOn(fm["depends_on"]); derr == nil && len(deps) > 0 {
				if d, _, ok := firstUnmet(opts, deps); ok {
					display = fmt.Sprintf("%s (waits on %s)", slug, d)
				}
			}
		}
		readyRows = append(readyRows, readyRow{slug: slug, display: display})
	}
	sort.Slice(readyRows, func(i, j int) bool { return readyRows[i].slug < readyRows[j].slug })
	readyNotStarted := make([]string, len(readyRows))
	for i, r := range readyRows {
		readyNotStarted[i] = r.display
	}

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

	if len(rows) > 0 {
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
	}

	if active := readActiveLimit(stateRoot, time.Now(), false); active != nil {
		resetAt, _ := time.Parse(time.RFC3339, active.ResetAt)
		fmt.Fprintf(w, "usage limit: until %s\n", resetAt.Local().Format("Mon 15:04"))
	}

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
	dependsOn, owns := specDepsOwns(homeDir, slug)
	if len(dependsOn) > 0 {
		fmt.Fprintf(w, "depends_on: %s\n", strings.Join(dependsOn, ", "))
	}
	if len(owns) > 0 {
		fmt.Fprintf(w, "owns: %s\n", strings.Join(owns, ", "))
	}
	fmt.Fprintf(w, "last_green: %s\n", st.LastGreen)
	if len(st.DepTips) > 0 {
		fmt.Fprintf(w, "dep_tips: %s\n", formatDepTips(st.DepTips))
	}
	if st.BaseResolved != "" {
		fmt.Fprintf(w, "base_resolved: %s\n", st.BaseResolved)
	}
	fmt.Fprintf(w, "sync_ref: %s\n", st.SyncRef)
	fmt.Fprintf(w, "sync_owed: %v\n", st.SyncOwed)
	fmt.Fprintf(w, "spec_hash: %s\n", st.SpecHash)
	fmt.Fprintf(w, "planned: %v\n", st.Planned)
	fmt.Fprintf(w, "iterations: %d\n", st.Iterations)
	fmt.Fprintf(w, "commits: %d\n", st.Commits)
	fmt.Fprintf(w, "status: %s\n", st.Status)
	if st.WaitingUntil != "" {
		fmt.Fprintf(w, "waiting_until: %s\n", st.WaitingUntil)
	}
	fmt.Fprintf(w, "started: %s\n", st.Started)
	fmt.Fprintf(w, "history: %v\n", st.History)

	dir := filepath.Join(stateRoot, slug)
	fmt.Fprintf(w, "state dir: %s\n", dir)
	fmt.Fprintf(w, "clone: %s\n", filepath.Join(dir, "repo"))
	fmt.Fprintf(w, "runner log: %s\n", filepath.Join(dir, "runner.log"))
	fmt.Fprintf(w, "agent log: %s\n", filepath.Join(dir, "agent.log"))

	resultsDir := home.ResultsDir(homeDir)

	latest := "none"
	if newest := latestNote(resultsDir, slug, false); newest != "" {
		latest = newest
	}
	fmt.Fprintf(w, "latest result: %s\n", latest)

	latestReview := "none"
	if newest := latestNote(resultsDir, slug, true); newest != "" {
		verdict := "unknown"
		if data, err := os.ReadFile(newest); err == nil {
			fm := home.ParseFrontmatter(data)
			if v := home.Unquote(strings.TrimSpace(fm["verdict"])); v != "" {
				verdict = v
			}
		}
		latestReview = fmt.Sprintf("%s — %s", verdict, newest)
	}
	fmt.Fprintf(w, "latest review: %s\n", latestReview)

	fmt.Fprintf(w, "review: git -C %s log --oneline %s..%s\n", st.Repo, short(st.Base), reviewRefFor(&st, slug))
	return nil
}

// CompleteReadySlugs returns, in slug order, the ready specs whose run.json
// status is "complete" and whose spec lock is not held — the set
// `nightshift done --complete` marks done.
func CompleteReadySlugs(homeDir, stateRoot string) []string {
	entries, err := os.ReadDir(home.ReadyDir(homeDir))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			continue
		}
		slug := slugFromFilename(e.Name())
		data, err := os.ReadFile(filepath.Join(stateRoot, slug, "run.json"))
		if err != nil {
			continue
		}
		var st struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(data, &st) != nil || st.Status != "complete" {
			continue
		}
		if _, held := lock.Probe(specLockPath(stateRoot, slug)); held {
			continue
		}
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}
