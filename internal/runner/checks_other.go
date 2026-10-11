//go:build !linux

package runner

import "runtime"

// countHardlinks is a no-op outside Linux: Stat_t.Nlink is not portably
// available, so the clone-objects check just reports why it was skipped.
func countHardlinks(clone string) checkLine {
	return checkLine{"skip", "clone-objects (not supported on " + runtime.GOOS + ")"}
}
