package review

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jwbonnell/nightshift/internal/forge"
	"github.com/jwbonnell/nightshift/internal/gitx"
	"github.com/jwbonnell/nightshift/internal/home"
)

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return out
}

// writeMinimalNote writes just enough of a note's frontmatter at
// <homeDir>/reviews/<slug>.md for readNoteMeta/preCloneSkip to read it
// back.
func writeMinimalNote(t *testing.T, homeDir, slug string, kind forge.Kind, project, sha string) string {
	t.Helper()
	dir := home.ReviewsDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir reviews: %v", err)
	}
	path := filepath.Join(dir, slug+".md")
	content := "---\n" +
		"type: nightshift-pr-review\n" +
		"forge: " + string(kind) + "\n" +
		"project: \"" + project + "\"\n" +
		"sha: " + sha + "\n" +
		"---\n\nbody\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write note: %v", err)
	}
	return path
}

func TestReadNoteMetaMissingFile(t *testing.T) {
	k, project, sha, ok := readNoteMeta(filepath.Join(t.TempDir(), "nope.md"))
	if ok {
		t.Fatalf("ok = true for a missing file (k=%q project=%q sha=%q)", k, project, sha)
	}
}

func TestReadNoteMetaFields(t *testing.T) {
	homeDir := t.TempDir()
	sha := strings.Repeat("a", 40)
	path := writeMinimalNote(t, homeDir, "team-app-mr12", forge.KindGitLab, "team/app", sha)

	k, project, gotSHA, ok := readNoteMeta(path)
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if k != forge.KindGitLab {
		t.Errorf("forge = %q, want gitlab", k)
	}
	if project != "team/app" {
		t.Errorf("project = %q, want team/app", project)
	}
	if gotSHA != sha {
		t.Errorf("sha = %q, want %q", gotSHA, sha)
	}
}

