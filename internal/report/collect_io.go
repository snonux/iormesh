package report

import (
	"strconv"

	"iormesh/internal/schema"
)

// conn folds an event with a conn_id into its connection end: the role
// from connect or accept, the addresses from the first event naming
// them, stream bytes, and an http event's request (on the end and on the
// thread's line).
func (c *Collector) conn(p *procAcc, ev *schema.Event) bool {
	if isPair(ev.Event) && ev.Args.FD2 != nil && ev.Args.FD2Gen != nil {
		// pipe, pipe2, socketpair: both ends are born here.
		if c.end(p, ev.ConnID, fdKey{*ev.Args.FD2, *ev.Args.FD2Gen}, "") == nil {
			return false
		}
	}
	a := c.end(p, ev.ConnID, fdKey{ev.FD, ev.FDGen}, httpRole(ev))
	if a == nil {
		return false
	}
	switch ev.Event {
	case schema.KindConnect:
		a.role = RoleClient
	case schema.KindAccept, schema.KindAccept4:
		a.role = RoleServer
	}
	if a.addrL == "" {
		a.addrL = addrPort(ev.Args.LocalAddr, ev.Args.LocalPort)
	}
	if a.addrR == "" {
		a.addrR = addrPort(ev.Args.RemoteAddr, ev.Args.RemotePort)
	}
	switch {
	case ev.Ret <= 0:
	case schema.IsStreamSend(ev.Event):
		a.sent += ev.Bytes
	case schema.IsStreamReceive(ev.Event):
		a.recv += ev.StreamBytes() // a peek's bytes count once, when read (task ok4)
	}
	if ev.Event == schema.KindHTTP && ev.HTTP != nil {
		c.request(p, a, ev)
	}
	return true
}

// end finds or starts the process's end of conn held by fd. An event
// without an fd (an http event) goes to the end of its role (role, from
// httpRole), else to the conn's only end, else to an end no fd named
// yet; an fd event adopts such an end. nil past the cap.
func (c *Collector) end(p *procAcc, conn string, fd fdKey, role string) *connAcc {
	var loose *connAcc
	for _, i := range p.ends[conn] {
		a := p.conns[i]
		switch {
		case fd.fd >= 0 && a.fd == fd:
			return a
		case fd.fd < 0 && role != "" && a.role == role:
			return a
		case a.fd.fd < 0:
			loose = a
		}
	}
	if fd.fd < 0 && len(p.ends[conn]) == 1 {
		return p.conns[p.ends[conn][0]] // no role to tell ends apart: the only one
	}
	if loose != nil {
		if fd.fd >= 0 {
			loose.fd = fd
		}
		return loose
	}
	if !c.take() {
		return nil
	}
	p.ends[conn] = append(p.ends[conn], len(p.conns))
	a := &connAcc{local: conn, fd: fd}
	p.conns = append(p.conns, a)
	return a
}

// httpRole is the end an http event (fd -1) speaks for: the client's
// carries the request URI's host, the server's none (schema HTTP.Host).
// "" for other events.
func httpRole(ev *schema.Event) string {
	switch {
	case ev.Event != schema.KindHTTP || ev.HTTP == nil || ev.FD >= 0:
		return ""
	case ev.HTTP.Host != "":
		return RoleClient
	}
	return RoleServer
}

func isPair(kind string) bool {
	switch kind {
	case schema.KindPipe, schema.KindPipe2, schema.KindSocketpair:
		return true
	}
	return false
}

// request records an http event on its connection and its thread, each
// capped.
func (c *Collector) request(p *procAcc, a *connAcc, ev *schema.Event) {
	// Add counted the thread first; it is missing only past the cap.
	ti, ok := p.thrIdx[ev.TID]
	name := "tid " + itoa(ev.TID)
	if ok {
		name = threadName(p.threads[ti])
	}
	r := Request{Method: ev.HTTP.Method, Scheme: ev.HTTP.Scheme, Path: ev.HTTP.Path, Status: ev.HTTP.Status,
		Thread: name}
	if len(a.requests) < MaxCalls {
		a.requests = append(a.requests, r)
	} else {
		a.moreRequests++
	}
	if !ok {
		return
	}
	t := p.threads[ti]
	if len(t.calls) < MaxCalls {
		t.calls = append(t.calls, callAcc{end: a, method: r.Method, scheme: r.Scheme, path: r.Path,
			status: r.Status, carrier: ev.Java != nil && ev.Java.Carrier})
	} else {
		t.MoreCalls++
	}
}

// file folds ev into the file it concerns: an open names the file and
// its fd instance; the fd's reads, writes and close follow it; a path
// op that changes the file system is recorded as an op. Other events
// are no file's.
func (c *Collector) file(p *procAcc, ev *schema.Event, method string) bool {
	switch {
	case isOpen(ev.Event):
		return c.open(p, ev, method)
	case ev.FD >= 0:
		if i, ok := p.fds[fdKey{ev.FD, ev.FDGen}]; ok {
			c.fdEvent(p, i, ev, method)
			return true
		}
	}
	if ev.Args.Path == nil || !changesFS(ev.Event) {
		return true
	}
	f := c.fileFor(p, *ev.Args.Path)
	if f == nil {
		return false
	}
	newPath := ev.Args.NewPath
	if newPath != nil && isRename(ev.Event) {
		f.op("rename -> "+*newPath, ev.Err)
		if g := c.fileFor(p, *newPath); g != nil {
			g.op("renamed from "+*ev.Args.Path, ev.Err)
			g.method(method)
		}
	} else {
		f.op(ev.Event, ev.Err)
	}
	f.method(method)
	return true
}

