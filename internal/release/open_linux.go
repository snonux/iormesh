package release

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// openChecked opens path for reading only after vetting it. The first
// open is O_PATH|O_NOFOLLOW: it touches no device driver, cannot block on
// a FIFO and, on a symlink, yields the link itself, which vet refuses.
// Only a file that passed vet on that descriptor is reopened for reading,
// through /proc/self/fd — the same inode, not the path again — and the
// reopened file is compared with the vetted one as a last guard.
func openChecked(path string, check FileCheck) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	pathFile := os.NewFile(uintptr(fd), path)
	defer func() { _ = pathFile.Close() }()
	info, err := pathFile.Stat()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := vet(path, info, check); err != nil {
		return nil, err
	}
	f, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(fd), os.O_RDONLY|unix.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("%s: reopen: %w", path, err)
	}
	reopened, err := f.Stat()
	if err != nil || !os.SameFile(info, reopened) {
		_ = f.Close()
		return nil, fmt.Errorf("%w: %s changed while it was opened", ErrUnsafeFile, path)
	}
	return f, nil
}