func TestReadNoteMetaNoFrontmatter(t *testing.T) {
	homeDir := t.TempDir()
	dir := home.ReviewsDir(homeDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "plain.md")
	if err := os.WriteFile(path, []byte("just text, no frontmatter\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, _, ok := readNoteMeta(path); ok {
		t.Fatal("ok = true for a file with no frontmatter")
	}
}

func TestShortSHA(t *testing.T) {
	cases := map[string]string{
		"0123456789abcdef": "01234567",
		"short":            "short",
		"exactly8":         "exactly8",
	}
	for in, want := range cases {
		if got := shortSHA(in); got != want {
			t.Errorf("shortSHA(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPreCloneSkip(t *testing.T) {
	baseSHA := strings.Repeat("a", 40)
	otherSHA := strings.Repeat("b", 40)

	type setup struct {
		kind    forge.Kind
		slug    string
		project string
		sha     string
	}

	tests := []struct {
		name       string
		existing   *setup
		pending    bool
		force      bool
		drafts     bool
		c          preClone
		wantReason string
	}{
		{
			name:    "pending draft skipped without include-drafts",
			pending: true,
			drafts:  false,
			c: preClone{
				Kind: forge.KindGitLab, Repo: "team/app", RepoID: 1, Number: 1,
				Draft: true, State: "open", SHA: baseSHA, Suffix: "mr",
			},
			wantReason: "draft",
		},
		{
			name:    "pending draft included with --include-drafts",
			pending: true,
			drafts:  true,
			c: preClone{
				Kind: forge.KindGitLab, Repo: "team/app", RepoID: 1, Number: 2,
				Draft: true, State: "open", SHA: baseSHA, Suffix: "mr",
			},
			wantReason: "",
		},
		{
			name:    "explicit target never skips a draft",
			pending: false,
			drafts:  false,
			c: preClone{
				Kind: forge.KindGitLab, Repo: "team/app", RepoID: 1, Number: 3,
				Draft: true, State: "open", SHA: baseSHA, Suffix: "mr",
			},
			wantReason: "",
		},
		{
			name: "non-open state skipped without force",
			c: preClone{
				Kind: forge.KindGitHub, Repo: "o/r", RepoID: 2, Number: 4,
				State: "closed", SHA: baseSHA, Suffix: "pr",
			},
			wantReason: "state closed",
		},
		{
			name:  "non-open state bypassed with force",
			force: true,
			c: preClone{
				Kind: forge.KindGitHub, Repo: "o/r", RepoID: 2, Number: 5,
				State: "closed", SHA: baseSHA, Suffix: "pr",
			},
			wantReason: "",
		},
		{
			name:     "already reviewed at this head sha",
			existing: &setup{kind: forge.KindGitLab, slug: "team-app-mr6", project: "team/app", sha: baseSHA},
			c: preClone{
				Kind: forge.KindGitLab, Repo: "team/app", RepoID: 1, Number: 6,
				State: "open", SHA: baseSHA, Suffix: "mr",
			},
			wantReason: "already reviewed at " + shortSHA(baseSHA),
		},
		{
			name:     "already reviewed bypassed with force",
			existing: &setup{kind: forge.KindGitLab, slug: "team-app-mr7", project: "team/app", sha: baseSHA},
			force:    true,
			c: preClone{
				Kind: forge.KindGitLab, Repo: "team/app", RepoID: 1, Number: 7,
				State: "open", SHA: baseSHA, Suffix: "mr",
			},
			wantReason: "",
		},
		{
			name:     "head sha advanced since the note: not a skip",
			existing: &setup{kind: forge.KindGitLab, slug: "team-app-mr8", project: "team/app", sha: baseSHA},
			c: preClone{
				Kind: forge.KindGitLab, Repo: "team/app", RepoID: 1, Number: 8,
				State: "open", SHA: otherSHA, Suffix: "mr",
			},
			wantReason: "",
		},
		{
			name: "no existing note at all",
			c: preClone{
				Kind: forge.KindGitHub, Repo: "o/r", RepoID: 2, Number: 9,
				State: "open", SHA: baseSHA, Suffix: "pr",
			},
			wantReason: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			homeDir := t.TempDir()
			if tt.existing != nil {
				writeMinimalNote(t, homeDir, tt.existing.slug, tt.existing.kind, tt.existing.project, tt.existing.sha)
			}
			slug, notePath, reason, err := preCloneSkip(homeDir, tt.pending, tt.force, tt.drafts, tt.c)
			if err != nil {
				t.Fatalf("preCloneSkip: %v", err)
			}
			if reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", reason, tt.wantReason)
			}
			wantSlug := Slug(tt.c.Repo, tt.c.Number, tt.c.Suffix)
			if slug != wantSlug {
				t.Errorf("slug = %q, want %q", slug, wantSlug)
			}
			if notePath != filepath.Join(home.ReviewsDir(homeDir), wantSlug+".md") {
				t.Errorf("notePath = %q, unexpected", notePath)
			}
		})
	}
}

func TestForgeMismatchIsCollision(t *testing.T) {
	homeDir := t.TempDir()
	// A note already sits at exactly the slug a GitHub #9 target for
	// "acme/widgets" would compute, but it belongs to GitLab.
	writeMinimalNote(t, homeDir, "acme-widgets-pr9", forge.KindGitLab, "acme/widgets", strings.Repeat("a", 40))

	c := preClone{
		Kind: forge.KindGitHub, Repo: "acme/widgets", RepoID: 555, Number: 9,
		State: "open", SHA: strings.Repeat("b", 40), Suffix: "pr",
	}
	slug, notePath, reason, err := preCloneSkip(homeDir, false, false, false, c)
	if err != nil {
		t.Fatalf("preCloneSkip: %v", err)
	}
	wantSlug := "acme-widgets-pr9-p555"
	if slug != wantSlug {
		t.Errorf("slug = %q, want %q", slug, wantSlug)
	}
	wantPath := filepath.Join(home.ReviewsDir(homeDir), wantSlug+".md")
	if notePath != wantPath {
		t.Errorf("notePath = %q, want %q", notePath, wantPath)
	}
	if reason != "" {
		t.Errorf("reason = %q, want no skip (no note exists yet at the suffixed slug)", reason)
	}
}

func TestDoubleCollisionRefuses(t *testing.T) {
	homeDir := t.TempDir()
	writeMinimalNote(t, homeDir, "acme-widgets-pr9", forge.KindGitLab, "acme/widgets", strings.Repeat("a", 40))
	writeMinimalNote(t, homeDir, "acme-widgets-pr9-p555", forge.KindGitLab, "acme/widgets", strings.Repeat("c", 40))

	baseBytes, err := os.ReadFile(filepath.Join(home.ReviewsDir(homeDir), "acme-widgets-pr9.md"))
	if err != nil {
		t.Fatalf("read base note: %v", err)
	}
	suffixedBytes, err := os.ReadFile(filepath.Join(home.ReviewsDir(homeDir), "acme-widgets-pr9-p555.md"))
	if err != nil {
		t.Fatalf("read suffixed note: %v", err)
	}

	c := preClone{
		Kind: forge.KindGitHub, Repo: "acme/widgets", RepoID: 555, Number: 9,
		State: "open", SHA: strings.Repeat("b", 40), Suffix: "pr",
	}
	slug, notePath, reason, err := preCloneSkip(homeDir, false, false, false, c)
	if err == nil {
		t.Fatal("preCloneSkip succeeded, want the P8.2 collision error")
	}
	if slug != "" || notePath != "" || reason != "" {
		t.Errorf("preCloneSkip returned non-empty slug/notePath/reason alongside an error: %q %q %q", slug, notePath, reason)
	}
	wantErr := "note slug acme-widgets-pr9 collides with " +
		filepath.Join(home.ReviewsDir(homeDir), "acme-widgets-pr9.md") + " and " +
		filepath.Join(home.ReviewsDir(homeDir), "acme-widgets-pr9-p555.md") + " — rename one by hand"
	if err.Error() != wantErr {
		t.Errorf("error = %q, want %q", err.Error(), wantErr)
	}

	// Neither note was touched.
	gotBase, err := os.ReadFile(filepath.Join(home.ReviewsDir(homeDir), "acme-widgets-pr9.md"))
	if err != nil || !bytes.Equal(gotBase, baseBytes) {
		t.Errorf("base note was modified")
	}
	gotSuffixed, err := os.ReadFile(filepath.Join(home.ReviewsDir(homeDir), "acme-widgets-pr9-p555.md"))
	if err != nil || !bytes.Equal(gotSuffixed, suffixedBytes) {
		t.Errorf("suffixed note was modified")
	}
}

func gitlabNoteData() noteData {
	return noteData{
		Kind:      forge.KindGitLab,
		Slug:      "team-app-mr12",
		Label:     "team/app!12",
		Project:   "team/app",
		Number:    12,
		Title:     "Fix auth",
		Author:    "alice",
		URL:       "https://gitlab.example.com/team/app/-/merge_requests/12",
		Head:      "0123456789abcdef0123456789abcdef01234567",
		Base:      "89abcdef0123456789abcdef0123456789abcdef",
		Verdict:   "LGTM",
		Lines:     1,
		CostUSD:   0.41,
		CostKnown: true,
		Model:     "sonnet",
		Body:      "VERDICT: LGTM\n...review body...",
	}
}

func githubNoteData() noteData {
	return noteData{
		Kind:      forge.KindGitHub,
		Slug:      "owner-repo-pr7",
		Label:     "owner/repo#7",
		Project:   "owner/repo",
		Number:    7,
		Title:     "Add feature",
		Author:    "bob",
		URL:       "https://github.com/owner/repo/pull/7",
		Head:      "abcdef0123456789abcdef0123456789abcdef01",
		Base:      "0123456789abcdef0123456789abcdef01234567",
		Verdict:   "CONCERNS",
		Lines:     42,
		CostKnown: false,
		Model:     "opencode/opencode/longcat-2.5-preview-free",
		Body:      "VERDICT: CONCERNS\nsome notes",
	}
}

func TestNoteFormatPerForge(t *testing.T) {
	gitlabWant := "---\n" +
		"type: nightshift-pr-review\n" +
		"forge: gitlab\n" +
		"project: \"team/app\"\n" +
		"number: 12\n" +
		"title: \"Fix auth\"\n" +
		"author: \"alice\"\n" +
		"url: https://gitlab.example.com/team/app/-/merge_requests/12\n" +
		"sha: 0123456789abcdef0123456789abcdef01234567\n" +
		"base: 89abcdef0123456789abcdef0123456789abcdef\n" +
		"verdict: LGTM\n" +
		"lines: 1\n" +
		"cost_usd: 0.41\n" +
		"model: sonnet\n" +
		"reviewed: 2026-09-29T12:00:00Z\n" +
		"---\n" +
		"\n" +
		"# team/app!12 — Fix auth\n" +
		"\n" +
		"[Open MR](https://gitlab.example.com/team/app/-/merge_requests/12) · @alice · 1 lines · head `01234567`\n" +
		"\n" +
		"VERDICT: LGTM\n...review body...\n"

	githubWant := "---\n" +
		"type: nightshift-pr-review\n" +
		"forge: github\n" +
		"project: \"owner/repo\"\n" +
		"number: 7\n" +
		"title: \"Add feature\"\n" +
		"author: \"bob\"\n" +
		"url: https://github.com/owner/repo/pull/7\n" +
		"sha: abcdef0123456789abcdef0123456789abcdef01\n" +
		"base: 0123456789abcdef0123456789abcdef01234567\n" +
		"verdict: CONCERNS\n" +
		"lines: 42\n" +
		"cost_usd: n/a\n" +
		"model: opencode/opencode/longcat-2.5-preview-free\n" +
		"reviewed: 2026-10-01T08:15:00Z\n" +
		"---\n" +
		"\n" +
		"# owner/repo#7 — Add feature\n" +
		"\n" +
		"[Open PR](https://github.com/owner/repo/pull/7) · @bob · 42 lines · head `abcdef01`\n" +
		"\n" +
		"VERDICT: CONCERNS\nsome notes\n"

	cases := []struct {
		name string
		d    noteData
		now  time.Time
		want string
	}{
		{"gitlab", gitlabNoteData(), time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC), gitlabWant},
		{"github", githubNoteData(), time.Date(2026, 10, 1, 8, 15, 0, 0, time.UTC), githubWant},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			homeDir := t.TempDir()
			notePath, content, err := writeNote(homeDir, tc.d, tc.now)
			if err != nil {
				t.Fatalf("writeNote: %v", err)
			}
			if string(content) != tc.want {
				t.Errorf("content =\n%s\nwant\n%s", content, tc.want)
			}
			onDisk, err := os.ReadFile(notePath)
			if err != nil {
				t.Fatalf("read note: %v", err)
			}
			if string(onDisk) != tc.want {
				t.Errorf("on-disk content =\n%s\nwant\n%s", onDisk, tc.want)
			}
			info, err := os.Stat(notePath)
			if err != nil {
				t.Fatalf("stat note: %v", err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("note mode = %o, want 0600", info.Mode().Perm())
			}
			if !strings.Contains(string(content), "type: nightshift-pr-review\n") {
				t.Errorf("note missing the nightshift-pr-review type line")
			}
			// No stray temp file left behind.
			if _, err := os.Stat(filepath.Join(home.ReviewsDir(homeDir), "."+tc.d.Slug+".md.tmp")); !os.IsNotExist(err) {
				t.Errorf("temp file still exists: %v", err)
			}
		})
	}
}

