package federate

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// Ownership checks for the controller's private state: the registry
// (registry.go) and the ssh control sockets (ssh.go). Both decide where
// the operator's key goes, so a file or directory someone else owns or
// can write is refused rather than repaired — repairing would bless what
// the other user may already have put there.

// checkOwned requires info (of path) to belong to this user and to have
// none of the mask's permission bits; sentinel classifies the error.
func checkOwned(info fs.FileInfo, path string, mask fs.FileMode, sentinel error) error {
	if perm := info.Mode().Perm(); perm&mask != 0 {
		return fmt.Errorf("%w: %s has mode %v (no %v bits allowed)", sentinel, path, perm, mask)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%w: %s is owned by uid %d", sentinel, path, st.Uid)
	}
	return nil
}

// checkPrivateDir requires dir to be a real directory (not a symlink),
// owned by this user, with no group or other permissions: the control
// socket directory, where a socket is a live, authenticated session.
func checkPrivateDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("ssh control dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrTransport, dir)
	}
	return checkOwned(info, dir, 0o077, ErrTransport)
}

// checkRegistryDir requires the registry's directory to be this user's and
// writable by nobody else: whoever can write the directory can rename a
// file of their own over servers.yaml. Read access and a symlinked
// directory (dotfile managers) are fine, so Stat follows links and only
// the write bits count.
func checkRegistryDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("registry dir: %w", err)
	}
	return checkOwned(info, dir, 0o022, ErrUnsafeFile)
}
