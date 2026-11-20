package viewmodel

import (
	"cmp"
	"fmt"
	"strconv"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/servicedb"
)

// emitter writes one conversation's steps, depth first: a request, the
// server's work in its thread's order (file operations and the calls it
// made, each with its own subtree), the response. Each step's Ordering
// is judged against the step before it in the list (ordering). Every
// step names its lifelines — the thread at each end (Lifeline) — and
// hands both ends to the thread stories (stories.go).
type emitter struct {
	r       *Recorder
	n       namer
	c       Conversation
	prev    federate.Stamp
	started bool
	seen    map[string]bool  // services already listed
	lanes   map[Lifeline]int // lifelines already listed
	book    *storyBook       // the thread stories, across conversations
}

// end is one lifeline's part in a step: the lifeline, the instant on
// that thread's own host clock, and the owned server request whose span
// holds it there (-1 for none) — what a thread story is ordered by.
type end struct {
	lane Lifeline
	wall uint64
	span int
}

// noEnd is the missing far end of a file step.
var noEnd = end{span: -1}

// conversations builds the kept conversations, newest first, and the
// stories of the threads they show.
func (r *Recorder) conversations(n namer) ([]Conversation, []ThreadStory) {
	out := make([]Conversation, 0, len(r.kept))
	book := newStoryBook()
	for i, root := range r.kept {
		q := r.reqs[root]
		e := emitter{r: r, n: n, seen: map[string]bool{}, lanes: map[Lifeline]int{}, book: book}
		e.c = Conversation{ID: fmt.Sprintf("%04d", i+1), Start: wallTime(q.start),
			Label: requestLabel(q), Steps: []Step{}, Services: []string{}, Lifelines: []Lifeline{}}
		if q.client {
			e.c.Entry = n.process(q.th)
			e.client(root, "")
		} else {
			e.c.Entry = n.server(q)
			e.serverRoot(root)
		}
		out = append(out, e.c)
	}
	return out, book.stories()
}

// client emits client request i made from lifeline ("" for a root: its
// process names it) and, when its server end is joined, that end's
// work and the response.
func (e *emitter) client(i int, lifeline string) {
	q, l := e.r.reqs[i], e.r.links
	if lifeline == "" {
		lifeline = e.n.process(q.th)
	}
	p, hop := l.partner[i], l.hop[i]
	to, at := e.n.peer(q), stampAt(q.th.host, q.start)
	me, them := e.thread(lifeline, i, l.parent[i], q.start), end{lane: Lifeline{Service: to}, span: -1}
	if p >= 0 {
		to, at = e.n.server(e.r.reqs[p]), stampAt(e.r.reqs[p].th.host, e.r.reqs[p].start)
		them = e.thread(to, p, e.r.own(p), e.r.reqs[p].start)
	}
	req := e.side(q, lifeline)
	req.Kind, req.To, req.Label, req.Bytes, req.Hop = StepRequest, to, requestLabel(q), q.reqBytes, hop
	e.c.Hops++
	if hop == HopAsync { // one record, no start: one step says it all
		req.Label += " -> " + statusLabel(q.status)
		me.wall = q.end
		e.add(req, stampAt(q.th.host, q.end), false, me, them)
		return
	}
	e.add(req, at, hop == HopPaired, me, them)
	resp := Step{Kind: StepResponse, Service: to, To: lifeline, Label: statusLabel(q.status),
		Bytes: q.respBytes, Latency: time.Duration(q.end - q.start), Stack: []string{}, Ref: e.ref(q.org, q.seq)}
	if p >= 0 {
		s := e.r.reqs[p]
		e.body(p, to)
		resp.Host, resp.Thread, resp.Carrier = e.r.origins[s.org].FQDN, threadName(s), s.carrier
		resp.Stack, resp.Ref = e.frames(s.org, s.stack), e.ref(s.org, s.seq)
		resp.Bytes = max(resp.Bytes, s.respBytes)
		them.wall = s.end
	}
	me.wall = q.end
	e.add(resp, stampAt(q.th.host, q.end), hop == HopPaired, them, me)
}

// serverRoot emits a server request no client end was joined to: the
// request from its caller (traced but unpaired, or untraced, its thread
// unknown either way), the server's work, and its response.
func (e *emitter) serverRoot(i int) {
	q := e.r.reqs[i]
	caller, lifeline := e.n.caller(q), e.n.server(q)
	hop := HopUnpaired
	if caller == servicedb.Untraced {
		hop = HopUntraced
	}
	them, me := end{lane: Lifeline{Service: caller}, span: -1}, e.thread(lifeline, i, e.r.own(i), q.start)
	e.c.Hops++
	e.add(Step{Kind: StepRequest, Service: caller, To: lifeline, Label: requestLabel(q),
		Bytes: q.reqBytes, Hop: hop, Stack: []string{}, Ref: e.ref(q.org, q.seq)},
		stampAt(q.th.host, q.start), false, them, me)
	e.body(i, lifeline)
	resp := e.side(q, lifeline)
	resp.Kind, resp.To, resp.Label, resp.Bytes = StepResponse, caller, statusLabel(q.status), q.respBytes
	if q.hasStart {
		resp.Latency = time.Duration(q.end - q.start)
	}
	me.wall = q.end
	e.add(resp, stampAt(q.th.host, q.end), true, me, them)
}

