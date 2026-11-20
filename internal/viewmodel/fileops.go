package viewmodel

import (
	"cmp"
	"maps"
	"slices"
	"sort"
	"strconv"

	"iormesh/internal/schema"
)

// oCreat is O_CREAT: an open that may create its file says "create".
const oCreat = 0o100

// fileVerb names what a file kind does to the path it names (args.path,
// and args.new_path for the two-path kinds); fdVerb what an fd kind
// does to the file its fd was opened from. Kinds in neither — stats,
// readlink, xattr reads, close — change nothing and read no data, so a
// thread's story leaves them out. Every name here is metadata; no
// payload is ever read.
var (
	fileVerb = map[string]string{
		schema.KindOpen: "open", schema.KindOpenat: "open", schema.KindOpenat2: "open",
		schema.KindCreat: "create", schema.KindMkdir: "mkdir", schema.KindMkdirat: "mkdir",
		schema.KindMknod: "mknod", schema.KindMknodat: "mknod", schema.KindRmdir: "rmdir",
		schema.KindUnlink: "unlink", schema.KindUnlinkat: "unlink",
		schema.KindRename: "rename", schema.KindRenameat: "rename", schema.KindRenameat2: "rename",
		schema.KindLink: "link", schema.KindLinkat: "link",
		schema.KindSymlink: "symlink", schema.KindSymlinkat: "symlink",
		schema.KindTruncate: "truncate", schema.KindUtime: "touch", schema.KindUtimes: "touch",
		schema.KindFutimesat: "touch", schema.KindUtimensat: "touch",
		schema.KindChmod: "chmod", schema.KindFchmodat: "chmod", schema.KindFchmodat2: "chmod",
		schema.KindChown: "chown", schema.KindLchown: "chown", schema.KindFchownat: "chown",
		schema.KindSetxattr: "setxattr", schema.KindLsetxattr: "setxattr",
		schema.KindRemovexattr: "removexattr", schema.KindLremovexattr: "removexattr",
	}
	fdVerb = map[string]string{
		schema.KindRead: "read", schema.KindPread64: "read", schema.KindReadv: "read",
		schema.KindPreadv: "read", schema.KindPreadv2: "read",
		schema.KindWrite: "write", schema.KindPwrite64: "write", schema.KindWritev: "write",
		schema.KindPwritev: "write", schema.KindPwritev2: "write",
		schema.KindFtruncate: "truncate", schema.KindFallocate: "fallocate",
		schema.KindFchmod: "chmod", schema.KindFchown: "chown",
		schema.KindFsetxattr: "setxattr", schema.KindFremovexattr: "removexattr",
	}
	// opens create the fd instances the fd kinds name.
	opens = map[string]bool{schema.KindOpen: true, schema.KindOpenat: true,
		schema.KindOpenat2: true, schema.KindCreat: true}
)

// File is the second read: it follows the fds of the processes that
// served a kept request and records the file work of their threads
// inside those requests' spans. Call it for every event, in each
// segment's order, after Plan.
func (r *Recorder) File(o Origin, ev *schema.Event) {
	if ev.PID == 0 || !r.procs[procKey{ev.HostID, ev.PID, ev.PIDInc}] {
		return
	}
	r.trackFD(ev)
	srv, ok := r.within(ev)
	if !ok {
		return
	}
	label, ok := r.describe(ev)
	if !ok {
		return
	}
	if len(r.ops[srv]) >= MaxSteps {
		r.moreOps[srv]++
		return
	}
	op := fileOp{org: r.origin(o), seq: ev.Seq, at: ev.WallNS, label: label, bytes: ev.Bytes,
		latency: ev.LatencyNS}
	op.thread, op.carrier, op.stack = javaOf(ev)
	r.ops[srv] = append(r.ops[srv], op)
}

// trackFD keeps the path each open fd instance was opened from, until
// its close. The table is capped (maxOpenFiles): a trace opening
// millions of files without closing them must not grow it unbounded.
func (r *Recorder) trackFD(ev *schema.Event) {
	k := fdKey{procKey{ev.HostID, ev.PID, ev.PIDInc}, ev.FD, ev.FDGen}
	switch {
	case opens[ev.Event] && ev.Ret >= 0 && ev.Args.Path != nil && len(r.fds) < maxOpenFiles:
		r.fds[k] = *ev.Args.Path
	case ev.Event == schema.KindClose:
		delete(r.fds, k)
	}
}

// within finds the kept server request of ev's thread whose span holds
// ev's start. Spans of one thread do not overlap unless requests nest,
// so the last one starting no later than ev is the one to check.
func (r *Recorder) within(ev *schema.Event) (int, bool) {
	ws := r.wanted[threadOf(ev)]
	n := sort.Search(len(ws), func(i int) bool { return r.reqs[ws[i]].start > ev.WallNS })
	if n == 0 {
		return 0, false
	}
	q := r.reqs[ws[n-1]]
	return ws[n-1], ev.WallNS <= q.end
}

// describe labels a file event: "read /etc/orders.conf", "rename a ->
// b", with the error when the call failed. Socket and pipe I/O, and an
// fd whose open the trace never showed, are no file work it can name.
func (r *Recorder) describe(ev *schema.Event) (string, bool) {
	var label string
	if verb, ok := fileVerb[ev.Event]; ok && ev.Args.Path != nil {
		if opens[ev.Event] && ev.Args.Flags != nil && *ev.Args.Flags&oCreat != 0 {
			verb = "create"
		}
		label = verb + " " + *ev.Args.Path
		if ev.Args.NewPath != nil {
			label += " -> " + *ev.Args.NewPath
		}
	} else if verb, ok := fdVerb[ev.Event]; ok && ev.ConnID == "" {
		path, known := r.fds[fdKey{procKey{ev.HostID, ev.PID, ev.PIDInc}, ev.FD, ev.FDGen}]
		if !known {
			return "", false
		}
		label = verb + " " + path
		if ev.Args.Offset != nil {
			label += " @" + strconv.FormatInt(*ev.Args.Offset, 10)
		}
	} else {
		return "", false
	}
	if ev.Ret < 0 {
		label += " (" + cmp.Or(ev.Err, "error") + ")"
	}
	return label, true
}

// Methods lists, per session, the method ids the kept conversations'
// stacks name, sorted: the build reads just these from the sessions'
// methods tables.
func (r *Recorder) Methods() map[string][]uint64 {
	want := map[string]map[uint64]bool{}
	add := func(org int, stack []uint64) {
		s := r.origins[org].Session
		if want[s] == nil {
			want[s] = map[uint64]bool{}
		}
		for _, id := range stack {
			want[s][id] = true
		}
	}
	for _, root := range r.kept {
		r.links.walk(root, func(i int) {
			add(r.reqs[i].org, r.reqs[i].stack)
			for _, op := range r.ops[i] {
				add(op.org, op.stack)
			}
		})
	}
	out := map[string][]uint64{}
	for s, ids := range want {
		if len(ids) > 0 {
			out[s] = slices.Sorted(maps.Keys(ids))
		}
	}
	return out
}

// Frame names one method id of a session's table ("class#method(file:
// line)"). An id never named stays a bare "method#<id>" in the model.
func (r *Recorder) Frame(session string, id uint64, frame string) {
	if r.frames[session] == nil {
		r.frames[session] = map[uint64]string{}
	}
	r.frames[session][id] = frame
}
