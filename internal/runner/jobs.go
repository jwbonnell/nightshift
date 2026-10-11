package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jwbonnell/nightshift/internal/home"
)

// resolveJobsCount resolves the --jobs effective count per D-i: flag >
// NIGHTSHIFT_JOBS env > NIGHTSHIFT_JOBS config > default 1, each clamped to
// maxConcurrent (min(N, max_concurrent)). opts.Jobs is the flag value, 0
// meaning unset — cmd/nightshift validates the raw flag (N >= 1, only with
// --next) before it ever reaches here, so only env/config need their own
// validation.
func resolveJobsCount(opts Options, maxConcurrent int) (int, string, error) {
	parse := func(raw, errFmt string) (int, error) {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return 0, fmt.Errorf(errFmt, raw)
		}
		return n, nil
	}

	if opts.Jobs > 0 {
		return min(opts.Jobs, maxConcurrent), "flag", nil
	}
	if env := os.Getenv("NIGHTSHIFT_JOBS"); env != "" {
		n, err := parse(env, "NIGHTSHIFT_JOBS=%q: must be an integer >= 1")
		if err != nil {
			return 0, "", err
		}
		return min(n, maxConcurrent), "env", nil
	}
	cfg, err := home.ParseConfig(home.ConfigPath(opts.Home))
	if err != nil {
		return 0, "", err
	}
	if val := cfg["NIGHTSHIFT_JOBS"]; val != "" {
		n, err := parse(val, "NIGHTSHIFT_JOBS=%q: must be an integer >= 1")
		if err != nil {
			return 0, "", err
		}
		return min(n, maxConcurrent), "config", nil
	}
	return 1, "default", nil
}

// thenDep is one ready spec that `--jobs --dry-run` reports as starting in
// a later round: slug is the dependent, after the subset of its
// depends_on that are both unmet and among the specs about to start.
type thenDep struct {
	slug  string
	after []string
}

// jobsDryRunPlan computes `run --next --jobs N --dry-run`'s plan: starting
// is selectNextCandidates's oldest-first eligible-now specs, capped at
// jobs; then is every other ready spec whose unmet dependencies are all
// found in starting (so it would be offered in the round right after).
// Specs with unmet dependencies not fully covered by starting (e.g. they
// also wait on something still running, or something not yet started)
// aren't reported — the spec only promises "after" lines for dependents
// that are exactly one round away.
func jobsDryRunPlan(opts Options, jobs int) (starting []string, then []thenDep, err error) {
	cands, _, err := selectNextCandidates(opts)
	if err != nil {
		return nil, nil, err
	}
	n := jobs
	if n > len(cands) {
		n = len(cands)
	}
	for i := 0; i < n; i++ {
		starting = append(starting, cands[i].slug)
	}
	startingSet := make(map[string]bool, len(starting))
	for _, s := range starting {
		startingSet[s] = true
	}

	readyDir := home.ReadyDir(opts.Home)
	entries, rerr := os.ReadDir(readyDir)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return starting, nil, nil
		}
		return nil, nil, rerr
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		slug := slugFromFilename(e.Name())
		if startingSet[slug] {
			continue
		}
		data, derr := os.ReadFile(filepath.Join(readyDir, e.Name()))
		if derr != nil {
			continue
		}
		deps, perr := parseDependsOn(home.ParseFrontmatter(data)["depends_on"])
		if perr != nil || len(deps) == 0 {
			continue
		}
		var after []string
		coveredByStarting := true
		for _, d := range deps {
			if depState(opts, d) == "complete" {
				continue
			}
			if !startingSet[d] {
				coveredByStarting = false
				break
			}
			after = append(after, d)
		}
		if coveredByStarting && len(after) > 0 {
			then = append(then, thenDep{slug: slug, after: after})
		}
	}
	sort.Slice(then, func(i, j int) bool { return then[i].slug < then[j].slug })
	return starting, then, nil
}

// printJobsDryRun prints `--jobs --dry-run`'s plan: the jobs/max_concurrent
// line, the specs that would start now, and a "then" line for each
// dependent one round away. It spawns and creates nothing.
func printJobsDryRun(opts Options, jobs, maxConcurrent int) error {
	starting, then, err := jobsDryRunPlan(opts, jobs)
	if err != nil {
		return err
	}
	fmt.Fprintf(opts.Stdout, "jobs: %d (max_concurrent %d)\n", jobs, maxConcurrent)
	fmt.Fprintf(opts.Stdout, "would start: %s\n", strings.Join(starting, ", "))
	for _, t := range then {
		fmt.Fprintf(opts.Stdout, "then: %s (after %s)\n", t.slug, strings.Join(t.after, ", "))
	}
	return nil
}