func TestPRNoteTypeDistinct(t *testing.T) {
	homeDir := t.TempDir()
	_, content, err := writeNote(homeDir, gitlabNoteData(), time.Now())
	if err != nil {
		t.Fatalf("writeNote: %v", err)
	}
	if !strings.Contains(string(content), "\ntype: nightshift-pr-review\n") {
		t.Errorf("note frontmatter missing type: nightshift-pr-review")
	}
}

func TestWriteNoteOverwritesExisting(t *testing.T) {
	homeDir := t.TempDir()
	d := gitlabNoteData()
	if _, _, err := writeNote(homeDir, d, time.Now()); err != nil {
		t.Fatalf("writeNote (first): %v", err)
	}
	d.Verdict = "BLOCKING"
	notePath, content, err := writeNote(homeDir, d, time.Now())
	if err != nil {
		t.Fatalf("writeNote (second): %v", err)
	}
	onDisk, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(onDisk) != string(content) || !strings.Contains(string(onDisk), "verdict: BLOCKING\n") {
		t.Errorf("second writeNote didn't overwrite the first: %s", onDisk)
	}
}

func TestHomeCommitOnlyWhenRepo(t *testing.T) {
	t.Run("plain home: no commit attempted", func(t *testing.T) {
		homeDir := t.TempDir()
		notePath, _, err := writeNote(homeDir, gitlabNoteData(), time.Now())
		if err != nil {
			t.Fatalf("writeNote: %v", err)
		}
		var stderr bytes.Buffer
		commitNote(homeDir, notePath, "team/app!12", "LGTM", &stderr)
		if stderr.Len() != 0 {
			t.Errorf("unexpected stderr for a non-repo home: %q", stderr.String())
		}
		if gitx.IsRepo(homeDir) {
			t.Fatal("test setup: homeDir unexpectedly became a repo")
		}
	})

	t.Run("home repo: exactly one scoped commit", func(t *testing.T) {
		homeDir := t.TempDir()
		mustGit(t, homeDir, "init", "-q")
		// An unrelated dirty file must never get staged by the note commit.
		if err := os.WriteFile(filepath.Join(homeDir, "unrelated.txt"), []byte("scratch"), 0o600); err != nil {
			t.Fatalf("seed unrelated file: %v", err)
		}

		notePath, _, err := writeNote(homeDir, gitlabNoteData(), time.Now())
		if err != nil {
			t.Fatalf("writeNote: %v", err)
		}
		var stderr bytes.Buffer
		commitNote(homeDir, notePath, "team/app!12", "LGTM", &stderr)
		if stderr.Len() != 0 {
			t.Fatalf("unexpected stderr: %q", stderr.String())
		}

		log := mustGit(t, homeDir, "log", "--oneline", "-1", "--format=%s")
		if log != "review: team/app!12 (LGTM)" {
			t.Errorf("commit message = %q, want %q", log, "review: team/app!12 (LGTM)")
		}
		files := mustGit(t, homeDir, "show", "--name-only", "--format=", "HEAD")
		if strings.TrimSpace(files) != "reviews/team-app-mr12.md" {
			t.Errorf("committed files = %q, want only reviews/team-app-mr12.md", files)
		}
		status := mustGit(t, homeDir, "status", "--porcelain")
		if !strings.Contains(status, "unrelated.txt") {
			t.Errorf("unrelated.txt should still be untracked/dirty, status = %q", status)
		}
	})
}

