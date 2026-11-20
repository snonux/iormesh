//go:build !linux

package release

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// openChecked is the portable form for the controller's other platforms
// (macOS), which have no O_PATH: O_NOFOLLOW refuses a final symlink and
// O_NONBLOCK keeps a FIFO from blocking the open; vet then judges the
// opened descriptor before anything is read. Only the controller runs
// here, as its own user — never root on a caller's behalf.
func openChecked(path string, check FileCheck) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("%w: %s is a symlink", ErrUnsafeFile, path)
		}
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = vet(path, info, check)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
