package runner

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jwbonnell/nightshift/internal/home"
	"github.com/jwbonnell/nightshift/internal/usagelimit"
)

// limitHit is the result of parsing a rate-limited agent's result message.
type limitHit = usagelimit.Hit

// limitMarker is the on-disk JSON shape of the usage-limit marker file.
type limitMarker = usagelimit.Marker

// limitConfig holds the three NIGHTSHIFT_LIMIT_* settings.
type limitConfig struct {
	Fallback time.Duration
	MaxWait  time.Duration
	MaxHits  int
}

const (
	defaultLimitFallback = usagelimit.DefaultFallback
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
	return sleepCtx(ctx, opts.Sleep, d)
}

// sleepCtx is sleepFor's logic without an Options, for callers (runReview's
// lens rate-limit retry) that don't have one: sleep, if set, is honored
// as-is; otherwise a real timer returns ctx.Err() early on cancellation.
func sleepCtx(ctx context.Context, sleep func(ctx context.Context, d time.Duration) error, d time.Duration) error {
	if sleep != nil {
		return sleep(ctx, d)
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

// limitMarkerPath is the per-state-root marker file recording an active
// Claude usage limit; run/--next/nightshift pr consult it before
// starting new work.
func limitMarkerPath(stateRoot string) string {
	return usagelimit.Path(stateRoot)
}

// writeLimitMarker records hit as the active usage limit for stateRoot, by
// slug by, as of now. A zero hit.ResetAt is stored as now+fallback. An
// existing marker whose reset_at is later than the new one is kept as-is.
func writeLimitMarker(stateRoot string, hit limitHit, now time.Time, by string, fallback time.Duration) error {
	return usagelimit.Write(stateRoot, hit, now, by, fallback)
}

// readActiveLimit reads stateRoot's usage-limit marker, returning nil
// when there is none or its reset_at is not strictly after now (an
// expired or corrupt marker). deleteExpired controls whether an expired
// marker is removed from disk: run/--next pass true; --dry-run passes
// false so it never deletes the marker (Design: "It never deletes the
// marker").
func readActiveLimit(stateRoot string, now time.Time, deleteExpired bool) *limitMarker {
	return usagelimit.ReadActive(stateRoot, now, deleteExpired)
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
		fmt.Fprintf(opts.Stderr, "usage limit active until %s (recorded by %s); not starting\n", resetAt.Local().Format("Mon 15:04"), active.By)
		return StatusRateLimited, true, nil
	}

	fmt.Fprintf(opts.Stdout, "usage limit active; waiting until %s\n", wake.Local().Format("15:04 Mon"))
	if serr := sleepFor(opts, wake.Sub(now)); serr != nil || opts.interrupted() {
		return StatusInterrupted, true, nil
	}
	return "", false, nil
}

// limitCfg returns o.limits with any zero field filled from the defaults —
// needed because tests that build a run via setup() directly (bypassing
// Run(), which calls resolveLimitConfig) never populate o.limits.
func (o Options) limitCfg() limitConfig {
	return fillLimitConfig(o.limits)
}

// fillLimitConfig fills any zero field of c from the package defaults. It's
// limitCfg's logic without an Options, for reviewParams's limits field
// (runReview has no Options to call limitCfg on).
func fillLimitConfig(c limitConfig) limitConfig {
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

// parseLimit extracts a reset time (if any) from a rate-limited agent's
// result message. It never reads anything but msg: limit detection must
// stay confined to the agent's final result object.
func parseLimit(msg string, now time.Time) limitHit {
	return usagelimit.Parse(msg, now)
}

// parseReset parses the free-text phrase following "resets " in a limit
// message, resolving it relative to now. It returns false for anything it
// doesn't recognize.
func parseReset(phrase string, now time.Time) (time.Time, bool) {
	return usagelimit.ParseReset(phrase, now)
}
