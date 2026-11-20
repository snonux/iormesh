package k8s

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockName is the fetch lock in a local store directory. It is a dot
// file, so the build and collect's count skip it like the temporaries.
const lockName = ".fetch.lock"

// ErrBusy: another fetch into the same local directory holds its lock.
var ErrBusy = errors.New("k8s: another fetch into this directory is running")

// lockDir takes dir's fetch lock (wb4 review N9): an exclusive flock on
// dir/.fetch.lock, never waited for — a second collect of the same pod
// fails at once naming the busy directory, and the operator reruns it
// once the first is done (it then moves only what the first left). The
// lock dies with the process, so a killed collect leaves nothing to
// clean; the file itself stays, empty. Unpack's per-name temporaries
// (".<name>.part", replaced when stale) are safe only under this lock.
func lockDir(dir string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w: %s", ErrBusy, dir)
		}
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
