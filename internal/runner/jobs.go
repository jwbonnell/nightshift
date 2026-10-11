package runner

import (
	"fmt"
	"os"
	"strconv"

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
