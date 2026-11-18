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
	// markers of this scenario (inclusive, per process); empty keeps
	// everything.
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
		if events, err = cut(events, opt.Bracket); err != nil {
			return Trace{}, err
		}
	}
	n := normalizer{opt: opt, syms: newSymbols()}
	out := Trace{Events: make([]schema.Event, len(events))}
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
	if returnsFD(e.Event) && e.Ret >= 0 {
		// A successful open or accept returns the fd itself, so its
		// return value is as run-specific as the fd and follows it.
		e.Ret = int64(e.FD)
	}
	e.PID, e.PIDInc = n.process(proc, e.PID)
}

// process maps a raw (pid, incarnation) to ordinals. The PID ordinal is
// keyed by host and PID alone, so a reused PID keeps its number and only
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

// args rewrites paths, addresses and ports. The pointers are replaced,
// never written through, because the input event shares them.
func (n normalizer) args(a schema.Args) schema.Args {
	a.Path = mapPtr(a.Path, n.path)
	a.NewPath = mapPtr(a.NewPath, n.path)
	a.LocalAddr = mapPtr(a.LocalAddr, canonicalAddr)
	a.RemoteAddr = mapPtr(a.RemoteAddr, canonicalAddr)
	a.LocalPort = mapPtr(a.LocalPort, n.port)
	a.RemotePort = mapPtr(a.RemotePort, n.port)
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
