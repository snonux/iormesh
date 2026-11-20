package normalize

import (
	"net/netip"
	"strconv"
	"strings"

	"iormesh/internal/schema"
)

// Ranges kept verbatim. Renumbered values start right after them, so a
// renumbered value can never be mistaken for a kept one: the first
// traced fd instance becomes 3 (as in a fresh process), the first dynamic
// port 1024.
const (
	// firstDynamicFD: fds 0/1/2 are the same in every run.
	firstDynamicFD = 3
	// firstDynamicPort: ports below it are well-known and part of what a
	// scenario declares; ports above are kernel- or harness-picked.
	firstDynamicPort = 1024
	// tmpSymbol replaces Options.TmpPrefix in paths, as goldens write it.
	tmpSymbol = "$tmp"
)

// Symbol kinds of the symbol table, and the string symbols' prefixes.
const (
	kindHost = "h"
	kindPID  = "pid"
	kindInc  = "inc"
	kindTID  = "tid"
	kindFD   = "fd"
	kindPort = "port"
	kindConn = "c"
)

// Options controls Normalize.
type Options struct {
	// Bracket cuts the trace to the events between the begin and end
	// markers of this scenario (inclusive, per process; a process forked
	// inside a bracket shares it, which the graph's forked edges tell);
	// empty keeps everything.
	Bracket string
	// TmpPrefix is replaced by "$tmp" in every path and file node id;
	// empty leaves paths alone.
	TmpPrefix string
}

// Trace is what Normalize reads and writes: the event stream and the
// graph export of one trace. They are normalized together because node
// ids embed the same host ids, PIDs, paths and connection ids as events.
type Trace struct {
	Events []schema.Event
	Graph  schema.Graph
	// Methods is the trace's Java method table (methods.ndjson, M3) by
	// id, nil when the run joined no JVM: what an event's java.stack ids
	// name. Normalize keeps it as is — method ids are per trace, and a
	// check resolves them to names before comparing.
	Methods map[uint64]schema.Method
}

// normalizer holds the symbol table shared by events and graph.
type normalizer struct {
	opt  Options
	syms *symbols
}

// Normalize cuts the trace to opt.Bracket, then rewrites run-specific
// values to ordinals in order of first appearance, so two runs of one
// scenario compare equal. It never reorders events and never modifies
// the input.
//
// Cutting comes first (PLAN.md M0 "Golden semantics"): ids numbered over
// the whole trace would shift with every JVM startup file outside the
// bracket, and the bracket is exactly where runs must agree. The graph is
// pruned to what the bracketed events touched for the same reason.
func Normalize(in Trace, opt Options) (Trace, error) {
	events := in.Events
	if opt.Bracket != "" {
		var err error
		if events, err = cut(events, in.Graph.Edges, opt.Bracket); err != nil {
			return Trace{}, err
		}
	}
	n := normalizer{opt: opt, syms: newSymbols()}
	out := Trace{Events: make([]schema.Event, len(events)), Methods: in.Methods}
	for i, e := range events {
		out.Events[i] = n.event(e, uint64(i+1))
	}
	g := in.Graph
	if opt.Bracket != "" {
		g = prune(g, events)
	}
	out.Graph = n.graph(g)
	return out, nil
}

// event rewrites one event. rank is its 1-based position in the output:
// it becomes seq and both clocks, since only order survives across runs.
// Latency is a duration whose value is noise, so it becomes 0.
func (n normalizer) event(e schema.Event, rank uint64) schema.Event {
	e.Seq, e.BootNS, e.WallNS, e.LatencyNS = rank, rank, rank, 0
	if e.PID != 0 {
		// PID 0 marks host-level events (drop, session, end): they stay
		// 0, like stdio fds, so they keep that meaning and a run-specific
		// drop never takes — and shifts — a process ordinal.
		n.processFields(&e)
	}
	e.HostID = n.host(e.HostID)
	if e.ConnID != "" {
		e.ConnID = n.conn(e.ConnID)
	}
	e.Args = n.args(e.Args)
	return e
}

