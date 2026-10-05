//go:build !linux

package lock

// hostBootTime has no implementation outside Linux; staleness then relies
// on PID liveness alone.
func hostBootTime() (int64, bool) {
	return 0, false
}
