// Package usagelimit parses and records Claude usage-limit hits. It is
// std-lib only so internal/review (which must never import
// internal/runner) can use it too — see internal/runner/limit.go, which
// calls through this package, and SPEC.md P12.5.
package usagelimit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"
)

// MarkerName is the per-state-root marker file recording an active
// Claude usage limit.
const MarkerName = ".usage-limit"

// DefaultFallback is the reset wait used when a limit message carries no
// parsable reset time.
const DefaultFallback = 60 * time.Minute

// Hit is the result of parsing a rate-limited agent's result message.
type Hit struct {
	Weekly  bool
	ResetAt time.Time // zero when the message gives no usable reset time
}

// Marker is the on-disk JSON shape of the usage-limit marker file.
type Marker struct {
	ResetAt  string `json:"reset_at"`
	Weekly   bool   `json:"weekly"`
	Recorded string `json:"recorded"`
	By       string `json:"by"`
}

// Path returns the marker file path for stateRoot.
func Path(stateRoot string) string {
	return filepath.Join(stateRoot, MarkerName)
}

// readRaw reads and parses the marker file with no expiry handling
// (ReadActive adds that); nil on any read/parse error or if the file
// doesn't exist.
func readRaw(stateRoot string) *Marker {
	data, err := os.ReadFile(Path(stateRoot))
	if err != nil {
		return nil
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

// Write records hit as the active usage limit for stateRoot, by slug by,
// as of now. A zero hit.ResetAt is stored as now+fallback. An existing
// marker whose reset_at is later than the new one is kept as-is.
func Write(stateRoot string, hit Hit, now time.Time, by string, fallback time.Duration) error {
	resetAt := hit.ResetAt
	if resetAt.IsZero() {
		resetAt = now.Add(fallback)
	}

	if existing := readRaw(stateRoot); existing != nil {
		if t, err := time.Parse(time.RFC3339, existing.ResetAt); err == nil && t.After(resetAt) {
			return nil
		}
	}

	m := Marker{
		ResetAt:  resetAt.UTC().Format(time.RFC3339),
		Weekly:   hit.Weekly,
		Recorded: now.UTC().Format(time.RFC3339),
		By:       by,
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := os.MkdirAll(stateRoot, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateRoot, MarkerName+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil {
		os.Remove(tmpPath)
		return werr
	}
	if cerr != nil {
		os.Remove(tmpPath)
		return cerr
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, Path(stateRoot))
}

// ReadActive reads stateRoot's usage-limit marker, returning nil when
// there is none or its reset_at is not strictly after now (an expired or
// corrupt marker). deleteExpired controls whether an expired marker is
// removed from disk: run/--next pass true; --dry-run passes false so it
// never deletes the marker.
func ReadActive(stateRoot string, now time.Time, deleteExpired bool) *Marker {
	m := readRaw(stateRoot)
	if m == nil {
		return nil
	}
	resetAt, err := time.Parse(time.RFC3339, m.ResetAt)
	if err != nil || !resetAt.After(now) {
		if deleteExpired {
			os.Remove(Path(stateRoot))
		}
		return nil
	}
	return m
}

// epochLimitRe matches the epoch-stamped form, e.g.
// "Claude AI usage limit reached|1760000400".
var epochLimitRe = regexp.MustCompile(`Claude AI (?:weekly |opus )?usage limit reached\|(\d{9,})`)

// Parse extracts a reset time (if any) from a rate-limited agent's
// result message. It never reads anything but msg: limit detection must
// stay confined to the agent's final result object.
func Parse(msg string, now time.Time) Hit {
	weekly := strings.Contains(strings.ToLower(msg), "weekly")
	if m := epochLimitRe.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return Hit{Weekly: weekly, ResetAt: time.Unix(n, 0)}
		}
	}
	lower := strings.ToLower(msg)
	if i := strings.Index(lower, "resets "); i >= 0 {
		phrase := msg[i+len("resets "):]
		if t, ok := ParseReset(phrase, now); ok {
			return Hit{Weekly: weekly, ResetAt: t}
		}
	}
	return Hit{Weekly: weekly}
}

// timeOfDayRe matches a bare time of day: "7am", "7:30am", "15:00".
var timeOfDayRe = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s*(am|pm)$`)
var time24Re = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

// dateTimeRe matches a date plus a time of day, e.g. "Oct 14, 9am" or
// "Oct 14 at 9am".
var dateTimeRe = regexp.MustCompile(`(?i)^([A-Za-z]{3})[A-Za-z]*\.?\s+(\d{1,2}),?\s+(?:at\s+)?(.+)$`)

// zoneRe peels an optional trailing zone annotation, e.g. "(UTC)" or
// "(America/Detroit)".
var zoneRe = regexp.MustCompile(`\s*\(([^)]+)\)$`)

// ParseReset parses the free-text phrase following "resets " in a limit
// message, resolving it relative to now. It returns false for anything it
// doesn't recognize.
func ParseReset(phrase string, now time.Time) (time.Time, bool) {
	phrase = strings.TrimSpace(phrase)
	phrase = strings.TrimSuffix(phrase, ".")
	phrase = strings.TrimSpace(phrase)

	loc := time.Local
	if m := zoneRe.FindStringSubmatch(phrase); m != nil {
		zone := m[1]
		if strings.EqualFold(zone, "UTC") {
			loc = time.UTC
		} else {
			l, err := time.LoadLocation(zone)
			if err != nil {
				return time.Time{}, false
			}
			loc = l
		}
		phrase = strings.TrimSpace(phrase[:len(phrase)-len(m[0])])
	}

	if dm := dateTimeRe.FindStringSubmatch(phrase); dm != nil {
		month := parseMonth(dm[1])
		if month == 0 {
			return time.Time{}, false
		}
		day, err := strconv.Atoi(dm[2])
		if err != nil {
			return time.Time{}, false
		}
		hour, minute, ok := parseTimeOfDay(dm[3])
		if !ok {
			return time.Time{}, false
		}
		nowInLoc := now.In(loc)
		year := nowInLoc.Year()
		t := time.Date(year, month, day, hour, minute, 0, 0, loc)
		if t.Before(now.Add(-7 * 24 * time.Hour)) {
			t = time.Date(year+1, month, day, hour, minute, 0, 0, loc)
		}
		return t, true
	}

	hour, minute, ok := parseTimeOfDay(phrase)
	if !ok {
		return time.Time{}, false
	}
	nowInLoc := now.In(loc)
	t := time.Date(nowInLoc.Year(), nowInLoc.Month(), nowInLoc.Day(), hour, minute, 0, 0, loc)
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t, true
}

// parseTimeOfDay parses "7am", "7:30am", or "15:00" into hour/minute.
func parseTimeOfDay(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if m := timeOfDayRe.FindStringSubmatch(s); m != nil {
		hour, err := strconv.Atoi(m[1])
		if err != nil || hour < 1 || hour > 12 {
			return 0, 0, false
		}
		minute := 0
		if m[2] != "" {
			minute, err = strconv.Atoi(m[2])
			if err != nil || minute < 0 || minute > 59 {
				return 0, 0, false
			}
		}
		if strings.EqualFold(m[3], "pm") && hour != 12 {
			hour += 12
		} else if strings.EqualFold(m[3], "am") && hour == 12 {
			hour = 0
		}
		return hour, minute, true
	}
	if m := time24Re.FindStringSubmatch(s); m != nil {
		hour, err := strconv.Atoi(m[1])
		if err != nil || hour < 0 || hour > 23 {
			return 0, 0, false
		}
		minute, err := strconv.Atoi(m[2])
		if err != nil || minute < 0 || minute > 59 {
			return 0, 0, false
		}
		return hour, minute, true
	}
	return 0, 0, false
}

var months = map[string]time.Month{
	"jan": time.January, "feb": time.February, "mar": time.March,
	"apr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "aug": time.August, "sep": time.September,
	"oct": time.October, "nov": time.November, "dec": time.December,
}

// parseMonth parses a 3+ letter month abbreviation, returning 0 if
// unrecognized.
func parseMonth(s string) time.Month {
	if len(s) < 3 {
		return 0
	}
	return months[strings.ToLower(s[:3])]
}
