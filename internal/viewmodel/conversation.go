package viewmodel

import "time"

// Conversations (V2, docs/visualization.md; PLAN.md M5 "Sequence
// view"): one request entering the mesh and everything it caused, as a
// list of steps in happens-before order. The model renders the
// correlated trace instead of re-inferring it, and says how each step's
// place is known — never a silent guess:
//
//   - A hop is paired when the client's and the server's http events
//     carry the same merged conn id and req_seq (docs/schema.md "HTTP
//     events"): the request's bytes went from the one thread to the
//     other, a causal edge.
//   - A client request nests in a server request when both ran on the
//     same kernel thread, the server's span holds the client's, and the
//     thread worked on each alone (request.owned: http.held — a
//     synchronous library, never Netty's event loop — on a platform
//     thread known by name). A virtual thread's carrier (java.carrier)
//     runs other virtual threads, an event loop other channels, so
//     such a span nests nothing and owns no file work.
//   - An unpaired client request (HTTP/2, a lost record) is joined to
//     an unpaired server request on the same fused connection only when
//     the merge's clock bounds prove the server's span inside the
//     client's (federate.Timeline), each is the other's only such
//     match, and no unpaired end of the connection lacks a start; the
//     hop is then time-ordered, not causal.
//   - Past MaxRequests, a request starting near the cut joins no hop
//     (Recorder.edge): its partner may be among the records cut.
//   - Everything else stays apart: an unpaired client request ends at
//     its peer, and the server request that may have answered it is a
//     conversation of its own.
//
// Within a server request, the thread's file work (opens, reads,
// writes, renames, unlinks, …) is interleaved in its own clock's order
// with the calls it made.

// Step kinds.
const (
	StepRequest  = "request"
	StepResponse = "response"
	StepFile     = "file"
)

// Orderings: how a step's place after the step before it is known.
const (
	// OrderCausal: thread order (the same kernel thread, synchronously)
	// or a paired send→receive edge.
	OrderCausal = "causal"
	// OrderTime: no causal edge, but the timestamps prove it came after
	// the step before — on one host's clock, or across hosts by the
	// merge's clock bounds.
	OrderTime = "time-ordered"
	// OrderNone: neither — the clock bounds cannot prove the order (no
	// bound covers the two hosts, or the instants lie closer than the
	// bound is wide). The step is placed by the conversation's structure
	// only, and the model says so.
	OrderNone = "unordered"
)

// Hops: how a request step's two ends were joined.
const (
	// HopPaired: same merged conn id and req_seq on both ends.
	HopPaired = "paired"
	// HopClock: an unpaired hop the clock bounds proved (see above).
	HopClock = "clock"
	// HopUnpaired: no server end was joined — HTTP/2, a lost record, an
	// untraced server, or bounds too wide to prove the match.
	HopUnpaired = "unpaired"
	// HopAsync: a request recorded without its start (sendAsync): it
	// cannot nest and cannot be joined by time.
	HopAsync = "async"
	// HopUntraced: a server request whose client no trace saw.
	HopUntraced = "untraced"
)

// Conversation caps keep the model a summary (docs/visualization.md
// "Bounded"): what is cut is counted.
const (
	// MaxConversations is how many conversations the model keeps, newest
	// first; MoreConversations counts the rest.
	MaxConversations = 100
	// MaxSteps caps one conversation's steps; MoreSteps counts the rest.
	MaxSteps = 200
	// MaxFrames caps a step's Java stack, top first.
	MaxFrames = 12
	// MaxRequests caps the http events a build reads into conversations:
	// the newest by completion are kept, the rest counted in
	// Health.RequestsCut. The records come from servers; their number
	// must not decide the controller's memory.
	MaxRequests = 1 << 18
	// maxOpenFiles caps the fd → path table the file pass keeps for the
	// processes that served a kept request; past it new opens are not
	// followed (their reads and writes are left out, as a file never
	// opened in the trace is).
	maxOpenFiles = 1 << 20
)

// Conversation is one line of the end-goal picture.
type Conversation struct {
	// ID is the conversation's number in the model, newest first
	// ("0001"): what `show sequence` and the site's links take.
	ID string `json:"id"`
	// Start is the entering request's start on its own host's clock.
	// The list is sorted by it (newest first) for presentation only:
	// raw timestamps of two hosts order nothing (PLAN.md M4 "Clock
	// reconciliation").
	Start time.Time `json:"start"`
	// Entry is the service the request entered, Label the request
	// ("GET /checkout").
	Entry string `json:"entry"`
	Label string `json:"label"`
	// Hops counts the request steps; Services are the lifelines, in the
	// order they first appear.
	Hops      int      `json:"hops"`
	Services  []string `json:"services"`
	Steps     []Step   `json:"steps"`
	MoreSteps int      `json:"more_steps"`
}

// Step is one arrow or note of a conversation.
type Step struct {
	Kind string `json:"kind"` // request | response | file
	// Service is the lifeline the step starts on; Host its FQDN.
	Service string `json:"service"`
	Host    string `json:"host"`
	// Thread is the Java thread name, else "tid N"; "" for the far end
	// of an unpaired hop or an untraced client.
	Thread string `json:"thread"`
	// Carrier flags a virtual thread's I/O named by its carrier.
	Carrier bool `json:"carrier,omitempty"`
	// To is the peer lifeline of a request or response.
	To string `json:"to,omitempty"`
	// Label says what happened: "GET /orders/42", "200",
	// "rename /d/order-42.json.tmp -> /d/order-42.json".
	Label   string        `json:"label"`
	Bytes   uint64        `json:"bytes"`
	Latency time.Duration `json:"latency_ns"`
	// Ordering is causal, time-ordered or unordered (Order*).
	Ordering string `json:"ordering"`
	// Hop says how a request's ends were joined (Hop*); "" otherwise.
	Hop string `json:"hop,omitempty"`
	// Stack is the Java stack, top first, as "class#method(file:line)";
	// at most MaxFrames.
	Stack []string `json:"stack"`
	// Ref names the step's raw event for `iormeshctl query -ref`.
	Ref string `json:"ref"`
}