func TestMirrorWrittenAndCommitted(t *testing.T) {
	mirrorDir := t.TempDir()
	mustGit(t, mirrorDir, "init", "-q")
	if err := os.WriteFile(filepath.Join(mirrorDir, "unrelated.txt"), []byte("scratch"), 0o600); err != nil {
		t.Fatalf("seed unrelated file: %v", err)
	}

	content := []byte("---\ntype: nightshift-pr-review\n---\n\nbody\n")
	var stderr bytes.Buffer
	mirrorNote(mirrorDir, "team-app-mr12", content, "team/app!12", "LGTM", &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", stderr.String())
	}

	notePath := filepath.Join(mirrorDir, "team-app-mr12.md")
	got, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read mirrored note: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("mirrored content = %q, want %q", got, content)
	}

	log := mustGit(t, mirrorDir, "log", "--oneline", "-1", "--format=%s")
	if log != "review: team/app!12 (LGTM)" {
		t.Errorf("mirror commit message = %q, want %q", log, "review: team/app!12 (LGTM)")
	}
	files := mustGit(t, mirrorDir, "show", "--name-only", "--format=", "HEAD")
	if strings.TrimSpace(files) != "team-app-mr12.md" {
		t.Errorf("committed files = %q, want only team-app-mr12.md", files)
	}
	status := mustGit(t, mirrorDir, "status", "--porcelain")
	if !strings.Contains(status, "unrelated.txt") {
		t.Errorf("unrelated.txt should still be untracked/dirty, status = %q", status)
	}
}

