package gitlab

import "testing"

// SetMaxPagesForTest temporarily shrinks the pagination cap so tests don't
// need to generate thousands of fake merge requests to exercise it; it's
// restored automatically at the end of the calling test via t.Cleanup.
func SetMaxPagesForTest(t *testing.T, n int) {
	t.Helper()
	orig := maxPages
	maxPages = n
	t.Cleanup(func() { maxPages = orig })
}
