package query

import (
	"fmt"
	"strconv"
	"strings"

	"iormesh/internal/schema"
)

// maxTracked bounds each piece of state the filters build from earlier
// events (open fds of a matching file, threads a followed connection
// ran on). The events come from servers: a hostile trace opening a
// matching path millions of times without closing must fail the query
// ("narrow the filter"), not exhaust the controller's memory.
const maxTracked = 1 << 20

// fdKey names one fd instance (docs/schema.md: pid, pid_incarnation, fd,
// fd_gen) on one host.
type fdKey struct {
	host   string
	pid    uint32
	pidInc uint64
	fd     int32
	fdGen  uint32
}

// threadKey names one thread of one process incarnation on one host.
type threadKey struct {
	host   string
	pid    uint32
	pidInc uint64
	tid    uint32
}

// span is the wall-clock range of a thread's events on the followed
// connection, both ends inclusive.
type span struct{ first, last uint64 }

// matcher judges events against a filter. It is stateful (the fds a
// matching open returned), so it must see every event of the sources in
// order — the ones other filters reject too.
type matcher struct {
	f Filter
	// apps maps a process node id to its app's pattern (App).
	apps map[string]string
	// methods holds, per session, the method ids whose frame matched.
	methods map[string]map[uint64]bool
	// files are the open fd instances of a matching path.
	files map[fdKey]bool
	// follow holds the threads that used Conn and when (Follow).
	follow map[threadKey]span
}

// newMatcher prepares a matcher for in's sources.
func newMatcher(f Filter, in Input) *matcher {
	m := &matcher{f: f, methods: in.Methods, files: map[fdKey]bool{}}
	if f.App != "" {
		m.apps = appPatterns(in.Graph)
	}
	return m
}

// appPatterns maps every process node of the merged graph to its app's
// pattern, through the app → process incarnation edges.
func appPatterns(g schema.Graph) map[string]string {
	out := map[string]string{}
	for _, e := range g.Edges {
		if e.Type != schema.EdgeIncarnation {
			continue
		}
		// "app:<project>/<pattern>": a project holds no '/'.
		if _, pattern, ok := strings.Cut(strings.TrimPrefix(e.From, "app:"), "/"); ok {
			out[e.To] = pattern
		}
	}
	return out
}

// match reports whether ev, of src, is in the slice. Every predicate
// runs, whatever the others say, so the file state stays complete.
func (m *matcher) match(src *Source, ev *schema.Event) (bool, error) {
	file, err := m.file(ev)
	if err != nil {
		return false, err
	}
	f := m.f
	return file && m.window(ev) &&
		(f.Host == "" || glob(f.Host, src.Host) || glob(f.Host, src.FQDN)) &&
		m.app(ev) && m.thread(ev) && m.conn(ev) && m.method(src, ev), nil
}

// window is the time predicate.
func (m *matcher) window(ev *schema.Event) bool {
	return ev.WallNS >= m.f.From && (!m.f.HasTo || ev.WallNS < m.f.To)
}

// app matches the process's app pattern or its comm.
func (m *matcher) app(ev *schema.Event) bool {
	if m.f.App == "" {
		return true
	}
	if glob(m.f.App, ev.Comm) {
		return true
	}
	p, ok := m.apps[schema.ProcessNodeID(ev.HostID, ev.PID, ev.PIDInc)]
	return ok && glob(m.f.App, p)
}

// thread matches the Java thread name or the decimal tid.
func (m *matcher) thread(ev *schema.Event) bool {
	if m.f.Thread == "" {
		return true
	}
	if ev.Java != nil && ev.Java.Thread != "" && glob(m.f.Thread, ev.Java.Thread) {
		return true
	}
	return ev.TID != 0 && glob(m.f.Thread, strconv.FormatUint(uint64(ev.TID), 10))
}

// conn matches the connection, or with Follow its threads' events
// while they used it.
func (m *matcher) conn(ev *schema.Event) bool {
	switch {
	case m.f.Conn == "" || ev.ConnID == m.f.Conn:
		return true
	case !m.f.Follow:
		return false
	}
	s, ok := m.follow[threadOf(ev)]
	return ok && ev.WallNS >= s.first && ev.WallNS <= s.last
}

// method matches when a frame of the event's Java stack is one of the
// session's matching method ids.
func (m *matcher) method(src *Source, ev *schema.Event) bool {
	if m.f.Method == "" {
		return true
	}
	ids := m.methods[src.Session]
	if ev.Java == nil || len(ids) == 0 {
		return false
	}
	for _, id := range ev.Java.Stack {
		if ids[id] {
			return true
		}
	}
	return false
}

// file matches an event naming a matching path, and the events of the
// fd instance a successful open of one returned; a close ends that fd's
// tracking (after matching it). Only the open kinds start tracking:
// their fd is the one they returned. Any other event naming a path —
// newfstatat, unlinkat, mkdirat — carries its dirfd as fd, and an
// fstat or an AF_UNIX connect an fd it did not create, so tracking
// those would follow a directory or a socket instead of the file. An
// fd with fd_gen 0 predates the trace and is never tracked: its number
// may name anything.
func (m *matcher) file(ev *schema.Event) (bool, error) {
	if m.f.File == "" {
		return true, nil
	}
	k := fdKey{ev.HostID, ev.PID, ev.PIDInc, ev.FD, ev.FDGen}
	named := m.paths(&ev.Args)
	switch {
	case named && opens(ev):
		if len(m.files) >= maxTracked && !m.files[k] {
			return false, fmt.Errorf("query: over %d open fds of -file %q tracked at once: narrow the filter",
				maxTracked, m.f.File)
		}
		m.files[k] = true
	case ev.FD >= 0 && m.files[k]:
		if ev.Event == schema.KindClose {
			delete(m.files, k)
		}
		return true, nil
	}
	return named, nil
}

// paths reports whether the event's path or new path matches File.
func (m *matcher) paths(a *schema.Args) bool {
	return (a.Path != nil && glob(m.f.File, *a.Path)) || (a.NewPath != nil && glob(m.f.File, *a.NewPath))
}

// threadOf is the event's thread.
func threadOf(ev *schema.Event) threadKey {
	return threadKey{ev.HostID, ev.PID, ev.PIDInc, ev.TID}
}

// opens reports whether ev is a successful open of a new fd instance:
// open, openat, openat2 or creat, whose fd is the one it returned.
func opens(ev *schema.Event) bool {
	switch ev.Event {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2, schema.KindCreat:
		return ev.Ret >= 0 && ev.FD >= 0 && int64(ev.FD) == ev.Ret && ev.FDGen > 0
	}
	return false
}
