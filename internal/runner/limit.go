package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	_ "time/tzdata"

	"github.com/jwbonnell/nightshift/internal/home"
)

// limitHit is the result of parsing a rate-limited agent's result message.
type limitHit struct {
	Weekly  bool
	ResetAt time.Time // zero when the message gives no usable reset time
}

// limitConfig holds the three NIGHTSHIFT_LIMIT_* settings.
type limitConfig struct {
	Fallback time.Duration
	MaxWait  time.Duration
	MaxHits  int
}

const (
	defaultLimitFallback = 60 * time.Minute
	defaultLimitMaxWait  = 6 * time.Hour
	defaultLimitMaxHits  = 3
)

// resolveLimitConfig resolves the three NIGHTSHIFT_LIMIT_* keys per
// env > config > default.
func resolveLimitConfig(opts Options) (limitConfig, error) {
	cfg, err := home.ParseConfig(home.ConfigPath(opts.Home))
	if err != nil {
		return limitConfig{}, err
	}

	fallback, err := resolveLimitDuration("NIGHTSHIFT_LIMIT_FALLBACK", cfg, defaultLimitFallback)
	if err != nil {
		return limitConfig{}, err
	}
	maxWait, err := resolveLimitDuration("NIGHTSHIFT_LIMIT_MAX_WAIT", cfg, defaultLimitMaxWait)
	if err != nil {
		return limitConfig{}, err
	}
	maxHits, err := resolveLimitInt("NIGHTSHIFT_LIMIT_MAX_HITS", cfg, defaultLimitMaxHits)
	if err != nil {
		return limitConfig{}, err
	}
	return limitConfig{Fallback: fallback, MaxWait: maxWait, MaxHits: maxHits}, nil
}

func resolveLimitDuration(key string, cfg map[string]string, def time.Duration) (time.Duration, error) {
	raw := envConfigDefault(key, cfg, "")
	if raw == "" {
		return def, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s=%q: want a positive duration", key, raw)
	}
	return d, nil
}

func resolveLimitInt(key string, cfg map[string]string, def int) (int, error) {
	raw := envConfigDefault(key, cfg, "")
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s=%q: want a positive integer", key, raw)
	}
	return n, nil
}

// limitCfg returns o.limits with any zero field filled from the defaults —
// needed because tests that build a run via setup() directly (bypassing
// Run(), which calls resolveLimitConfig) never populate o.limits.
func (o Options) limitCfg() limitConfig {
	c := o.limits
	if c.Fallback == 0 {
		c.Fallback = defaultLimitFallback
	}
	if c.MaxWait == 0 {
		c.MaxWait = defaultLimitMaxWait
	}
	if c.MaxHits == 0 {
		c.MaxHits = defaultLimitMaxHits
	}
	return c
}

// epochLimitRe matches the epoch-stamped form, e.g.
// "Claude AI usage limit reached|1760000400".
var epochLimitRe = regexp.MustCompile(`Claude AI (?:weekly |opus )?usage limit reached\|(\d{9,})`)

// parseLimit extracts a reset time (if any) from a rate-limited agent's
// result message. It never reads anything but msg: limit detection must
// stay confined to the agent's final result object.
func parseLimit(msg string, now time.Time) limitHit {
	weekly := strings.Contains(strings.ToLower(msg), "weekly")
	if m := epochLimitRe.FindStringSubmatch(msg); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return limitHit{Weekly: weekly, ResetAt: time.Unix(n, 0)}
		}
	}
	lower := strings.ToLower(msg)
	if i := strings.Index(lower, "resets "); i >= 0 {
		phrase := msg[i+len("resets "):]
		if t, ok := parseReset(phrase, now); ok {
			return limitHit{Weekly: weekly, ResetAt: t}
		}
	}
	return limitHit{Weekly: weekly}
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

// parseReset parses the free-text phrase following "resets " in a limit
// message, resolving it relative to now. It returns false for anything it
// doesn't recognize.
func parseReset(phrase string, now time.Time) (time.Time, bool) {
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
