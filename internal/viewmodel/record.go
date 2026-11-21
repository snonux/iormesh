package viewmodel

import (
	"cmp"
	"slices"

	"iormesh/internal/federate"
	"iormesh/internal/schema"
)

// The Recorder gathers what conversations are built from, in two reads
// of the build's segments after the merge (internal/ctl build):
//
//  1. HTTP: every http event, as a compact record with its conn id
//     renamed to the merged trace's — at most MaxRequests of them, the
//     newest by completion.
//  2. Plan links the records (link.go) and picks the conversations, and
//     with them the server requests whose thread's file work belongs in
//     them.
//  3. File: only those threads' file events inside those requests'
//     spans, at most MaxSteps per request, plus the fd → path table of
//     their processes (a read names its file through the open).
//  4. Methods lists the Java method ids the kept steps need, per
//     session; Frame fills them in from the sessions' methods tables.
//
// No pass holds events: what grows is bounded by the caps above.

// Origin is the segment an event was read from: its session, as the
// store names it ("<project>/<host>/<fqdn>-<session>", federate
// Segment.Session — the scope of its seqs and method ids), and the
// session's FQDN. A step's Ref and Host come from it.
type Origin struct{ Session, FQDN string }

// threadKey names one kernel thread of one process incarnation.
type threadKey struct {
	host string // host id
	pid  uint32
	inc  uint64
	tid  uint32
}

// procKey names one process incarnation.
type procKey struct {
	host string
	pid  uint32
	inc  uint64
}

// fdKey names one fd instance of a process.
type fdKey struct {
	proc  procKey
	fd    int32
	fdGen uint32
}

// request is one http event, reduced to what a conversation shows.
type request struct {
	org    int    // index into Recorder.origins
	seq    uint64 // the event's seq: with org, its Ref
	th     threadKey
	thread string
	// carrier: the I/O of a virtual thread, named by its carrier.
	carrier bool
	// held: the recording thread worked on this request alone over its
	// span (schema HTTP.Held — a synchronous library's request).
	held   bool
	client bool // http.host set: the client's end (docs/schema.md)
	conn   string
	local  federate.LocalConn
	reqSeq uint32
	method string
	path   string
	status uint16
	// start and end are the request's span on its host's wall clock;
	// hasStart is false when the event carried no latency (sendAsync, a
	// server exchange closed on another thread): start is then end.
	start, end          uint64
	hasStart            bool
	reqBytes, respBytes uint64
	stack               []uint64
}

// fileOp is one file event of a kept server request's thread, or a run
// of count repeated ones folded into it (Recorder.File): org, seq, at
// and stack are the first call's, last is the last call's instant,
// bytes and latency are the run's sums. base is the label without an
// offset, what a run folds on; label is what its step shows.
type fileOp struct {
	org     int
	seq     uint64
	at      uint64
	last    uint64
	base    string
	label   string
	count   int
	bytes   uint64
	latency uint64
	thread  string
	carrier bool
	stack   []uint64
}

// Recorder collects a build's conversations. Use: HTTP for every event
// of every segment, Plan, File for every event again (when Wants),
// Methods and Frame, then hand it to New through Input.Conversations.
type Recorder struct {
	renames     map[federate.LocalConn]string
	maxRequests int
	origins     []Origin
	originIdx   map[Origin]int
	reqs        []request
	cut         uint64 // http events past maxRequests
	// Set by Plan.
	tl     federate.Timeline
	links  links
	kept   []int // the conversations' roots, newest first
	more   int   // roots past MaxConversations
	wanted map[threadKey][]int
	procs  map[procKey]bool
	// Set by File.
	fds     map[fdKey]string
	ops     map[int][]fileOp
	moreOps map[int]int
	// Set by Frame: session → method id → frame text.
	frames map[string]map[uint64]string
}

// NewRecorder starts a recording; renames are the merge's
// (federate.Result.Renames), so steps speak merged conn ids.
func NewRecorder(renames map[federate.LocalConn]string) *Recorder {
	return &Recorder{renames: renames, maxRequests: MaxRequests, originIdx: map[Origin]int{},
		fds: map[fdKey]string{}, ops: map[int][]fileOp{}, moreOps: map[int]int{},
		frames: map[string]map[uint64]string{}}
}

// HTTP records ev when it is an http event. It borrows ev (a Source
// callback): everything kept is copied.
func (r *Recorder) HTTP(o Origin, ev *schema.Event) {
	if ev.Event != schema.KindHTTP || ev.HTTP == nil || ev.PID == 0 {
		return
	}
	q := request{org: r.origin(o), seq: ev.Seq, th: threadOf(ev), client: ev.HTTP.Host != "",
		reqSeq: ev.HTTP.ReqSeq, method: ev.HTTP.Method, path: ev.HTTP.Path,
		status: ev.HTTP.Status, start: ev.WallNS, end: ev.WallNS + ev.LatencyNS,
		hasStart: ev.LatencyNS > 0}
	q.thread, q.carrier, q.stack = javaOf(ev)
	q.held = ev.HTTP.Held
	if ev.ConnID != "" {
		q.local = federate.LocalConn{Host: ev.HostID, ConnID: ev.ConnID}
		q.conn = cmp.Or(r.renames[q.local], ev.ConnID)
	}
	if ev.HTTP.ReqBytes != nil {
		q.reqBytes = *ev.HTTP.ReqBytes
	}
	if ev.HTTP.RespBytes != nil {
		q.respBytes = *ev.HTTP.RespBytes
	}
	r.reqs = append(r.reqs, q)
	if len(r.reqs) >= 2*r.maxRequests {
		r.trim()
	}
}

