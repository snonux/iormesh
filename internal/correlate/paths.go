package correlate

import "strings"

// atFDCWD is AT_FDCWD: an *at call's "relative to the working directory".
const atFDCWD = -100

// fdPath is what the correlator knows of an fd instance's file name (M2
// step 5, rename-aware paths).
//
// It follows the file by name, not by inode. A rename record carries the
// two names but no inode, and a directory rename moves every file below
// it — each its own inode — so matching by inode alone would miss exactly
// the move PLAN.md asks to follow. Instead a rename rewrites every open
// fd whose path is the old name or lies below it, in every traced process
// of the host (the rename is filesystem-wide). What name matching could
// get wrong is a name that came to stand for another file: unlink marks
// the fds of the removed name gone, and so does a rename onto an existing
// name for the fds of the file it replaced, so a later file reusing the
// name never drags them along.
type fdPath struct {
	path string
	// gone: the name was unlinked or replaced while the fd stayed open.
	// The fd keeps naming its file as it was opened, but no later rename
	// of that name concerns it.
	gone bool
}

// resolve returns the path a call names: path itself when absolute;
// relative to dirfd's known path when the call is dirfd-relative; the
// dirfd's own path for an empty one (AT_EMPTY_PATH, utimensat's NULL
// path), when known is true — known false means the read failed, and an
// empty path then says nothing. A relative path against AT_FDCWD or an
// fd the trace never named stays as the call gave it: the working
// directory is not tracked, and guessing it would put files where they
// are not.
func (p *procState) resolve(dirfd int32, path string, known bool) string {
	if strings.HasPrefix(path, "/") || dirfd == atFDCWD || dirfd < 0 {
		return path
	}
	dir, ok := p.paths[dirfd]
	switch {
	case !ok:
		return path
	case path == "":
		if known {
			return dir.path
		}
		return ""
	case strings.HasSuffix(dir.path, "/"):
		return dir.path + path
	}
	return dir.path + "/" + path
}

// setPath records the path of a new fd instance. Only absolute paths are
// kept: a relative one cannot resolve later calls or follow renames.
func (p *procState) setPath(fd int32, path string) {
	if strings.HasPrefix(path, "/") {
		p.paths[fd] = fdPath{path: path}
	}
}

// below reports whether path is name or lies inside directory name.
func below(path, name string) bool {
	return path == name ||
		(len(path) > len(name) && strings.HasPrefix(path, name) && path[len(name)] == '/')
}

// trimSlash drops a trailing slash ("dir/" names dir) except of "/".
func trimSlash(path string) string {
	if len(path) > 1 {
		return strings.TrimSuffix(path, "/")
	}
	return path
}

// unlinked marks every fd of the removed name gone. rmdir only removes an
// empty directory, so the exact name is all an unlink can take away.
func (c *Correlator) unlinked(name string) {
	name = trimSlash(name)
	if !strings.HasPrefix(name, "/") {
		return
	}
	for _, p := range c.procs {
		for fd, fp := range p.paths {
			if !fp.gone && fp.path == name {
				p.paths[fd] = fdPath{path: fp.path, gone: true}
			}
		}
	}
}

// renamed applies a successful rename to every traced process's fd paths:
// the file that new named before is replaced (its fds are gone, see
// fdPath), then every fd at or below old moves to the same place below
// new, and its process gets an opened edge to the new name — the process
// holds that file open under it, which the write after the rename in
// file-lifecycle proves. Relative names cannot be matched against the
// absolute paths fds keep, so they change nothing.
func (c *Correlator) renamed(old, new string) {
	old, new = trimSlash(old), trimSlash(new)
	if old == new || !strings.HasPrefix(old, "/") || !strings.HasPrefix(new, "/") {
		return
	}
	c.unlinked(new)
	for pid, p := range c.procs {
		for fd, fp := range p.paths {
			if fp.gone || !below(fp.path, old) {
				continue
			}
			moved := new + fp.path[len(old):]
			p.paths[fd] = fdPath{path: moved}
			c.graph.opened(pid, p.inc, moved)
		}
	}
}
