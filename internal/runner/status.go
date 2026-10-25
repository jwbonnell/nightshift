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

// StatusTableOptions tunes PrintStatusTable. The zero value is the default
// view with the real clock.
type StatusTableOptions struct {
	Now       time.Time     // zero means time.Now()
	All       bool          // show every row, however old
	Since     time.Duration // done-row age window; zero means 72h
	SinceText string        // the --since text as given, for the footer; empty means "3d"
	Repo      string        // keep only rows whose REPO equals this
}

var sinceDaysRE = regexp.MustCompile(`^(\d+)d$`)

// ParseStatusSince parses a --since value: "<N>d" (whole days) or any
// time.ParseDuration value.
// Only positive values are accepted; the day-count bound keeps n*24h from
// overflowing time.Duration.
func ParseStatusSince(s string) (time.Duration, error) {
	if m := sinceDaysRE.FindStringSubmatch(s); m != nil {
		var n int
		if _, err := fmt.Sscan(m[1], &n); err == nil && n > 0 && n < 100000 {
			return time.Duration(n) * 24 * time.Hour, nil
		}
	} else if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d, nil
	}
	return 0, fmt.Errorf("--since %q: want a duration like 3d or 36h", s)
}

// Row groups, in display order.
const (
	groupActive = iota
	groupAttention
	groupComplete
	groupReady
	groupDraft
	groupDone
)

const statusTimeLayout = "2006-01-02 15:04"

// displayStatus applies the run-backed rules of the display status: done,
// then running/waiting (lock held), then the run's own status ("new" when
// empty). st may be nil (no run.json); the second result is false when no
// rule fired and the caller must fall back to ready/draft.
func displayStatus(stateRoot, slug string, st *runState, inDone bool) (string, bool) {
	if inDone {
		return "done", true
	}
	if st == nil {
		return "", false
	}
	if info, held := lock.Probe(specLockPath(stateRoot, slug)); held {
		status := fmt.Sprintf("running (pid %d)", info.PID)
		if waitingUntil, err := time.Parse(time.RFC3339, st.WaitingUntil); err == nil {
			status = fmt.Sprintf("waiting (until %s)", waitingUntil.Local().Format("15:04"))
		}
		return status, true
	}
	if st.Status == "" {
		return "new", true
	}
	return st.Status, true
}

