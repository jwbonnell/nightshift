package review

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
)

var (
	noteShaRe     = regexp.MustCompile(`(?m)^sha:\s*"?([0-9a-fA-F]{40})"?\s*$`)
	noteProjectRe = regexp.MustCompile(`(?m)^project:\s*(.+?)\s*$`)
	noteForgeRe   = regexp.MustCompile(`(?m)^forge:\s*(\S+)\s*$`)
)

// readNoteMeta reads just enough of an existing note's frontmatter to
// decide ownership and skip decisions: its forge, its canonical project
// path, and its reviewed head sha. ok is false whenever the file can't
// be read or doesn't look like a frontmatter'd note at all; k/project/sha
// are still returned as "" when ok is true but a given field is simply
// absent (P1 extends the reference's reader with the forge: line).
func readNoteMeta(notePath string) (k forge.Kind, project, sha string, ok bool) {
	b, err := os.ReadFile(notePath)
	if err != nil {
		return "", "", "", false
	}
	s := string(b)
	if !strings.HasPrefix(s, "---\n") {
		return "", "", "", false
	}
	end := strings.Index(s[4:], "\n---\n")
	if end < 0 {
		return "", "", "", false
	}
	fm := s[:4+end]

	if m := noteShaRe.FindStringSubmatch(fm); m != nil {
		sha = m[1]
	}
	if m := noteProjectRe.FindStringSubmatch(fm); m != nil {
		raw := m[1]
		if strings.HasPrefix(raw, `"`) {
			var decoded string
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				project = decoded
			}
		} else {
			project = raw
		}
	}
	if m := noteForgeRe.FindStringSubmatch(fm); m != nil {
		k = forge.Kind(m[1])
	}
	return k, project, sha, true
}

// shortSHA returns s's first 8 characters, or s unchanged when it's no
// longer than that.
func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// preClone is a pre-review snapshot of one change, known before any
// clone/fetch happens — enough to compute its note slug and decide
// whether to skip it outright.
type preClone struct {
	Kind   forge.Kind
	Repo   string // canonical repo path (forge.Repo.Path)
	RepoID int64
	Number int
	Draft  bool
	State  string // normalized: "open" | "closed" | "merged" | "locked"
	SHA    string
	Suffix string // forge.Client.SlugSuffix(): "mr" / "pr"
}

// preCloneSkip computes c's note slug/path and decides whether to skip
// it, per the reference's preCloneSkip (4.4.6) extended for two forges
// (P1) and P8.2's double-collision refusal:
//
//  1. slug/notePath start at the plain Slug(Repo, Number, Suffix).
//  2. If a note already exists there but belongs to a different forge or
//     project (collision), fall back to the "-p<RepoID>" suffixed slug.
//  3. If THAT slug is also taken by someone else, return an error instead
//     of silently colliding (P8.2) — the caller treats this as a failed
//     outcome and writes neither note.
//  4. Otherwise: a pending-mode draft is skipped unless includeDrafts; a
//     non-open change is skipped unless force; an unchanged head sha
//     (same owner, same sha) is skipped unless force. force bypasses
//     state and already-reviewed, but never draft.
func preCloneSkip(homeDir string, pending, force, includeDrafts bool, c preClone) (slug, notePath, reason string, err error) {
	slug = Slug(c.Repo, c.Number, c.Suffix)
	notePath = filepath.Join(home.ReviewsDir(homeDir), slug+".md")

	mKind, mProject, mSHA, mOK := readNoteMeta(notePath)
	belongs := mOK && mKind == c.Kind && strings.EqualFold(mProject, c.Repo)

	if mOK && !belongs {
		baseSlug, baseNotePath := slug, notePath
		slug = fmt.Sprintf("%s-p%d", slug, c.RepoID)
		notePath = filepath.Join(home.ReviewsDir(homeDir), slug+".md")

		mKind, mProject, mSHA, mOK = readNoteMeta(notePath)
		belongs = mOK && mKind == c.Kind && strings.EqualFold(mProject, c.Repo)
		if mOK && !belongs {
			return "", "", "", fmt.Errorf("note slug %s collides with %s and %s — rename one by hand", baseSlug, baseNotePath, notePath)
		}
	}

	if pending && c.Draft && !includeDrafts {
		return slug, notePath, "draft", nil
	}
	if c.State != "open" && !force {
		return slug, notePath, fmt.Sprintf("state %s", c.State), nil
	}
	if !force && belongs && mSHA != "" && strings.EqualFold(mSHA, c.SHA) {
		return slug, notePath, fmt.Sprintf("already reviewed at %s", shortSHA(mSHA)), nil
	}
	return slug, notePath, "", nil
}

