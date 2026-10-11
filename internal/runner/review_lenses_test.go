package runner

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"
)

// seqReviewer returns one scripted response per call to Review, in
// order — unlike stubReviewer, which always returns the same response.
// Used to script a different reply per lens.
type seqReviewer struct {
	texts   []string
	i       int
	prompts []string
}

func (s *seqReviewer) Review(ctx context.Context, ws, model, prompt string) (string, error) {
	s.prompts = append(s.prompts, prompt)
	idx := s.i
	s.i++
	if idx < len(s.texts) {
		return s.texts[idx], nil
	}
	return "", nil
}

func TestCombinedVerdict(t *testing.T) {
	tests := []struct {
		name     string
		verdicts []string
		want     string
	}{
		{"single approve", []string{"approve"}, "approve"},
		{"single reject", []string{"reject"}, "reject"},
		{"single error", []string{"error"}, "error"},
		{"single concerns", []string{"concerns"}, "concerns"},
		{"single unknown", []string{"unknown"}, "concerns"},
		{"all approve", []string{"approve", "approve"}, "approve"},
		{"any reject wins", []string{"approve", "reject"}, "reject"},
		{"reject beats error", []string{"reject", "error"}, "reject"},
		{"all error", []string{"error", "error"}, "error"},
		{"concerns mixed in", []string{"approve", "concerns"}, "concerns"},
		{"error mixed in (not all)", []string{"approve", "error"}, "concerns"},
		{"unknown mixed in", []string{"approve", "unknown"}, "concerns"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := combineVerdicts(tt.verdicts); got != tt.want {
				t.Fatalf("combineVerdicts(%v) = %q, want %q", tt.verdicts, got, tt.want)
			}
		})
	}
}

func TestBlockingItemsParse(t *testing.T) {
	results := []lensResult{
		{lens: "requirements", verdict: "reject", text: "VERDICT: reject\nSUMMARY: missing stuff.\n\n" +
			"## Acceptance criteria\n- [unmet] Does X\n- [met] Does Y\n\n" +
			"## Findings\n- [high] foo.go:1 — bad\n- [low] bar.go:2 — minor\n"},
		{lens: "quality", verdict: "approve", text: "## Findings\n- [high] should.go:1 — not counted\n"},
		{lens: "security", verdict: "reject", text: "VERDICT: reject\nSUMMARY: no specific findings here.\n\n" +
			"## Findings\n- none\n"},
		{lens: "style", verdict: "reject", text: "VERDICT: reject\nSUMMARY: style issue.\n\n" +
			"## Findings\n- [high] baz.go:3 — ugly\n"},
	}

	items := blockingItems(results, 1)
	want := []blockItem{
		{ID: "R1.1", Lens: "requirements", Text: "Does X"},
		{ID: "R1.2", Lens: "requirements", Text: "foo.go:1 — bad"},
		{ID: "S1.1", Lens: "security", Text: "security rejected the change: no specific findings here."},
		{ID: "S1.2", Lens: "style", Text: "baz.go:3 — ugly"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("items = %#v\nwant %#v", items, want)
	}
}

func TestSingleGeneralLensNoteUnchanged(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	text := "VERDICT: approve\nSUMMARY: fine."

	for _, lenses := range [][]string{nil, {"general"}} {
		rv := stubReviewer{text: text}
		p := reviewParams{
			home:      t.TempDir(),
			stateRoot: t.TempDir(),
			slug:      "demo",
			repo:      repo,
			base:      base,
			ref:       "main",
			model:     "opus",
			lenses:    lenses,
			now:       now,
		}
		verdict, notePath, _, err := runReview(context.Background(), rv, p)
		if err != nil {
			t.Fatalf("runReview(%v): %v", lenses, err)
		}
		if verdict != "approve" {
			t.Fatalf("verdict = %q, want approve", verdict)
		}
		data, err := os.ReadFile(notePath)
		if err != nil {
			t.Fatalf("read note: %v", err)
		}

		head := short(base)
		want := "---\ntype: nightshift-review\nspec: demo\nverdict: approve\nreviewed: " + head +
			"\nbase: " + head + "\nmodel: opus\ncreated: " + now.Format(time.RFC3339) +
			"\n---\n\n# Review: demo — approve\n\n" + text + "\n"
		if string(data) != want {
			t.Fatalf("lenses=%v note = %q\nwant %q", lenses, data, want)
		}
	}
}

func TestMultiLensNoteFormat(t *testing.T) {
	repo := newSourceRepo(t)
	base := mustGit(t, repo, "rev-parse", "HEAD")
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	reqText := "VERDICT: approve\nSUMMARY: reqs fine."
	secText := "VERDICT: reject\nSUMMARY: sec issue.\n\n## Findings\n- [high] repo/main.go:10 — bad\n"
	rv := &seqReviewer{texts: []string{reqText, secText}}

	p := reviewParams{
		home:      t.TempDir(),
		stateRoot: t.TempDir(),
		slug:      "demo",
		repo:      repo,
		base:      base,
		ref:       "main",
		model:     "opus",
		lenses:    []string{"requirements", "security"},
		round:     2,
		now:       now,
	}
	verdict, notePath, _, err := runReview(context.Background(), rv, p)
	if err != nil {
		t.Fatalf("runReview: %v", err)
	}
	if verdict != "reject" {
		t.Fatalf("verdict = %q, want reject", verdict)
	}
	if len(rv.prompts) != 2 {
		t.Fatalf("calls = %d, want 2", len(rv.prompts))
	}

	data, err := os.ReadFile(notePath)
	if err != nil {
		t.Fatalf("read note: %v", err)
	}

	head := short(base)
	want := "---\ntype: nightshift-review\nspec: demo\nverdict: reject\nreviewed: " + head +
		"\nbase: " + head + "\nmodel: opus\nlenses: requirements, security\nround: 2\ncreated: " +
		now.Format(time.RFC3339) + "\n---\n\n" +
		"# Review: demo — reject\n\n" +
		"## requirements — approve\n\n" + reqText + "\n\n" +
		"## security — reject\n\n" + secText + "\n"
	if string(data) != want {
		t.Fatalf("note = %q\nwant %q", data, want)
	}
}