// lastHistoryTime parses the newest history entry's "2006-01-02 15:04"
// prefix as local time.
func lastHistoryTime(st *runState) (time.Time, bool) {
	if st == nil || len(st.History) == 0 {
		return time.Time{}, false
	}
	last := st.History[len(st.History)-1]
	if len(last) < len(statusTimeLayout) {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation(statusTimeLayout, last[:len(statusTimeLayout)], time.Local)
	return t, err == nil
}

// noteVerdict reads a review note's verdict, "unknown" when it has none.
func noteVerdict(path string) string {
	if data, err := os.ReadFile(path); err == nil {
		fm := home.ParseFrontmatter(data)
		if v := home.Unquote(strings.TrimSpace(fm["verdict"])); v != "" {
			return v
		}
	}
	return "unknown"
}

var reviewNoteRE = regexp.MustCompile(`^(.+)-\d{4}-\d{2}-\d{2}-review(-\d+)?\.md$`)

// latestReviewNotes reads dir once and maps each slug to its newest review
// note, with the same matching and tie rules as latestNote(…, true).
func latestReviewNotes(dir string) map[string]string {
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	mods := map[string]int64{}
	for _, e := range entries {
		m := reviewNoteRE.FindStringSubmatch(e.Name())
		if e.IsDir() || m == nil {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		mod := fi.ModTime().UnixNano()
		if _, ok := out[m[1]]; !ok || mod > mods[m[1]] {
			out[m[1]], mods[m[1]] = path, mod
		}
	}
	return out
}

type specFile struct {
	path    string
	modTime time.Time
}

// listSpecs maps slug → spec file for the .md files directly in dir.
func listSpecs(dir string) map[string]specFile {
	out := map[string]specFile{}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out[slugFromFilename(e.Name())] = specFile{path: filepath.Join(dir, e.Name()), modTime: fi.ModTime()}
	}
	return out
}

// PrintStatusTable prints the no-argument `status` table: one row per slug
// across runs, ready specs, drafts and done specs, or the "no runs yet"
// line if there is nothing at all to show.
func PrintStatusTable(w io.Writer, homeDir, stateRoot string, o StatusTableOptions) error {
	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}
	matches, err := filepath.Glob(filepath.Join(stateRoot, "*", "run.json"))
	if err != nil {
		return err
	}
	runs := map[string]*runState{}
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			continue
		}
		var st runState
		if err := json.Unmarshal(data, &st); err != nil {
			continue
		}
		runs[filepath.Base(filepath.Dir(m))] = &st
	}
	drafts := listSpecs(home.DraftDir(homeDir))
	readys := listSpecs(home.ReadyDir(homeDir))
	dones := listSpecs(home.DoneDir(homeDir))

	slugSet := map[string]bool{}
	for _, set := range []map[string]specFile{drafts, readys, dones} {
		for s := range set {
			slugSet[s] = true
		}
	}
	for s := range runs {
		slugSet[s] = true
	}
	if len(slugSet) == 0 {
		fmt.Fprint(w, "no runs yet — nightshift new <slug> to start a spec\n")
		return nil
	}

	reviews := latestReviewNotes(home.ResultsDir(homeDir))
	opts := Options{Home: homeDir, StateRoot: stateRoot}

	type row struct {
		slug, status, iter, commits, repo, review string
		updated                                   time.Time
		group                                     int
	}
	var rows []row
	for slug := range slugSet {
		st := runs[slug]
		r := row{slug: slug, iter: "-", commits: "-", repo: "-", review: "-"}

		spec, haveSpec := readys[slug]
		if !haveSpec {
			spec, haveSpec = drafts[slug]
		}
		doneSpec, inDone := dones[slug]
		var fm map[string]string
		if inDone {
			spec, haveSpec = doneSpec, true
		}
		if haveSpec {
			if data, err := os.ReadFile(spec.path); err == nil {
				fm = home.ParseFrontmatter(data)
			}
		}

		status, ok := displayStatus(stateRoot, slug, st, inDone)
		_, isReady := readys[slug]
		switch {
		case ok:
			r.status = status
			switch {
			case inDone:
				r.group = groupDone
			case strings.HasPrefix(status, "running") || strings.HasPrefix(status, "waiting"):
				r.group = groupActive
			case status == "complete":
				r.group = groupComplete
			default:
				r.group = groupAttention
			}
		case isReady:
			r.status, r.group = "ready", groupReady
			if deps, derr := parseDependsOn(fm["depends_on"]); derr == nil && len(deps) > 0 {
				if d, _, ok := firstUnmet(opts, deps); ok {
					r.status = fmt.Sprintf("ready (waits on %s)", d)
				}
			}
		default:
			r.status, r.group = "draft", groupDraft
		}

		switch {
		case inDone:
			if t, err := time.Parse(time.RFC3339, home.Unquote(strings.TrimSpace(fm["done"]))); err == nil {
				r.updated = t
			} else if t, ok := lastHistoryTime(st); ok {
				r.updated = t
			} else {
				r.updated = spec.modTime
			}
		case st != nil:
			if t, ok := lastHistoryTime(st); ok {
				r.updated = t
			} else if t, err := time.Parse(time.RFC3339, st.Started); err == nil {
				r.updated = t
			}
		default:
			r.updated = spec.modTime
		}

		if st != nil {
			r.iter, r.commits = fmt.Sprint(st.Iterations), fmt.Sprint(st.Commits)
		}
		if st != nil && st.Repo != "" {
			r.repo = filepath.Base(st.Repo)
		} else if st == nil {
			if v := home.Unquote(strings.TrimSpace(fm["repo"])); v != "" {
				if p, err := expandHome(v); err == nil {
					r.repo = filepath.Base(p)
				}
			}
		}
		if note, ok := reviews[slug]; ok {
			r.review = noteVerdict(note)
		}
		rows = append(rows, r)
	}

	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.group != b.group {
			return a.group < b.group
		}
		if !a.updated.Equal(b.updated) {
			return a.updated.After(b.updated)
		}
		return a.slug < b.slug
	})

	window, windowText := 72*time.Hour, "3d"
	if o.Since > 0 {
		window = o.Since
		if o.SinceText != "" {
			windowText = o.SinceText
		} else {
			windowText = o.Since.String()
		}
	}
	shown := rows[:0:0]
	hidden := 0
	for _, r := range rows {
		if o.Repo != "" && r.repo != o.Repo {
			continue
		}
		if !o.All && r.group == groupDone && now.Sub(r.updated) > window {
			hidden++
			continue
		}
		shown = append(shown, r)
	}
	rows = shown

	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	if len(rows) > 0 {
		fmt.Fprint(tw, "SLUG\tSTATUS\tITER\tCOMMITS\tUPDATED\tREPO\tREVIEW\n")
	}
	for _, r := range rows {
		updated := "-"
		if !r.updated.IsZero() {
			updated = r.updated.Local().Format(statusTimeLayout)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.slug, r.status, r.iter, r.commits, updated, r.repo, r.review)
	}
	_ = tw.Flush()
	if hidden > 0 {
		fmt.Fprintf(w, "+%d done more than %s ago (--all to show)\n", hidden, windowText)
	}

	if active := readActiveLimit(stateRoot, now, false); active != nil {
		resetAt, _ := time.Parse(time.RFC3339, active.ResetAt)
		fmt.Fprintf(w, "usage limit: until %s\n", resetAt.Local().Format("Mon 15:04"))
	}
	complete := 0
	for _, r := range rows {
		if r.group == groupComplete {
			complete++
		}
	}
	if complete > 0 {
		fmt.Fprintf(w, "%d complete — review and merge, then nightshift done <slug> (or nightshift done --complete)\n", complete)
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
	_, inDone := findCaseInsensitive(home.DoneDir(homeDir), slug+".md")
	status := st.Status
	if ds, ok := displayStatus(stateRoot, slug, &st, inDone); ok {
		status = ds
	}
	fmt.Fprintf(w, "status: %s\n", status)
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
	fmt.Fprintf(w, "logs: nightshift logs %s [-f] [--agent]\n", slug)

	resultsDir := home.ResultsDir(homeDir)

	latest := "none"
	if newest := latestNote(resultsDir, slug, false); newest != "" {
		latest = newest
	}
	fmt.Fprintf(w, "latest result: %s\n", latest)

	latestReview := "none"
	if newest := latestNote(resultsDir, slug, true); newest != "" {
		latestReview = fmt.Sprintf("%s — %s", noteVerdict(newest), newest)
	}
	fmt.Fprintf(w, "latest review: %s\n", latestReview)

	fmt.Fprintf(w, "review: git -C %s log --oneline %s..%s\n", st.Repo, short(st.Base), reviewRefFor(&st, slug))
	return nil
}

// CompleteReadySlugs returns, in slug order, the ready specs whose run.json
// status is "complete" and whose spec lock is not held — the set
// `nightshift done --complete` marks done. Slugs that already have a spec in
// specs/done/ come back in skipped instead, since marking them would fail.
func CompleteReadySlugs(homeDir, stateRoot string) (slugs, skipped []string) {
	ready := listSpecs(home.ReadyDir(homeDir))
	keys := make([]string, 0, len(ready))
	for slug := range ready {
		keys = append(keys, slug)
	}
	sort.Strings(keys)
	for _, slug := range keys {
		st := loadPriorState(Options{StateRoot: stateRoot}, slug)
		if st == nil || st.Status != "complete" {
			continue
		}
		if _, held := lock.Probe(specLockPath(stateRoot, slug)); held {
			continue
		}
		if _, exists := findCaseInsensitive(home.DoneDir(homeDir), slug+".md"); exists {
			skipped = append(skipped, slug)
			continue
		}
		slugs = append(slugs, slug)
	}
	return slugs, skipped
}