// body emits server request s's work on lifeline: its thread's file
// operations and the client requests it made, merged in the thread's
// own clock order — one thread, so the order is causal.
func (e *emitter) body(s int, lifeline string) {
	ops, kids := e.r.ops[s], e.r.links.children[s]
	srv := e.r.reqs[s]
	for len(ops) > 0 || len(kids) > 0 {
		if len(ops) > 0 && (len(kids) == 0 || ops[0].at <= e.r.reqs[kids[0]].start) {
			op := ops[0]
			ops = ops[1:]
			st := Step{Kind: StepFile, Service: lifeline, Host: e.r.origins[op.org].FQDN,
				Thread: cmp.Or(op.thread, threadName(srv)), Carrier: op.carrier, Label: op.label,
				Bytes: op.bytes, Latency: time.Duration(op.latency), Stack: e.frames(op.org, op.stack),
				Ref: e.ref(op.org, op.seq)}
			me := end{lane: Lifeline{Service: lifeline, Host: st.Host, PID: srv.th.pid, TID: srv.th.tid,
				Thread: st.Thread, Carrier: op.carrier}, wall: op.at, span: s}
			e.add(st, stampAt(srv.th.host, op.at), true, me, noEnd)
			continue
		}
		e.client(kids[0], lifeline)
		kids = kids[1:]
	}
	e.c.MoreSteps += e.r.moreOps[s]
}

// thread is request i's end on lifeline (a service): its kernel thread,
// named as its steps name it, at instant wall, inside span.
func (e *emitter) thread(lifeline string, i, span int, wall uint64) end {
	q := e.r.reqs[i]
	return end{lane: Lifeline{Service: lifeline, Host: e.r.origins[q.org].FQDN, PID: q.th.pid,
		TID: q.th.tid, Thread: threadName(q), Carrier: q.carrier}, wall: wall, span: span}
}

// side is the step fields request q's own end fills: its lifeline,
// host, thread, stack and raw event.
func (e *emitter) side(q request, lifeline string) Step {
	return Step{Service: lifeline, Host: e.r.origins[q.org].FQDN, Thread: threadName(q),
		Carrier: q.carrier, Stack: e.frames(q.org, q.stack), Ref: e.ref(q.org, q.seq)}
}

// add appends s, judged at instant at; causal says a thread order or a
// paired send→receive puts it after the step before. from and to are
// its two ends (to.lane.Service "" for none). A step past MaxSteps is
// counted, not kept — but still moves the instant on, so the next kept
// step is judged against what really came before it, and its ends are
// counted in their threads' stories.
func (e *emitter) add(s Step, at federate.Stamp, causal bool, from, to end) {
	s.Ordering = e.ordering(at, causal)
	e.prev, e.started = at, true
	if len(e.c.Steps) >= MaxSteps {
		e.c.MoreSteps++
		e.book.cut(from.lane, to.lane)
		return
	}
	for _, l := range []string{s.Service, s.To} {
		if l != "" && !e.seen[l] {
			e.seen[l] = true
			e.c.Services = append(e.c.Services, l)
		}
	}
	s.Lane, s.ToLane = e.lane(from.lane), -1
	if to.lane.Service != "" {
		s.ToLane = e.lane(to.lane)
	}
	e.c.Steps = append(e.c.Steps, s)
	e.book.mark(e.c.ID, len(e.c.Steps), s, from, to)
}

// lane is l's index in the conversation's Lifelines, added on first
// appearance.
func (e *emitter) lane(l Lifeline) int {
	if i, ok := e.lanes[l]; ok {
		return i
	}
	e.lanes[l] = len(e.c.Lifelines)
	e.c.Lifelines = append(e.c.Lifelines, l)
	return len(e.c.Lifelines) - 1
}

// own is i when request i's span belongs to its thread alone
// (request.owned), else -1: only an owned span makes two of the
// thread's actions inside it causally ordered.
func (r *Recorder) own(i int) int {
	if r.reqs[i].owned() {
		return i
	}
	return -1
}

// ordering judges a step at instant at against the previous step:
// causal when an edge says so; else time-ordered only when the clocks
// prove the previous instant earlier — one host's clock directly, two
// hosts' through the merge's clock bounds (federate.Timeline) — and
// unordered when they cannot. Only the step right before is compared:
// a step unordered against it may still be provably after an earlier
// one. The first step has nothing before it: causal when its own hop is
// paired, else time-ordered (it is placed by its own stamp).
func (e *emitter) ordering(at federate.Stamp, causal bool) string {
	switch {
	case causal:
		return OrderCausal
	case !e.started, e.r.tl.Order(e.prev, at) == federate.Before:
		return OrderTime
	}
	return OrderNone
}

// frames resolves a stack's method ids through the session's methods
// table; an id the table did not name stays "method#<id>".
func (e *emitter) frames(org int, stack []uint64) []string {
	out := make([]string, 0, len(stack))
	names := e.r.frames[e.r.origins[org].Session]
	for _, id := range stack {
		f, ok := names[id]
		if !ok {
			f = "method#" + strconv.FormatUint(id, 10)
		}
		out = append(out, f)
	}
	return out
}

// ref names a raw event for `iormeshctl query -ref`: the session (the
// store's "<project>/<host>/<fqdn>-<session>") and the event's seq,
// which is unique within it.
func (e *emitter) ref(org int, seq uint64) string {
	return e.r.origins[org].Session + "/" + strconv.FormatUint(seq, 10)
}

// requestLabel is "METHOD path".
func requestLabel(q request) string {
	if q.path == "" {
		return q.method
	}
	return q.method + " " + q.path
}

// statusLabel is the response status, "no status" when none came.
func statusLabel(s uint16) string {
	if s == 0 {
		return "no status"
	}
	return strconv.Itoa(int(s))
}

// threadName is the Java thread's name, else the kernel tid.
func threadName(q request) string { return cmpThread(q.thread, q.th.tid) }

func cmpThread(name string, tid uint32) string {
	if name != "" {
		return name
	}
	return "tid " + strconv.FormatUint(uint64(tid), 10)
}
