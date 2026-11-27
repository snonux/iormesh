// Package viewmodel is everything a human-facing surface may show of a
// consolidation run (docs/visualization.md, "One view model, three
// renderers"). `iormeshctl build` computes it once per run from the
// merge, the per-session tallies and the service database; the plain
// text of `build` and `show` (summary.go) renders it, the TUI
// (internal/tui, task ob4) explores it and the local site (internal/site,
// task nb4) embeds it, so no two surfaces can disagree.
//
// Step V1 (M4) built run, health, services and edges; V2 (M5, task mb4)
// appends the conversations (conversation.go): request chains across
// the fused connections in happens-before order, each thread's file work
// interleaved, each step on the lifelines of the threads at its ends;
// task xb4 appended the routed edges and their flags (routes.go: the
// route map's Services and ingress routes, docs/milestones.md M7, decision
// #15); task 0f4 appended the thread stories (stories.go: one thread's steps
// across the conversations, `show thread`) and the processes
// (process.go: per host, its threads, files and sockets, projected from
// the run's rollup, internal/report).
//
// Rules the model keeps:
//
//   - Sorted and deterministic: the same inputs give the same model,
//     byte for byte as JSON — no maps, no build time, no local paths.
//   - Bounded: a summary, never raw events; long per-edge lists are cut
//     and say so (TopPaths, MorePaths).
//   - Honest: unresolved edges, bad segments, sessions that never
//     recorded their end and empty clock bounds are fields, so a
//     renderer has to show them.
//
// It is cgo-free: iormeshctl builds it on a laptop (CGO_ENABLED=0).
package viewmodel

import "time"

// Edge states. An edge between two services aggregates connections, and
// each connection is fused (both ends traced and joined) or not; Mixed
// says the edge has some of each, rather than letting one hide the
// other.
const (
	StateFused      = "fused"
	StateUnresolved = "unresolved"
	StateMixed      = "mixed"
)

// MaxTopPaths caps a service's request paths (docs/visualization.md:
// "top N paths per edge"); the rest is counted in MorePaths.
const MaxTopPaths = 10

// Model is one consolidation run's view (V1).
type Model struct {
	Run      Run       `json:"run"`
	Health   Health    `json:"health"`
	Services []Service `json:"services"`
	Edges    []Edge    `json:"edges"`
	// Conversations are the newest MaxConversations request chains,
	// newest first; MoreConversations counts the older ones left out.
	Conversations     []Conversation `json:"conversations"`
	MoreConversations int            `json:"more_conversations"`
	// Threads are the stories of the conversations' threads, sorted by
	// lifeline.
	Threads []ThreadStory `json:"threads"`
	// Processes are the traced process incarnations in the rollup's
	// order (project, host, pid, incarnation); MoreProcesses counts those the rollup cut.
	Processes     []Process `json:"processes"`
	MoreProcesses int       `json:"more_processes"`
	// Untracked is the rollup's report.Report.Untracked: events past its
	// tracking cap, so processes, files, threads and connections first
	// seen after it are missing. Not zero, every processes view says the
	// list is partial (PartialLine).
	Untracked uint64 `json:"untracked"`
	// Routes are the route map's edges (routes.go, docs/milestones.md M7):
	// ClusterIP connections with their Service named, and untraced Services and
	// ingress FQDNs completed to their workload; empty without a map.
	Routes []Route `json:"routes"`
}

// Run is what the build read and what the merge made of it.
type Run struct {
	// Projects are the projects built together, sorted.
	Projects []string `json:"projects"`
	// Hosts are the store's host directories (registry names), sorted.
	Hosts    []string `json:"hosts"`
	Sessions int      `json:"sessions"`
	// Segments counts the events segments merged; bad ones are in
	// Health.BadSegments, not here.
	Segments int    `json:"segments"`
	Events   uint64 `json:"events"`
	// First and Last bound the events' wall clocks (UTC); zero without
	// events.
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
	// FusedConns and UnresolvedConns count this build's cross-trace TCP
	// connections: joined by the merge, and halves it could not join.
	// PendingConns are the halves it could not join yet: made in the
	// build's last minute, their far end maybe in a segment not
	// collected (federate.ReasonPending); not counted as unresolved.
	FusedConns      int `json:"fused_conns"`
	UnresolvedConns int `json:"unresolved_conns"`
	PendingConns    int `json:"pending_conns,omitempty"`
	// GraphNodes and GraphEdges size the merged graph export.
	GraphNodes int `json:"graph_nodes"`
	GraphEdges int `json:"graph_edges"`
	// Window is a windowed build's window as build echoes it, "FROM ..
	// TO" in RFC 3339 UTC with "(open)" for a side not given; empty (and
	// absent from the JSON) for a full build. The build selects whole
	// segments overlapping it, so First and Last can lie outside it. It
	// is how the site and the TUI say which window they show (task hk4).
	Window string `json:"window,omitempty"`
}