// processFields renumbers the fields naming a process, its thread and
// its fd. The process key is taken before any of them changes.
func (n normalizer) processFields(e *schema.Event) {
	proc := processKey(*e)
	e.TID = uint32(n.syms.ord(kindTID, proc+"/"+strconv.FormatUint(uint64(e.TID), 10)))
	e.FD, e.FDGen = n.fd(proc, e.FD, e.FDGen)
	if e.Args.FD2 != nil {
		// A second fd instance of the same process (a pipe's other end,
		// M2 step 7) is renumbered like the first; fresh pointers, since
		// the input event shares them.
		var gen uint32
		if e.Args.FD2Gen != nil {
			gen = *e.Args.FD2Gen
		}
		fd2, gen2 := n.fd(proc, *e.Args.FD2, gen)
		e.Args.FD2, e.Args.FD2Gen = &fd2, &gen2
	}
	if returnsFD(e.Event) && e.Ret >= 0 {
		// A successful open or accept returns the fd itself, so its
		// return value is as run-specific as the fd and follows it.
		e.Ret = int64(e.FD)
	}
	e.PID, e.PIDInc = n.process(proc, e.PID)
	host, _, _ := strings.Cut(strings.TrimPrefix(proc, "proc:"), "/")
	e.Args.TargetPID = mapPtr(e.Args.TargetPID, func(pid uint32) uint32 { return n.pid(host, pid) })
	e.Args.SenderPID = mapPtr(e.Args.SenderPID, func(pid uint32) uint32 { return n.pid(host, pid) })
	n.parentFields(host, &e.Args)
}

// parentFields renumbers a fork's parent (task ze4) like the parent's own
// events: its pid by the PID ordinal, its incarnation by the ordinal of
// its process key — the parent's events come first, so they agree.
func (n normalizer) parentFields(host string, a *schema.Args) {
	if a.ParentPID == nil || a.ParentInc == nil {
		return
	}
	key := schema.ProcessNodeID(host, *a.ParentPID, *a.ParentInc)
	pid, inc := n.process(key, *a.ParentPID)
	a.ParentPID, a.ParentInc = &pid, &inc
}

// pid renumbers a raw pid an argument names (a kill's target, a signal's
// sender) with the PID ordinal its process's events get: they are keyed
// alike (host and PID), so a signalled child's events and the kill naming
// it agree. An untraced pid takes an ordinal too — in a deterministic
// scenario it is still first named at the same place.
func (n normalizer) pid(host string, pid uint32) uint32 {
	return uint32(n.syms.ord(kindPID, host+"/"+strconv.FormatUint(uint64(pid), 10)))
}

// process maps a raw (pid, incarnation) to ordinals. The PID ordinal is
// keyed by host and PID alone (as pid's), so a reused PID keeps its number and only
// the incarnation tells the two processes apart — as in the raw trace.
func (n normalizer) process(procKey string, pid uint32) (uint32, uint64) {
	host, _, _ := strings.Cut(strings.TrimPrefix(procKey, "proc:"), "/")
	p := n.syms.ord(kindPID, host+"/"+strconv.FormatUint(uint64(pid), 10))
	inc := n.syms.ord(kindInc, procKey)
	return uint32(p), uint64(inc)
}

// fd maps one fd instance — (process, fd, fd_gen), the identity an
// expectation's $fd symbol binds to — to one number. The generation is
// folded into the number, so it becomes 1 (created in the trace) or stays
// 0 (predates it); its raw count depends on unrelated JVM activity.
func (n normalizer) fd(procKey string, fd int32, gen uint32) (int32, uint32) {
	if fd < firstDynamicFD {
		return fd, gen
	}
	key := procKey + "#" + strconv.Itoa(int(fd)) + "." + strconv.FormatUint(uint64(gen), 10)
	if gen > 0 {
		gen = 1
	}
	return int32(firstDynamicFD - 1 + n.syms.ord(kindFD, key)), gen
}

