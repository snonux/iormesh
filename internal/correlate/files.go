package correlate

import (
	"golang.org/x/sys/unix"

	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// creatFlags is what creat(2) is defined as: O_CREAT|O_WRONLY|O_TRUNC.
const creatFlags = 0o1101

// The M2 file surface (docs/implementation.md M2 steps 4–5): path_event
// and name_event enters, and the fd kinds beyond read/write/close. Kind
// sets are switches, not maps: a string switch compiles to a few compares
// and allocates nothing (decision #25: the consumer's per-event cost).

func (c *Correlator) enterPath(r *types.PathEvent) {
	c.advance(r.Time)
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), shape: shapePath, time: r.Time,
		pid: r.Pid, fd: r.Dirfd, path: types.StringValue(r.Pathname[:]),
		pathKnown: r.PathnameStatus != types.PATH_READ_FAILED, flags: int32(r.Flags)}
}

func (c *Correlator) enterName(r *types.NameEvent) {
	c.advance(r.Time)
	c.enters[r.Tid] = enter{name: r.TraceId.Name(), shape: shapeName, time: r.Time,
		pid: r.Pid, fd: r.Olddirfd, newDirfd: r.Newdirfd,
		path: types.StringValue(r.Oldname[:]), newPath: types.StringValue(r.Newname[:]),
		pathKnown: r.OldnameStatus != types.PATH_READ_FAILED, flags: int32(r.Flags)}
}

// pathDetail fills a path kind: the dirfd as fd (none for AT_FDCWD), the
// resolved path, the flags argument where the syscall has one, and the
// effect a successful unlink has on open fds. creat is an open and takes
// that path.
func (c *Correlator) pathDetail(ev *schema.Event, p *procState, en enter, ret int64) bool {
	if en.name == schema.KindCreat {
		en.fd, en.flags = atFDCWD, creatFlags
		return c.openDetail(ev, p, en, ret)
	}
	if en.fd != atFDCWD && en.fd >= 0 {
		ev.FD, ev.FDGen = en.fd, p.use(en.fd)
	}
	if path := p.resolve(en.fd, en.path, en.pathKnown); path != "" {
		ev.Args.Path = &path
		if ret == 0 && removesName(en.name) {
			c.unlinked(path)
		}
	}
	if hasFlagsArg(en.name) {
		flags := uint32(en.flags)
		ev.Args.Flags = &flags
	}
	return true
}

// nameDetail fills a rename, link or symlink: path is the old name, except
// for symlinks, whose first argument is the link's text — stored as the
// link holds it, never resolved —, new_path the new one. A successful
// rename moves the open fds (paths.go).
func (c *Correlator) nameDetail(ev *schema.Event, p *procState, en enter, ret int64) {
	old := en.path
	if en.name != schema.KindSymlink && en.name != schema.KindSymlinkat {
		old = p.resolve(en.fd, en.path, en.pathKnown)
	}
	if old != "" {
		ev.Args.Path = &old
	}
	if newPath := p.resolve(en.newDirfd, en.newPath, false); newPath != "" {
		ev.Args.NewPath = &newPath
		if ret == 0 && old != "" && isRename(en.name) {
			c.renamed(old, newPath)
		}
	}
	if en.name == schema.KindLinkat {
		flags := uint32(en.flags)
		ev.Args.Flags = &flags
	}
}

// fdFileDetail fills the fd kinds of the file surface: bytes for the data
// kinds, the fd's current path for the metadata ones (so an ftruncate
// after a rename names the new file), and the fd lifecycle of dup* and
// fcntl (fdtable.go).
// read and write never get the path: they are the hot kinds, and their fd
// instance already joins to the open that named it (decision #25).
func fdFileDetail(ev *schema.Event, p *procState, en enter, ret int64) {
	switch en.name {
	case schema.KindPread64, schema.KindPwrite64, schema.KindReadv, schema.KindWritev,
		schema.KindPreadv, schema.KindPwritev, schema.KindPreadv2, schema.KindPwritev2:
		if ret > 0 {
			ev.Bytes = uint64(ret)
		}
	case schema.KindFtruncate, schema.KindFallocate, schema.KindFchmod, schema.KindFchown,
		schema.KindFsetxattr, schema.KindFgetxattr, schema.KindFlistxattr,
		schema.KindFremovexattr:
		if fp, ok := p.paths[en.fd]; ok {
			path := fp.path
			ev.Args.Path = &path
		}
	case schema.KindDup, schema.KindDup2:
		// Neither has a flags argument: the copy's close-on-exec is clear.
		dupDetail(p, en.fd, ret, false)
	case schema.KindDup3:
		flags := uint32(en.flags)
		ev.Args.Flags = &flags
		dupDetail(p, en.fd, ret, flags&unix.O_CLOEXEC != 0)
	case schema.KindFcntl:
		fcntlDetail(ev, p, en, ret)
	}
}

// removesName: kinds whose success removes a name (paths.go, unlinked).
func removesName(kind string) bool {
	switch kind {
	case schema.KindUnlink, schema.KindUnlinkat, schema.KindRmdir:
		return true
	}
	return false
}

func isRename(kind string) bool {
	switch kind {
	case schema.KindRename, schema.KindRenameat, schema.KindRenameat2:
		return true
	}
	return false
}

// hasFlagsArg: path kinds whose record carries the syscall's flags
// argument (statx's AT_SYMLINK_NOFOLLOW, the lstat distinction). The
// others' record holds 0 there, which args must not present as a value.
func hasFlagsArg(kind string) bool {
	switch kind {
	case schema.KindStatx, schema.KindNewfstatat, schema.KindUtimensat,
		schema.KindFchownat, schema.KindFchmodat2:
		return true
	}
	return false
}
