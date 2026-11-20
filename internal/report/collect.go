package report

import (
	"net/netip"
	"strconv"

	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// maxTracked caps the entries the collector keeps while reading —
// processes, files, open fds, connections, threads, methods, requests
// together — the same bound as query's filter state: a hostile trace
// cannot grow the build's memory through the rollup. Past it, events
// that would need a new entry are counted in Report.Untracked.
const maxTracked = 1 << 20

// Open flags as Linux defines them: the trace is Linux's whatever
// machine builds the rollup (iormeshctl also runs on macOS).
const (
	oAccMode = 0o3
	oRDOnly  = 0o0
)

// Source names the session an event came from: its store directories,
// FQDN and the federate session key that scopes its method ids.
type Source struct {
	Project, Host, FQDN, Session string
}

// Collector accumulates the rollup from the events a build merges.
// Not safe for concurrent use.
type Collector struct {
	// methods holds each session's method table by id, keyed by
	// Source.Session.
	methods map[string]map[uint64]schema.Method
	procs   map[procKey]*procAcc
	// limit is maxTracked; tests lower it to reach the cap.
	limit     int
	tracked   int
	untracked uint64
}

// procKey names a process incarnation on one host (host id).
type procKey struct {
	host string
	pid  uint32
	inc  uint64
}

// fdKey names one fd instance of a process.
type fdKey struct {
	fd  int32
	gen uint32
}

// procAcc is one process so far; the maps are indexes into the
// first-seen-ordered slices.
type procAcc struct {
	p       Process
	hostID  string
	files   []*fileAcc
	fileIdx map[string]int
	fds     map[fdKey]int
	conns   []*connAcc
	// ends lists, per host-local conn id, the indexes of the process's
	// ends of it: one process may hold both (a JVM calling its own server
	// over loopback, a pipe within one process), and each end is an entry
	// of its own.
	ends    map[string][]int
	threads []*threadAcc
	thrIdx  map[uint32]int
	methods map[string]*MethodStat
}

type fileAcc struct {
	File
	ops, methods map[string]bool
}

// connAcc is one end of a connection: the conn id and the fd instance
// that holds it (fd -1 until an event with an fd names it).
type connAcc struct {
	local        string // the host-local conn id
	fd           fdKey
	role         string
	addrL, addrR string
	sent, recv   uint64
	requests     []Request
	moreRequests int
}

type threadAcc struct {
	Thread
	calls []callAcc
}

// callAcc is a request on a thread's line before the connection's role
// and merged id are known.
type callAcc struct {
	end                  *connAcc
	method, scheme, path string
	status               uint16
	carrier              bool // a virtual thread's request (java.carrier)
}

// NewCollector returns an empty collector; methods are the sessions'
// method tables (keyed by Source.Session), nil without Java.
func NewCollector(methods map[string]map[uint64]schema.Method) *Collector {
	return &Collector{methods: methods, procs: map[procKey]*procAcc{}, limit: maxTracked}
}

// Add folds one event of src into the rollup. It keeps no reference to
// ev. Host-level records (pid 0: loss, session, end) belong to no
// process; the view model reports them. An event some part of which
// found no room under the cap counts once in untracked.
func (c *Collector) Add(src Source, ev *schema.Event) {
	if !c.add(src, ev) {
		c.untracked++
	}
}

// add is Add, false when the cap left part of ev untracked.
func (c *Collector) add(src Source, ev *schema.Event) bool {
	if ev.PID == 0 {
		return true
	}
	p := c.proc(src, ev)
	if p == nil {
		return false
	}
	p.p.Events++
	if ev.Comm != "" {
		p.p.Comm = ev.Comm
	}
	method := ""
	if ev.Attribution == schema.AttributionJava && ev.Java != nil {
		p.p.JavaEvents++
		method = normalize.CausingMethod(*ev, c.methods[src.Session])
	}
	ok := c.thread(p, ev)
	ok = c.method(p, method, ev.Bytes) && ok
	switch ev.Event {
	case schema.KindExec:
		p.p.Argv = append([]string(nil), ev.Args.Argv...)
	case schema.KindAttach:
		if ev.Args.Pattern != nil {
			p.p.App = *ev.Args.Pattern
		}
	}
	if ev.ConnID != "" {
		ok = c.conn(p, ev) && ok
	}
	return c.file(p, ev, method) && ok
}

// take reserves one tracked entry, false once the cap is reached.
func (c *Collector) take() bool {
	if c.tracked >= c.limit {
		return false
	}
	c.tracked++
	return true
}

// proc finds or starts ev's process; nil past the cap.
func (c *Collector) proc(src Source, ev *schema.Event) *procAcc {
	k := procKey{ev.HostID, ev.PID, ev.PIDInc}
	if p, ok := c.procs[k]; ok {
		return p
	}
	if !c.take() {
		return nil
	}
	p := &procAcc{hostID: ev.HostID, fileIdx: map[string]int{}, fds: map[fdKey]int{},
		ends: map[string][]int{}, thrIdx: map[uint32]int{}, methods: map[string]*MethodStat{}}
	p.p = Process{ID: schema.ProcessNodeID(ev.HostID, ev.PID, ev.PIDInc), Project: src.Project,
		Host: src.Host, FQDN: src.FQDN, PID: ev.PID, Incarnation: ev.PIDInc}
	c.procs[k] = p
	return p
}

// thread counts ev on its thread and names the thread after the first
// Java thread name it carries.
func (c *Collector) thread(p *procAcc, ev *schema.Event) bool {
	i, ok := p.thrIdx[ev.TID]
	if !ok {
		if !c.take() {
			return false
		}
		i = len(p.threads)
		p.thrIdx[ev.TID] = i
		p.threads = append(p.threads, &threadAcc{Thread: Thread{TID: ev.TID}})
	}
	t := p.threads[i]
	t.Events++
	t.Bytes += ev.Bytes
	if ev.Java != nil && ev.Java.Carrier {
		t.Carried++
	}
	if t.Name == "" && ev.Java != nil && ev.Java.Thread != "" {
		t.Name = ev.Java.Thread
	}
	return true
}

// method counts an event on its causing method ("" for none).
func (c *Collector) method(p *procAcc, method string, bytes uint64) bool {
	if method == "" {
		return true
	}
	m, ok := p.methods[method]
	if !ok {
		if !c.take() {
			return false
		}
		m = &MethodStat{Method: method}
		p.methods[method] = m
	}
	m.Events++
	m.Bytes += bytes
	return true
}

// addrPort formats an address and port as the event carried them: ""
// without a port or with port 0 (a connect's local end before the
// kernel bound it: a later record of the connection names the real
// one), ":PORT" without an address.
func addrPort(addr *string, port *uint16) string {
	if port == nil || *port == 0 {
		return ""
	}
	if addr == nil {
		return ":" + strconv.Itoa(int(*port))
	}
	if a, err := netip.ParseAddr(*addr); err == nil {
		return netip.AddrPortFrom(a, *port).String()
	}
	return *addr + ":" + strconv.Itoa(int(*port))
}
