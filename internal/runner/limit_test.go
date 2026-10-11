package runner

import (
	"testing"
	"time"
)

func TestParseResetTable(t *testing.T) {
	// now fixed well inside a day so "earlier than now" cases are deterministic.
	now := time.Date(2026, 10, 11, 10, 0, 0, 0, time.Local)

	t.Run("time only, still ahead today", func(t *testing.T) {
		got, ok := parseReset("3pm", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 11, 15, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time only, already past today rolls to tomorrow", func(t *testing.T) {
		got, ok := parseReset("7am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 12, 7, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time with minutes", func(t *testing.T) {
		got, ok := parseReset("7:30am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 12, 7, 30, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("24h time", func(t *testing.T) {
		got, ok := parseReset("15:00", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 11, 15, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("time with UTC zone", func(t *testing.T) {
		got, ok := parseReset("7am (UTC)", now)
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
		got, ok := parseReset("9am (America/Detroit)", now)
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
		got, ok := parseReset("Oct 14, 9am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 14, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("date and time, at form", func(t *testing.T) {
		got, ok := parseReset("Oct 14 at 9am", now)
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
		got, ok := parseReset("Oct 14 at 9am (America/Detroit)", now)
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
		got, ok := parseReset("Oct 1, 9am", now)
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
		got, ok := parseReset("Oct 5, 9am", now)
		if !ok {
			t.Fatal("expected ok")
		}
		want := time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local)
		if !got.Equal(want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("junk returns false", func(t *testing.T) {
		if _, ok := parseReset("sometime soon", now); ok {
			t.Error("expected not ok")
		}
	})

	t.Run("bare hour with no am/pm returns false", func(t *testing.T) {
		if _, ok := parseReset("7", now); ok {
			t.Error("expected not ok")
		}
	})

	t.Run("unknown zone returns false", func(t *testing.T) {
		if _, ok := parseReset("7am (Nowhere/Fake)", now); ok {
			t.Error("expected not ok")
		}
	})
}

func TestParseLimitMessages(t *testing.T) {
	now := time.Date(2026, 10, 9, 3, 0, 0, 0, time.UTC)

	t.Run("real session limit message", func(t *testing.T) {
		hit := parseLimit("You've hit your session limit · resets 7am (UTC)", now)
		if hit.Weekly {
			t.Error("expected not weekly")
		}
		want := time.Date(2026, 10, 9, 7, 0, 0, 0, time.UTC)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("weekly limit message", func(t *testing.T) {
		hit := parseLimit("You've hit your weekly limit · resets Oct 14, 9am (America/Detroit)", now)
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
		hit := parseLimit("5-hour limit reached · resets 3pm", now)
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
		hit := parseLimit("Claude AI usage limit reached|1760000400", now)
		if hit.Weekly {
			t.Error("expected not weekly")
		}
		want := time.Unix(1760000400, 0)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("weekly epoch form", func(t *testing.T) {
		hit := parseLimit("Claude AI weekly usage limit reached|1760000400", now)
		if !hit.Weekly {
			t.Error("expected weekly")
		}
		want := time.Unix(1760000400, 0)
		if !hit.ResetAt.Equal(want) {
			t.Errorf("ResetAt = %v, want %v", hit.ResetAt, want)
		}
	})

	t.Run("no reset phrase leaves ResetAt zero", func(t *testing.T) {
		hit := parseLimit("You've hit your usage limit", now)
		if !hit.ResetAt.IsZero() {
			t.Errorf("ResetAt = %v, want zero", hit.ResetAt)
		}
		if hit.Weekly {
			t.Error("expected not weekly")
		}
	})
}
