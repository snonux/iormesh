package federate

import (
	"maps"
	"slices"
	"strings"

	"iormesh/internal/schema"
)

// The rebuild's fd table (correlate's fdtable.go and paths.go, reduced to
// what the graph needs): each open fd's file name, for the opened edges a
// rename adds, and its close-on-exec state, for what an exec closes.

// file follows the open fds and adds the opened edges.
func (g *graphRebuilder) file(proc string, ev *schema.Event) {
	k := fdKey{ev.PID, ev.PIDInc, ev.FD}
	switch ev.Event {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2, schema.KindCreat:
		if ev.Ret >= 0 && ev.Args.Path != nil {
			g.opened(proc, ev.HostID, *ev.Args.Path)
			g.paths[k] = *ev.Args.Path
			g.cloexec[k] = ev.Args.Flags != nil && *ev.Args.Flags&oCloexec != 0
		}
	case schema.KindDup, schema.KindDup2, schema.KindDup3:
		g.dup(k, ev, ev.Event == schema.KindDup3 && ev.Args.Flags != nil && *ev.Args.Flags&oCloexec != 0)
	case schema.KindFcntl:
		g.fcntl(k, ev)
	case schema.KindExec:
		g.exec(ev.PID, ev.PIDInc)
	case schema.KindClose:
		// Linux releases the fd even when close fails, except with EBADF:
		// then there was nothing to release (correlate's close).
		if ev.Ret != -eBADF {
			g.forget(k)
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
		g.created(k, ev)
	}
}

// created forgets what the numbers an fd-creating call without a path
// returned held before (accept, pipe, socketpair, epoll_create): a new
// instance, so a later rename must not move it (correlate's create).
func (g *graphRebuilder) created(k fdKey, ev *schema.Event) {
	if ev.Ret < 0 || ev.FD < 0 {
		return
	}
	switch ev.Event {
	case schema.KindAccept, schema.KindAccept4, schema.KindEpollCreate, schema.KindEpollCreate1:
		g.forget(k)
	case schema.KindPipe, schema.KindPipe2, schema.KindSocketpair:
		g.forget(k)
		if ev.Args.FD2 != nil {
			g.forget(fdKey{k.pid, k.inc, *ev.Args.FD2})
		}
	}
}

// fcntl applies the commands that change or reveal the fd table
// (correlate's fcntlDetail): the F_DUPFD forms copy, F_SETFD sets the
// close-on-exec state (its argument is args.flags, task ze4) and F_GETFD
// reports it (its return value).
func (g *graphRebuilder) fcntl(k fdKey, ev *schema.Event) {
	if ev.Args.Cmd == nil || ev.Ret < 0 {
		return
	}
	switch *ev.Args.Cmd {
	case fDupFD, fDupFDCloexec:
		g.dup(k, ev, *ev.Args.Cmd == fDupFDCloexec)
	case fSetFD:
		if ev.Args.Flags != nil {
			g.cloexec[k] = *ev.Args.Flags&fdCloexec != 0
		}
	case fGetFD:
		g.cloexec[k] = ev.Ret&fdCloexec != 0
	}
}

// forget drops one fd instance.
func (g *graphRebuilder) forget(k fdKey) {
	delete(g.paths, k)
	delete(g.cloexec, k)
}

// dup makes the returned fd a copy of the event's fd (docs/schema.md
// "fd lifecycle"): same file, its own close-on-exec state. Whatever the
// new number held before is replaced; a dup2 onto itself changes
// nothing.
func (g *graphRebuilder) dup(old fdKey, ev *schema.Event, cloexec bool) {
	if ev.Ret < 0 || int32(ev.Ret) == old.fd {
		return
	}
	nk := fdKey{old.pid, old.inc, int32(ev.Ret)}
	g.forget(nk)
	g.cloexec[nk] = cloexec
	if p, ok := g.paths[old]; ok {
		g.paths[nk] = p
	}
}

// exec closes what the kernel closed (correlate's closeOnExec and
// survivesExec): the fds whose close-on-exec state is set, or unknown
// unless stdio. A rename after the exec no longer moves them.
func (g *graphRebuilder) exec(pid uint32, inc uint64) {
	known := maps.Clone(g.cloexec)
	g.dropFDs(pid, inc, func(k fdKey) bool {
		set, ok := known[k]
		if ok {
			return set
		}
		return k.fd < 0 || k.fd > 2
	})
}

func (g *graphRebuilder) opened(proc, hostID, path string) {
	file := schema.FileNodeID(hostID, path)
	g.nodes[file] = schema.Node{Type: schema.NodeFile, ID: file}
	g.edge(schema.Edge{Type: schema.EdgeOpened, From: proc, To: file})
}

// unlinked forgets the fds of a removed name: a later rename onto the
// same name must not move them.
func (g *graphRebuilder) unlinked(name string) {
	if !strings.HasPrefix(name, "/") {
		return
	}
	maps.DeleteFunc(g.paths, func(_ fdKey, p string) bool { return p == name })
}

// renamed moves every open fd at or below old to new, and gives its
// process an opened edge to the new name (correlate's renamed). Relative
// names cannot be matched against the absolute paths fds keep.
func (g *graphRebuilder) renamed(hostID, old, new string) {
	if old == new || !strings.HasPrefix(old, "/") || !strings.HasPrefix(new, "/") {
		return
	}
	g.unlinked(new)
	for _, k := range slices.SortedFunc(maps.Keys(g.paths), compareFD) {
		p := g.paths[k]
		if !below(p, old) {
			continue
		}
		moved := new + p[len(old):]
		g.paths[k] = moved
		g.opened(schema.ProcessNodeID(hostID, k.pid, k.inc), hostID, moved)
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