// Health is what the run could not see, or could not see well.
type Health struct {
	// Lost counts records lost to the ring buffer or the decoder (drop
	// events without a reason).
	Lost uint64 `json:"lost"`
	// SkippedRuns counts BPF program runs the kernel skipped: possible
	// loss, never added to Lost (docs/output-contract.md).
	SkippedRuns uint64 `json:"skipped_runs"`
	// Unattributed counts folded calls no single fd explained.
	Unattributed uint64 `json:"unattributed"`
	// AttachGap counts records a pattern-matched process produced before
	// the tracer attached to it.
	AttachGap uint64 `json:"attach_gap"`
	// RequestsCut counts the http events past MaxRequests, the oldest by
	// completion, that no conversation was built from.
	RequestsCut uint64 `json:"requests_cut"`
	// Sessions says how each tracer run ended.
	Sessions []Session `json:"sessions"`
	// Clocks summarizes each traced host pair's clock bounds.
	Clocks []Clock `json:"clocks"`
	// BadSegments were skipped whole; Ignored are stray store entries.
	BadSegments []BadSegment `json:"bad_segments"`
	Ignored     []string     `json:"ignored"`
	// RouteFlags are where the route map and the traces disagree (a stale
	// endpoint list, a contradicted route) or what the map left open.
	RouteFlags []RouteFlag `json:"route_flags"`
}

// Session is one tracer run on one host.
type Session struct {
	Project string `json:"project"`
	Host    string `json:"host"` // the store's host directory
	FQDN    string `json:"fqdn"`
	// HostID is the host id its events carry (/etc/machine-id, a pod's
	// own id): what scopes a loopback or link-local endpoint
	// (servicedb Endpoint.Host), which the site's host filter maps back
	// to the host directory through it.
	HostID string `json:"host_id"`
	ID     string `json:"id"`
	// Segments and Events count what was merged of it.
	Segments int       `json:"segments"`
	Events   uint64    `json:"events"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	// End is the end event's reason; "" when no end was collected (the
	// run is still going, or was cut short).
	End string `json:"end"`
}

// Clock summarizes the clock bounds of one host pair (federate
// ClockBound, one per window): how far the two hosts' timestamps can be
// compared.
type Clock struct {
	// HostA and HostB are FQDNs (the host ids when no session named
	// them), HostA's id sorting first as in federate.
	HostA string `json:"host_a"`
	HostB string `json:"host_b"`
	// Windows have a bound; of them Empty contradict themselves and
	// OneSided have only one side.
	Windows  int `json:"windows"`
	Empty    int `json:"empty"`
	OneSided int `json:"one_sided"`
	// MinWidth and MaxWidth range over the two-sided, non-empty windows'
	// widths (the drift allowance included); zero when there is none.
	MinWidth time.Duration `json:"min_width_ns"`
	MaxWidth time.Duration `json:"max_width_ns"`
}

// BadSegment is a segment the build skipped, and why.
type BadSegment struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// Service is a server endpoint label (a service name from the naming
// rules, else hostname:port, else ip:port) or a client known only as an
// app or process name.
type Service struct {
	Name string `json:"name"`
	// Named is true when a naming rule gave the name.
	Named bool `json:"named"`
	// Endpoints are the endpoints behind the name (hostname or IP, and
	// port), sorted; none for a client-only service.
	Endpoints  []string `json:"endpoints"`
	Conns      int64    `json:"conns"`
	Unresolved int64    `json:"unresolved"`
	// TLS is "tls", "plain", "mixed" or "" (unknown).
	TLS       string     `json:"tls"`
	Requests  int64      `json:"requests"`
	TopPaths  []PathStat `json:"top_paths"`
	MorePaths int        `json:"more_paths"`
}

// PathStat counts one HTTP method and path template: the redacted path
// with its ids folded (/orders/{id}) or the operator's template, as the
// service database keys requests (internal/pathtmpl, task xh4).
type PathStat struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Count  int64  `json:"count"`
}

// Edge is who talks to whom: a client service to a server service.
type Edge struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	State      string    `json:"state"`
	Conns      int64     `json:"conns"`
	Unresolved int64     `json:"unresolved"`
	First      time.Time `json:"first"`
	Last       time.Time `json:"last"`
}