func TestMirrorNotAGitRepoSkipsCommitSilently(t *testing.T) {
	mirrorDir := t.TempDir()
	content := []byte("note body\n")
	var stderr bytes.Buffer
	mirrorNote(mirrorDir, "slug", content, "label", "LGTM", &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr for a plain (non-repo) mirror dir: %q", stderr.String())
	}
	got, err := os.ReadFile(filepath.Join(mirrorDir, "slug.md"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("mirror note not written correctly: %v %q", err, got)
	}
}

func TestMirrorFailureNonFatal(t *testing.T) {
	// Make the mirror dir unwritable by putting a file where the mirror
	// dir itself needs to be a directory.
	parent := t.TempDir()
	mirrorDir := filepath.Join(parent, "mirror-is-a-file")
	if err := os.WriteFile(mirrorDir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	var stderr bytes.Buffer
	mirrorNote(mirrorDir, "slug", []byte("content\n"), "label", "LGTM", &stderr)
	if !strings.Contains(stderr.String(), "nightshift pr: mirror:") {
		t.Errorf("stderr = %q, want a mirror: failure message", stderr.String())
	}
}

func TestMirrorOffWhenEmpty(t *testing.T) {
	var stderr bytes.Buffer
	mirrorNote("", "slug", []byte("content\n"), "label", "LGTM", &stderr)
	if stderr.Len() != 0 {
		t.Errorf("unexpected stderr with mirror off: %q", stderr.String())
	}
}