// trim keeps the maxRequests newest records. Run whenever twice as many
// piled up, it keeps memory bounded and ends with the same set a single
// sort of every record would: the order is total.
func (r *Recorder) trim() {
	slices.SortFunc(r.reqs, func(a, b request) int {
		return cmp.Or(cmp.Compare(b.end, a.end), r.refCompare(a, b))
	})
	if len(r.reqs) > r.maxRequests {
		r.cut += uint64(len(r.reqs) - r.maxRequests)
		clear(r.reqs[r.maxRequests:])
		r.reqs = r.reqs[:r.maxRequests]
	}
}

// refCompare orders two records by their raw event: session, seq.
func (r *Recorder) refCompare(a, b request) int {
	return cmp.Or(cmp.Compare(r.origins[a.org].Session, r.origins[b.org].Session),
		cmp.Compare(a.seq, b.seq))
}

// origin interns o.
func (r *Recorder) origin(o Origin) int {
	if i, ok := r.originIdx[o]; ok {
		return i
	}
	r.origins = append(r.origins, o)
	r.originIdx[o] = len(r.origins) - 1
	return len(r.origins) - 1
}

// Plan trims the records, links them and picks the conversations: the
// MaxConversations newest roots. bounds are the merge's clock bounds
// (Result.Clocks): the Timeline orders instants across hosts by them.
func (r *Recorder) Plan(bounds []federate.ClockBound) {
	r.trim()
	edge := r.edge(bounds)
	slices.SortFunc(r.reqs, r.refCompare) // stable indices for the links
	r.tl = federate.NewTimeline(bounds)
	r.links = link(r.reqs, r.tl, edge)
	roots := slices.Clone(r.links.roots)
	slices.SortFunc(roots, func(a, b int) int {
		return cmp.Or(cmp.Compare(r.reqs[b].start, r.reqs[a].start), cmp.Compare(a, b))
	})
	if len(roots) > MaxConversations {
		r.more = len(roots) - MaxConversations
		roots = roots[:MaxConversations]
	}
	r.kept = roots
	r.wanted, r.procs = map[threadKey][]int{}, map[procKey]bool{}
	for _, root := range roots {
		r.links.walk(root, func(i int) {
			if q := r.reqs[i]; !q.client && q.owned() {
				r.wanted[q.th] = append(r.wanted[q.th], i)
				r.procs[procKey{q.th.host, q.th.pid, q.th.inc}] = true
			}
		})
	}
	for th, ws := range r.wanted {
		slices.SortFunc(ws, func(a, b int) int { return cmp.Compare(r.reqs[a].start, r.reqs[b].start) })
		r.wanted[th] = ws
	}
}

// edge is the instant before which a kept request joins no hop, 0 when
// nothing was cut. Past MaxRequests the oldest records are cut by their
// completion on their own host's clock, so a kept request's partner on
// another host may be gone — or a duplicate that would have spoiled its
// pairing. A partner ends after the request starts in true time; on
// another clock that is at most the widest offset any bound allows
// earlier. So only a request starting more than that after the oldest
// kept completion is sure to have every partner kept. Hosts no bound
// relates are assumed within that offset too: their skew is unknown,
// and the merge itself fuses only NTP-close hosts (federate
// DefaultWindow). Call it right after trim, while reqs are newest first.
func (r *Recorder) edge(bounds []federate.ClockBound) uint64 {
	if r.cut == 0 || len(r.reqs) == 0 {
		return 0
	}
	var widest int64
	for _, b := range bounds {
		if _, ok := b.Width(); ok {
			lo, hi := b.Interval()
			widest = max(widest, abs(lo), abs(hi))
		}
	}
	return r.reqs[len(r.reqs)-1].end + uint64(widest) + 1
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// Wants reports whether the file pass has a thread to follow.
func (r *Recorder) Wants() bool { return len(r.wanted) > 0 }

// owned reports whether q's span belongs to its kernel thread alone, so
// that the thread's other calls and file work inside it are q's: a
// start, a held request (no Netty event loop, no async completion), a
// platform thread known by name (a virtual thread's carrier runs other
// virtual threads, and a thread whose record never came may be one) and
// a known tid. Anything less nests nothing — never a guess (task mb4
// review).
func (q request) owned() bool {
	return q.hasStart && q.held && !q.carrier && q.thread != "" && q.th.tid != 0
}

// threadOf is ev's thread.
func threadOf(ev *schema.Event) threadKey {
	return threadKey{host: ev.HostID, pid: ev.PID, inc: ev.PIDInc, tid: ev.TID}
}

// javaOf is ev's Java thread name, carrier flag and stack (at most
// MaxFrames, top first), copied.
func javaOf(ev *schema.Event) (string, bool, []uint64) {
	if ev.Java == nil {
		return "", false, nil
	}
	st := ev.Java.Stack
	return ev.Java.Thread, ev.Java.Carrier, slices.Clone(st[:min(len(st), MaxFrames)])
}
