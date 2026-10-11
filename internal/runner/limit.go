package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// sleepFor waits for d, honoring opts.Sleep if injected; otherwise it runs
// a real timer that returns ctx.Err() early if opts.Ctx is cancelled. A nil
// opts.Ctx defaults to context.Background(), matching Options.interrupted.
func sleepFor(opts Options, d time.Duration) error {
	ctx := opts.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Sleep != nil {
		return opts.Sleep(ctx, d)
	}
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// usageLimitMarkerName is the per-state-root marker file recording an
// active Claude usage limit; run/--next/nightshift pr consult it (Task 5)
// before starting new work.
const usageLimitMarkerName = ".usage-limit"

// limitMarker is the on-disk JSON shape of the usage-limit marker file.
type limitMarker struct {
	ResetAt  string `json:"reset_at"`
	Weekly   bool   `json:"weekly"`
	Recorded string `json:"recorded"`
	By       string `json:"by"`
}

func limitMarkerPath(stateRoot string) string {
	return filepath.Join(stateRoot, usageLimitMarkerName)
}

// readLimitMarkerRaw reads and parses the marker file with no expiry
// handling (readActiveLimit, Task 5, adds that); nil on any read/parse
// error or if the file doesn't exist.
func readLimitMarkerRaw(stateRoot string) *limitMarker {
	data, err := os.ReadFile(limitMarkerPath(stateRoot))
	if err != nil {
		return nil
	}
	var m limitMarker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	return &m
}

// writeLimitMarker records hit as the active usage limit for stateRoot, by
// slug by, as of now. A zero hit.ResetAt is stored as now+fallback. An
// existing marker whose reset_at is later than the new one is kept as-is.
func writeLimitMarker(stateRoot string, hit limitHit, now time.Time, by string, fallback time.Duration) error {
	resetAt := hit.ResetAt
	if resetAt.IsZero() {
		resetAt = now.Add(fallback)
	}

	if existing := readLimitMarkerRaw(stateRoot); existing != nil {
		if t, err := time.Parse(time.RFC3339, existing.ResetAt); err == nil && t.After(resetAt) {
			return nil
		}
	}

	m := limitMarker{
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
	tmp, err := os.CreateTemp(stateRoot, usageLimitMarkerName+".tmp-*")
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
	return os.Rename(tmpPath, limitMarkerPath(stateRoot))
}

// readActiveLimit reads stateRoot's usage-limit marker, returning nil
// when there is none or its reset_at is not strictly after now (an
// expired or corrupt marker). deleteExpired controls whether an expired
// marker is removed from disk: run/--next pass true; --dry-run passes
// false so it never deletes the marker (Design: "It never deletes the
// marker").
func readActiveLimit(stateRoot string, now time.Time, deleteExpired bool) *limitMarker {
	m := readLimitMarkerRaw(stateRoot)
	if m == nil {
		return nil
	}
	resetAt, err := time.Parse(time.RFC3339, m.ResetAt)
	if err != nil || !resetAt.After(now) {
		if deleteExpired {
			os.Remove(limitMarkerPath(stateRoot))
		}
		return nil
	}
	return m
}

// limitWake is the time to wait until for a limit with the given reset
// (hit.ResetAt or a stored marker's reset_at): resetAt+2m, or now+fallback
// when resetAt is zero (an unparsable message).
func limitWake(resetAt, now time.Time, fallback time.Duration) time.Time {
	if resetAt.IsZero() {
		return now.Add(fallback)
	}
	return resetAt.Add(2 * time.Minute)
}

// limitWaitExceedsWindow reports whether waking at wake falls outside the
// run's --until deadline (if any) or, with no deadline, more than maxWait
// away from now.
func limitWaitExceedsWindow(wake, now, deadline time.Time, maxWait time.Duration) bool {
	if !deadline.IsZero() {
		return !wake.Before(deadline)
	}
	return wake.Sub(now) > maxWait
}

// checkActiveLimit consults the usage-limit marker before run/--next start
// new work: "read it after taking their locks and before creating
// sandboxes." handled is true when the caller should return (status, err)
// immediately without creating a sandbox; handled is false when there is
// no active limit, or an active one was waited out and the caller should
// proceed as normal.
func checkActiveLimit(opts Options) (status Status, handled bool, err error) {
	now := opts.clockFn()()
	active := readActiveLimit(opts.StateRoot, now, true)
	if active == nil {
		return "", false, nil
	}

	// readActiveLimit only returns markers whose reset_at parsed and is
	// strictly after now, so this parse cannot fail.
	resetAt, _ := time.Parse(time.RFC3339, active.ResetAt)
	deadline := parseUntil(opts.Until, now)
	cfg := opts.limitCfg()
	wake := limitWake(resetAt, now, cfg.Fallback)

	if limitWaitExceedsWindow(wake, now, deadline, cfg.MaxWait) {
		fmt.Fprintf(opts.Stderr, "usage limit active until %s (recorded by %s); not starting\n", resetAt.Format("Mon 15:04"), active.By)
		return StatusRateLimited, true, nil
	}

	fmt.Fprintf(opts.Stdout, "usage limit active; waiting until %s\n", wake.Format("15:04 Mon"))
	if serr := sleepFor(opts, wake.Sub(now)); serr != nil || opts.interrupted() {
		return StatusInterrupted, true, nil
	}
	return "", false, nil
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
