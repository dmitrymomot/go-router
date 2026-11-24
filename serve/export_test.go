package serve

import (
	"testing"
	"time"
)

// ShortenJobWaitReport makes Run name the jobs it waits for every d, for the
// length of the test.
func ShortenJobWaitReport(tb testing.TB, d time.Duration) {
	tb.Helper()
	old := jobWaitReport
	jobWaitReport = d
	tb.Cleanup(func() { jobWaitReport = old })
}
