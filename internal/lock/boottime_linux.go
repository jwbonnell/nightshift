//go:build linux

package lock

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// hostBootTime reads the system boot time (seconds since the Unix epoch)
// from /proc/stat's "btime" line.
func hostBootTime() (int64, bool) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "btime ") {
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "btime ")), 10, 64)
			if err != nil {
				return 0, false
			}
			return v, true
		}
	}
	return 0, false
}
