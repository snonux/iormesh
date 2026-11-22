//go:build linux

package query

import (
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// TestErrnoTableMatchesTracer: the controller's errno table (it builds
// for darwin too, whose numbers differ) names every number exactly as
// the tracer's unix.ErrnoName does (internal/correlate), so -errno N
// selects the events the tracer recorded for errno N.
func TestErrnoTableMatchesTracer(t *testing.T) {
	for n := 1; n < 4096; n++ {
		want := unix.ErrnoName(syscall.Errno(n))
		var got string
		if n < len(linuxErrnos) {
			got = linuxErrnos[n]
		}
		if got != want {
			t.Errorf("errno %d: table %q, unix.ErrnoName %q", n, got, want)
		}
	}
}