func (n normalizer) host(id string) string {
	return "$" + kindHost + strconv.Itoa(n.syms.ord(kindHost, id))
}

func (n normalizer) conn(id string) string {
	return "$" + kindConn + strconv.Itoa(n.syms.ord(kindConn, id))
}

// args rewrites paths, addresses and ports and drops the tcp_sock
// sequence fields. The pointers (and an exec's argv) are replaced, never
// written through, because the input event shares them. An argv element
// that is a path under the scenario's temp directory is one too — a child
// gets its work dir that way — so it is rewritten like args.path; other
// elements pass unchanged.
func (n normalizer) args(a schema.Args) schema.Args {
	a.Path = mapPtr(a.Path, n.path)
	a.NewPath = mapPtr(a.NewPath, n.path)
	if a.Argv != nil {
		argv := make([]string, len(a.Argv))
		for i, arg := range a.Argv {
			argv[i] = n.path(arg)
		}
		a.Argv = argv
	}
	a.LocalAddr = mapPtr(a.LocalAddr, canonicalAddr)
	a.RemoteAddr = mapPtr(a.RemoteAddr, canonicalAddr)
	a.LocalPort = mapPtr(a.LocalPort, n.port)
	a.RemotePort = mapPtr(a.RemotePort, n.port)
	// A tcp_state record's tcp_sock fields (M4, decision #26) are drawn
	// from the kernel's random initial sequence numbers: no two runs
	// agree on them, and no expectation could name them. Dropped, all
	// four together, so a normalized trace stays comparable across runs
	// and still satisfies the schema's all-four-or-none.
	a.SndUna, a.RcvNxt, a.BytesAcked, a.BytesReceived = nil, nil, nil, nil
	// A data call's tcp_seq (task xe4) is a sequence number from the same
	// random ISNs: dropped for the same reason.
	a.TCPSeq = nil
	return a
}

// port renumbers dynamic ports by value alone, not per host or process:
// a loopback server's local port and its client's remote port are one
// port and must get one number ($port1 on both sides of tcp-loopback).
func (n normalizer) port(p uint16) uint16 {
	if p < firstDynamicPort {
		return p
	}
	return uint16(firstDynamicPort - 1 + n.syms.ord(kindPort, strconv.Itoa(int(p))))
}

// path replaces the scenario's temp directory with $tmp. Only a whole
// leading path component matches: /tmp/xy is not under /tmp/x.
func (n normalizer) path(p string) string {
	prefix := strings.TrimSuffix(n.opt.TmpPrefix, "/")
	if prefix == "" {
		return p
	}
	if p == prefix {
		return tmpSymbol
	}
	if rest, ok := strings.CutPrefix(p, prefix+"/"); ok {
		return tmpSymbol + "/" + rest
	}
	return p
}

// canonicalAddr unmaps IPv4-mapped IPv6 addresses: the JVM's dual-stack
// sockets report 127.0.0.1 as ::ffff:127.0.0.1 unless the host lacks
// IPv6, so without this the same scenario reads differently per host.
// Unparseable values pass through unchanged.
func canonicalAddr(s string) string {
	a, err := netip.ParseAddr(s)
	if err != nil || !a.Is4In6() {
		return s
	}
	return a.Unmap().String()
}

// returnsFD reports whether a kind's non-negative return value is the fd
// it created (and therefore equals Event.FD).
func returnsFD(kind string) bool {
	switch kind {
	case schema.KindOpen, schema.KindOpenat, schema.KindOpenat2,
		schema.KindAccept, schema.KindAccept4:
		return true
	}
	return false
}

// mapPtr applies f to *p into a fresh pointer; nil stays nil.
func mapPtr[T any](p *T, f func(T) T) *T {
	if p == nil {
		return nil
	}
	v := f(*p)
	return &v
}
