package usagelimit

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// importsOf parses every non-test .go file in dir with ImportsOnly and
// returns the raw import paths. Hermetic and fast: no go/build, no
// network, no module resolution.
func importsOf(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var imports []string
	fset := token.NewFileSet()
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imp := range f.Imports {
			imports = append(imports, strings.Trim(imp.Path.Value, `"`))
		}
	}
	return imports
}

// TestUsageLimitPackageMoved asserts internal/usagelimit imports only the
// std-lib, so internal/review can depend on it without pulling in sbx,
// git or notification code (SPEC.md P12.5).
func TestUsageLimitPackageMoved(t *testing.T) {
	for _, imp := range importsOf(t, ".") {
		if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
			t.Errorf("internal/usagelimit imports %q, want std-lib only", imp)
		}
	}
}

func TestParseResetTable(t *testing.T) {
	// now fixed well inside a day so "earlier than now" cases are deterministic.
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.Local)

	t.Run("time only, still ahead today", func(t *testing.T) {
		got, ok := ParseReset("3pm", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 11, 15, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time only, already past today rolls to tomorrow", func(t *testing.T) {
		got, ok := ParseReset("7am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 12, 7, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time with minutes", func(t *testing.T) {
		got, ok := ParseReset("7:30am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 12, 7, 30, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("24h time", func(t *testing.T) {
		got, ok := ParseReset("15:00", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 11, 15, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time with UTC zone", func(t *testing.T) {
		got, ok := ParseReset("7am (UTC)", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 11, 7, 0, 0, 0, time.UTC)
		if !want.Before(now) {
			t.Fatalf("test setup: want %v should be before now %v", want, now)
		}
		want = want.AddDate(0, 0, 1)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time with named zone", func(t *testing.T) {
		loc, err := time.LoadLocation("America/Detroit")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}
		got, ok := ParseReset("9am (America/Detroit)", now)
		if !ok {
			t.Fatal("expected ok")
		}
		nowInLoc := now.In(loc)
		want := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), 9, 0, 0, 0, loc)
		if !want.After(now) {
			want = want.AddDate(0, 0, 1)
		}
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("date and time, comma form", func(t *testing.T) {
		got, ok := ParseReset("Oct 14, 9am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("date and time, at form", func(t *testing.T) {
		got, ok := ParseReset("Oct 14 at 9am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("date and time with zone", func(t *testing.T) {
		loc, err := time.LoadLocation("America/Detroit")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}
		got, ok := ParseReset("Oct 14 at 9am (America/Detroit)", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 14, 9, 0, 0, 0, loc)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("date far in the past rolls to next year", func(t *testing.T) {
		// Oct 1 at current year is 10 days before "now" (Oct 11) -> more
		// than 7 days in the past -> rolls to next year.
		got, ok := ParseReset("Oct 1, 9am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2027, 10, 1, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("date within 7 days in the past stays current year", func(t *testing.T) {
		// Oct 5 at current year is 6 days before "now" (Oct 11) -> within
		// the 7-day grace window -> stays this year even though it's past.
		got, ok := ParseReset("Oct 5, 9am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("junk returns false", func(t *testing.T) {
		if _, ok := ParseReset("sometime soon", now); ok {
			t.Error("expected not ok")
		}
	})

	t.Run("bare hour with no am/pm returns false", func(t *testing.T) {
		if _, ok := ParseReset("7", now); ok {
			t.Error("expected not ok")
		}
	})

	t.Run("unknown zone returns false", func(t *testing.T) {
		if _, ok := ParseReset("7am (Nowhere/Fake)", now); ok {
			t.Error("expected not ok")
		}
	})
}

func TestParseLimitMessages(t *testing.T) {
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)

	t.Run("real session limit message", func(t *testing.T) {
		hit := Parse("You've hit your session limit · resets 7am (UTC)", now)
		if hit.Weekly {
			t.Error("expected not weekly")
		}
		want := time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("weekly limit message", func(t *testing.T) {
		hit := Parse("You've hit your weekly limit · resets Oct 14, 9am (America/Detroit)", now)
		if !hit.Weekly {
			t.Error("expected weekly")
		}
		loc, err := time.LoadLocation("America/Detroit")
		if err != nil {
			t.Fatalf("LoadLocation: %v", err)
		}
		want := time.Date(2026, 10, 14, 9, 0, 0, 0, loc)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("5-hour limit message", func(t *testing.T) {
		hit := Parse("5-hour limit reached · resets 3pm", now)
		if hit.Weekly {
			t.Error("expected not weekly")
		}
		nowInLoc := now.In(time.Local)
		want := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), 15, 0, 0, 0, time.Local)
		if !want.After(now) {
			want = want.AddDate(0, 0, 1)
		}
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("epoch form", func(t *testing.T) {
		hit := Parse("Claude AI usage limit reached|1760000400", now)
		if hit.Weekly {
			t.Error("expected not weekly")
		}
		want := time.Unix(1760000400, 0)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("weekly epoch form", func(t *testing.T) {
		hit := Parse("Claude AI weekly usage limit reached|1760000400", now)
		if !hit.Weekly {
			t.Error("expected weekly")
		}
		want := time.Unix(1760000400, 0)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("no reset phrase leaves ResetAt zero", func(t *testing.T) {
		hit := Parse("You've hit your usage limit", now)
		if !hit.ResetAt.IsZero() {
			t.Errorf("ResetAt = %v, want zero", hit.ResetAt)
		}
		if hit.Weekly {
			t.Error("expected not weekly")
		}
	})
}

// TestLimitMarkerWrittenAndKeepsLater exercises Write's merge rule
// directly: an existing marker whose reset_at is later than a new hit's
// is kept as-is; a new hit with a later reset_at replaces it. A zero
// ResetAt is stored as recorded+fallback.
func TestLimitMarkerWrittenAndKeepsLater(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 1, 5, 3, 0, 0, 0, time.UTC)

	if err := Write(dir, Hit{ResetAt: base.Add(30 * time.Minute)}, base, "first", time.Hour); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// An earlier-resetting hit arriving after must not overwrite the
	// later one already recorded.
	if err := Write(dir, Hit{ResetAt: base.Add(10 * time.Minute)}, base.Add(time.Minute), "second", time.Hour); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m := readRaw(dir)
	if m == nil {
		t.Fatalf("marker missing")
	}
	got, err := time.Parse(time.RFC3339, m.ResetAt)
	if err != nil {
		t.Fatalf("parse reset_at: %v", err)
	}
	if !got.Equal(base.Add(30 * time.Minute)) {
		t.Fatalf("reset_at = %v, want the later (kept) reset %v", got, base.Add(30*time.Minute))
	}
	if m.By != "first" {
		t.Fatalf("by = %q, want %q (first writer kept)", m.By, "first")
	}

	// A later-resetting hit does replace it.
	if err := Write(dir, Hit{ResetAt: base.Add(time.Hour), Weekly: true}, base.Add(2*time.Minute), "third", time.Hour); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m = readRaw(dir)
	got, err = time.Parse(time.RFC3339, m.ResetAt)
	if err != nil {
		t.Fatalf("parse reset_at: %v", err)
	}
	if !got.Equal(base.Add(time.Hour)) {
		t.Fatalf("reset_at = %v, want the new later reset %v", got, base.Add(time.Hour))
	}
	if m.By != "third" || !m.Weekly {
		t.Fatalf("marker = %+v, want by=third weekly=true", m)
	}

	// A zero ResetAt (an unparsable message) is stored as recorded+fallback.
	dir2 := t.TempDir()
	if err := Write(dir2, Hit{}, base, "x", 45*time.Minute); err != nil {
		t.Fatalf("Write: %v", err)
	}
	m2 := readRaw(dir2)
	got2, err := time.Parse(time.RFC3339, m2.ResetAt)
	if err != nil {
		t.Fatalf("parse reset_at: %v", err)
	}
	if !got2.Equal(base.Add(45 * time.Minute)) {
		t.Fatalf("reset_at = %v, want recorded+fallback %v", got2, base.Add(45*time.Minute))
	}
}
