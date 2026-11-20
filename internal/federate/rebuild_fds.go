package federate

import (
	"maps"
	"strings"

	"iormesh/internal/schema"
)

// The rebuild's fd tables (correlate's fdtable.go and paths.go, reduced
// to what the graph needs), one per pid (procFDs). Forks and execs touch
// only their own pid's table; renames and unlinks walk every table, as
// the correlator's do.

// table is the fd table of ev's pid, created on its first event (the
// correlator's proc: the pid's state stays until a fork or attach
// replaces it).
func (g *graphRebuilder) table(ev *schema.Event) *procFDs {
	t := g.procs[ev.PID]
	if t == nil {
		t = &procFDs{inc: ev.PIDInc, fds: map[int32]fdState{}}
		g.procs[ev.PID] = t
	}
	return t
}

// file follows the open fds and adds the opened edges.
func (g *graphRebuilder) file(proc string, ev *schema.Event) {
	switch ev.Event {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2, schema.KindCreat:
		if ev.Ret >= 0 && ev.Args.Path != nil {
			g.opened(proc, ev.HostID, *ev.Args.Path)
			g.table(ev).fds[ev.FD] = fdState{path: *ev.Args.Path, hasPath: true, known: true,
				cloexec: ev.Args.Flags != nil && *ev.Args.Flags&oCloexec != 0}
		}
	case schema.KindDup, schema.KindDup2, schema.KindDup3:
		g.table(ev).dup(ev, ev.Event == schema.KindDup3 && ev.Args.Flags != nil && *ev.Args.Flags&oCloexec != 0)
	case schema.KindFcntl:
		g.table(ev).fcntl(ev)
	case schema.KindExec:
		g.table(ev).exec()
	case schema.KindClose:
		// Linux releases the fd even when close fails, except with EBADF:
		// then there was nothing to release (correlate's close).
		if ev.Ret != -eBADF {
			delete(g.table(ev).fds, ev.FD)
		}
	case schema.KindUnlink, schema.KindUnlinkat, schema.KindRmdir:
		if ev.Ret == 0 && ev.Args.Path != nil {
			g.unlinked(trimSlash(*ev.Args.Path))
		}
	case schema.KindRename, schema.KindRenameat, schema.KindRenameat2:
		if ev.Ret == 0 && ev.Args.Path != nil && ev.Args.NewPath != nil {
			g.renamed(ev.HostID, trimSlash(*ev.Args.Path), trimSlash(*ev.Args.NewPath))
		}
	default:
		g.created(ev)
	}
}

// created forgets what the numbers an fd-creating call without a path
// returned held before (accept, pipe, socketpair, epoll_create): a new
// instance, so a later rename must not move it (correlate's create).
func (g *graphRebuilder) created(ev *schema.Event) {
	if ev.Ret < 0 || ev.FD < 0 {
		return
	}
	switch ev.Event {
	case schema.KindAccept, schema.KindAccept4, schema.KindEpollCreate, schema.KindEpollCreate1:
		delete(g.table(ev).fds, ev.FD)
	case schema.KindPipe, schema.KindPipe2, schema.KindSocketpair:
		t := g.table(ev)
		delete(t.fds, ev.FD)
		if ev.Args.FD2 != nil {
			delete(t.fds, *ev.Args.FD2)
		}
	}
}

// fcntl applies the commands that change or reveal the fd table
// (correlate's fcntlDetail): the F_DUPFD forms copy, F_SETFD sets the
// close-on-exec state (its argument is args.flags, task ze4) and F_GETFD
// reports it (its return value).
func (t *procFDs) fcntl(ev *schema.Event) {
	if ev.Args.Cmd == nil || ev.Ret < 0 {
		return
	}
	switch *ev.Args.Cmd {
	case fDupFD, fDupFDCloexec:
		t.dup(ev, *ev.Args.Cmd == fDupFDCloexec)
	case fSetFD:
		if ev.Args.Flags != nil {
			t.setCloexec(ev.FD, *ev.Args.Flags&fdCloexec != 0)
		}
	case fGetFD:
		t.setCloexec(ev.FD, ev.Ret&fdCloexec != 0)
	}
}

// setCloexec records fd's close-on-exec state, keeping its path.
func (t *procFDs) setCloexec(fd int32, set bool) {
	st := t.fds[fd]
	st.cloexec, st.known = set, true
	t.fds[fd] = st
}

// dup makes the returned fd a copy of the event's fd (docs/schema.md
// "fd lifecycle"): same file, its own close-on-exec state. Whatever the
// new number held before is replaced; a dup2 onto itself changes
// nothing.
func (t *procFDs) dup(ev *schema.Event, cloexec bool) {
	if ev.Ret < 0 || int32(ev.Ret) == ev.FD {
		return
	}
	old := t.fds[ev.FD]
	t.fds[int32(ev.Ret)] = fdState{path: old.path, hasPath: old.hasPath, cloexec: cloexec, known: true}
}

// exec closes what the kernel closed (correlate's closeOnExec and
// survivesExec): the fds whose close-on-exec state is set, or unknown
// unless stdio. A rename after the exec no longer moves them.
func (t *procFDs) exec() {
	maps.DeleteFunc(t.fds, func(fd int32, st fdState) bool {
		if st.known {
			return st.cloexec
		}
		return fd < 0 || fd > 2
	})
}

func (g *graphRebuilder) opened(proc, hostID, path string) {
	file := schema.FileNodeID(hostID, path)
	g.nodes[file] = schema.Node{Type: schema.NodeFile, ID: file}
	g.edge(schema.Edge{Type: schema.EdgeOpened, From: proc, To: file})
}

// unlinked forgets the name of the fds of a removed name: a later rename
// onto the same name must not move them (correlate's gone fds).
func (g *graphRebuilder) unlinked(name string) {
	if !strings.HasPrefix(name, "/") {
		return
	}
	for _, t := range g.procs {
		for fd, st := range t.fds {
			if st.hasPath && st.path == name {
				st.hasPath = false
				t.fds[fd] = st
			}
		}
	}
}

// renamed moves every open fd at or below old to new, and gives its
// process an opened edge to the new name (correlate's renamed): every
// pid's current table, under the incarnation the table belongs to.
// Relative names cannot be matched against the absolute paths fds keep.
func (g *graphRebuilder) renamed(hostID, old, new string) {
	if old == new || !strings.HasPrefix(old, "/") || !strings.HasPrefix(new, "/") {
		return
	}
	g.unlinked(new)
	for pid, t := range g.procs {
		for fd, st := range t.fds {
			if !st.hasPath || !below(st.path, old) {
				continue
			}
			st.path = new + st.path[len(old):]
			t.fds[fd] = st
			g.opened(schema.ProcessNodeID(hostID, pid, t.inc), hostID, st.path)
		}
	}
}

// below: path is name or lies under it (correlate's below).
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