// open records an open of a path: a failed one as an op, a successful
// one as an open for reading or writing and as the fd instance its
// later reads and writes are counted under. The fd instance holds one
// tracked entry while it is open (fdEvent gives it back on close); an
// instance already open (its close was lost) is pointed at the new path
// without a second one.
func (c *Collector) open(p *procAcc, ev *schema.Event, method string) bool {
	if ev.Args.Path == nil {
		return true
	}
	f := c.fileFor(p, *ev.Args.Path)
	if f == nil {
		return false
	}
	f.method(method)
	if ev.Ret < 0 {
		f.op("open", ev.Err)
		return true
	}
	// creat(2) is open(O_CREAT|O_WRONLY|O_TRUNC) and records no flags.
	if ev.Event == schema.KindCreat {
		f.op(ev.Event, "")
	}
	if ev.Event == schema.KindCreat || (ev.Args.Flags != nil && *ev.Args.Flags&oAccMode != oRDOnly) {
		f.WriteOpens++
	} else {
		f.ReadOpens++
	}
	if ev.FD < 0 {
		return true
	}
	k := fdKey{ev.FD, ev.FDGen}
	if _, open := p.fds[k]; !open && !c.take() {
		return false
	}
	p.fds[k] = p.fileIdx[*ev.Args.Path]
	return true
}

// fdEvent folds an event on a tracked file fd into file i. A close
// forgets the fd instance and returns its tracked entry: open fds are
// state while reading, not rollup entries, so the cap bounds the fds
// open at once rather than the opens a build reads (task 2i4).
func (c *Collector) fdEvent(p *procAcc, i int, ev *schema.Event, method string) {
	f := p.files[i]
	f.method(method)
	switch {
	case ev.Event == schema.KindClose:
		delete(p.fds, fdKey{ev.FD, ev.FDGen})
		c.tracked--
	case changesFS(ev.Event):
		f.op(ev.Event, ev.Err)
	case ev.Ret < 0:
	case isFileRead(ev.Event):
		f.ReadBytes += ev.Bytes
	case isFileWrite(ev.Event):
		f.WriteBytes += ev.Bytes
	}
}

// fileFor finds or starts a process's file entry; nil past the cap.
func (c *Collector) fileFor(p *procAcc, path string) *fileAcc {
	if i, ok := p.fileIdx[path]; ok {
		return p.files[i]
	}
	if !c.take() {
		return nil
	}
	p.fileIdx[path] = len(p.files)
	f := &fileAcc{File: File{Path: path}, ops: map[string]bool{}, methods: map[string]bool{}}
	p.files = append(p.files, f)
	return f
}

// op records a structural change once, " (ERRNO)" appended when it
// failed; past MaxFileOps distinct ops it is only counted.
func (f *fileAcc) op(op, errno string) {
	if errno != "" {
		op += " (" + errno + ")"
	} else {
		f.changed = true
	}
	if f.ops[op] {
		return
	}
	f.ops[op] = true
	if len(f.Ops) < MaxFileOps {
		f.Ops = append(f.Ops, op)
	} else {
		f.MoreOps++
	}
}

// method records a causing method once, up to MaxFileMethods.
func (f *fileAcc) method(m string) {
	if m == "" || f.methods[m] || len(f.Methods) >= MaxFileMethods {
		return
	}
	f.methods[m] = true
	f.Methods = append(f.Methods, m)
}

func isOpen(kind string) bool {
	switch kind {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2, schema.KindCreat:
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

func isFileRead(kind string) bool {
	switch kind {
	case schema.KindRead, schema.KindReadv, schema.KindPread64, schema.KindPreadv, schema.KindPreadv2:
		return true
	}
	return false
}

func isFileWrite(kind string) bool {
	switch kind {
	case schema.KindWrite, schema.KindWritev, schema.KindPwrite64, schema.KindPwritev, schema.KindPwritev2:
		return true
	}
	return false
}

// changesFS reports the file kinds that change the file system — the
// meta ops PLAN.md's sequence view protocols (create, delete,
// rename/move, touch, symlink, …). Reads of metadata (stat, readlink,
// getxattr) are no change and stay out of the rollup.
func changesFS(kind string) bool {
	switch kind {
	case schema.KindCreat, schema.KindMkdir, schema.KindMkdirat, schema.KindMknod, schema.KindMknodat,
		schema.KindRmdir, schema.KindUnlink, schema.KindUnlinkat,
		schema.KindRename, schema.KindRenameat, schema.KindRenameat2,
		schema.KindLink, schema.KindLinkat, schema.KindSymlink, schema.KindSymlinkat,
		schema.KindTruncate, schema.KindFtruncate, schema.KindFallocate,
		schema.KindUtime, schema.KindUtimes, schema.KindFutimesat, schema.KindUtimensat,
		schema.KindChmod, schema.KindFchmod, schema.KindFchmodat, schema.KindFchmodat2,
		schema.KindChown, schema.KindLchown, schema.KindFchown, schema.KindFchownat,
		schema.KindSetxattr, schema.KindLsetxattr, schema.KindFsetxattr,
		schema.KindRemovexattr, schema.KindLremovexattr, schema.KindFremovexattr:
		return true
	}
	return false
}

// threadName is a thread's display name: its Java name, else its tid.
func threadName(t *threadAcc) string {
	if t.Name != "" {
		return t.Name
	}
	return "tid " + itoa(t.TID)
}

func itoa(n uint32) string { return strconv.FormatUint(uint64(n), 10) }