// noteData is everything writeNote needs to render one review note
// (P6): the forge-normalized change fields plus what the review loop
// itself computed.
type noteData struct {
	Kind    forge.Kind
	Slug    string
	Label   string // forge-native: "path!n" / "path#n"
	Project string // canonical repo path
	Number  int
	Title   string
	Author  string
	URL     string
	Head    string
	Base    string
	Verdict string
	Lines   int

	CostUSD   float64
	CostKnown bool

	Model string // agent.Selector.Display()
	Body  string
}

// writeNote renders d's note (P6's byte-exact template) and writes it to
// <Home>/reviews/<d.Slug>.md via a temp file (0600) plus rename,
// overwriting any existing note at that path. It returns the final path
// and the exact bytes written, so callers can mirror those same bytes
// without re-reading the file.
func writeNote(homeDir string, d noteData, now time.Time) (notePath string, content []byte, err error) {
	dir := home.ReviewsDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, err
	}
	notePath = filepath.Join(dir, d.Slug+".md")

	projJSON, err := json.Marshal(d.Project)
	if err != nil {
		return "", nil, err
	}
	titleJSON, err := json.Marshal(d.Title)
	if err != nil {
		return "", nil, err
	}
	authorJSON, err := json.Marshal(d.Author)
	if err != nil {
		return "", nil, err
	}

	cost := "n/a"
	if d.CostKnown {
		cost = fmt.Sprintf("%.2f", d.CostUSD)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "---\ntype: nightshift-pr-review\nforge: %s\nproject: %s\nnumber: %d\ntitle: %s\nauthor: %s\nurl: %s\nsha: %s\nbase: %s\nverdict: %s\nlines: %d\ncost_usd: %s\nmodel: %s\nreviewed: %s\n---\n\n",
		d.Kind, projJSON, d.Number, titleJSON, authorJSON, d.URL, d.Head, d.Base, d.Verdict, d.Lines, cost, d.Model, now.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "# %s — %s\n\n[Open %s](%s) · @%s · %d lines · head `%s`\n\n%s\n",
		d.Label, d.Title, d.Kind.Noun(), d.URL, d.Author, d.Lines, shortSHA(d.Head), strings.TrimRight(d.Body, "\n"))
	content = []byte(b.String())

	tmpPath := filepath.Join(dir, "."+d.Slug+".md.tmp")
	if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
		return "", nil, err
	}
	if err := os.Rename(tmpPath, notePath); err != nil {
		os.Remove(tmpPath)
		return "", nil, err
	}
	return notePath, content, nil
}

// commitNote commits the primary note into Home's own git history
// (P6), scoped to just that one file, when Home is a git repo. A commit
// failure is printed and never fails the review, since the review itself
// already succeeded by the time this runs.
func commitNote(homeDir, notePath, label, verdict string, stderr io.Writer) {
	if !gitx.IsRepo(homeDir) {
		return
	}
	rel, err := filepath.Rel(homeDir, notePath)
	if err != nil {
		rel = notePath
	}
	if err := gitx.Commit(homeDir, fmt.Sprintf("review: %s (%s)", label, verdict), rel); err != nil {
		fmt.Fprintf(stderr, "nightshift pr: commit review note failed (review still counts): %v\n", err)
	}
}

// mirrorNote copies content byte-for-byte to <mirrorDir>/<slug>.md when
// mirrorDir is non-empty (P6). The mirror is write-only — it is never
// read for skip decisions — and any failure (write or commit) is printed
// rather than failing the review. When mirrorDir sits inside a git work
// tree, the copy is additionally committed there, scoped to just that
// one file; existing unrelated files in the mirror or its repo are never
// touched.
func mirrorNote(mirrorDir, slug string, content []byte, label, verdict string, stderr io.Writer) {
	if mirrorDir == "" {
		return
	}
	if err := mirrorNoteErr(mirrorDir, slug, content, label, verdict); err != nil {
		fmt.Fprintf(stderr, "nightshift pr: mirror: %v\n", err)
	}
}

func mirrorNoteErr(mirrorDir, slug string, content []byte, label, verdict string) error {
	if err := os.MkdirAll(mirrorDir, 0o755); err != nil {
		return err
	}
	notePath := filepath.Join(mirrorDir, slug+".md")
	tmpPath := filepath.Join(mirrorDir, "."+slug+".md.tmp")
	if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, notePath); err != nil {
		os.Remove(tmpPath)
		return err
	}

	resolvedDir, err := filepath.EvalSymlinks(mirrorDir)
	if err != nil {
		return err
	}
	if !gitx.IsRepo(resolvedDir) {
		return nil
	}
	root, err := gitx.GitDir(resolvedDir, "--show-toplevel")
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, filepath.Join(resolvedDir, slug+".md"))
	if err != nil {
		return err
	}
	return gitx.Commit(root, fmt.Sprintf("review: %s (%s)", label, verdict), rel)
}
