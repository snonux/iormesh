package schema

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
)

// Node types. The set only grows (service, pod, … arrive with later
// milestones), and Graph.Validate rejects types not listed here.
const (
	NodeHost    = "host"
	NodeProcess = "process"
	NodeFile    = "file"
	// NodeSocket is one connection — the pair, not one end of it — so
	// both processes of a fused pair point at the same node: a TCP
	// connection, a unix stream connection, a UDP flow (one local/remote
	// address pair) or a pipe or socketpair (M2 step 7). Its id is
	// "conn:<conn_id>".
	NodeSocket = "socket"
	// NodeApp is the identity a pattern-bound session gives every process
	// incarnation its pattern matched (M2 step 3): an app restarted with a
	// new PID is a new process node, linked to the same app node. Its id
	// is "app:<project>/<pattern>" (AppNodeID).
	NodeApp = "app"
)

// Edge types.
const (
	EdgeRunsOn    = "runs_on"   // process → host
	EdgeOpened    = "opened"    // process → file
	EdgeConnected = "connected" // process → socket; carries a State
	// EdgeIncarnation links an app to one process incarnation of it.
	EdgeIncarnation = "incarnation" // app → process
	// EdgeForked links a traced process to a child it forked (M2 step
	// 6): the child starts with a copy of the parent's fd table, and the
	// normalizer's bracket cut gives a child forked inside a bracket that
	// bracket (internal/normalize, cut).
	EdgeForked = "forked" // process → process
	// EdgeSignaled links a traced process to a traced process it sent a
	// signal (M2 step 7, signal family): "who touched the process".
	EdgeSignaled = "signaled" // process → process
)

// Edge states, set on connected edges only. A connection is fused when
// both of its ends were traced and joined; unresolved when only one end
// was seen (the peer is untraced, or the join failed) — never guessed.
const (
	StateFused      = "fused"
	StateUnresolved = "unresolved"
)

// Id prefixes. Ids are derived from the traced entity, never counters,
// so two exports of one trace — or two hosts' exports of one connection —
// name the same thing the same way.
const (
	prefixHost    = "host:"
	prefixProcess = "proc:"
	prefixFile    = "file:"
	prefixConn    = "conn:"
	prefixApp     = "app:"
)

// Graph is the merged, queryable form: built per host by the correlator
// (M1), fused across hosts by the merge (M4). Exports are deterministic:
// call Sort before writing, and Validate rejects an unsorted graph.
type Graph struct {
	SchemaVersion uint32 `json:"schema_version"`
	Nodes         []Node `json:"nodes"` // sorted by (Type, ID)
	Edges         []Edge `json:"edges"` // sorted by (Type, From, To)
}

// Node is one graph vertex. Attrs is a map, which encoding/json writes
// with sorted keys, so it never makes an export nondeterministic.
type Node struct {
	Type  string            `json:"type"`
	ID    string            `json:"id"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Edge is one typed, directed graph edge between two node ids.
type Edge struct {
	Type  string            `json:"type"`
	From  string            `json:"from"`
	To    string            `json:"to"`
	State string            `json:"state,omitempty"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// HostNodeID names a host by its host_id: a machine id, or "pod-<uid>"
// for an in-pod tracer (the pod is the host, PLAN.md M7).
func HostNodeID(hostID string) string { return prefixHost + hostID }

// ProcessNodeID names one process incarnation: the incarnation keeps a
// reused PID from merging with its predecessor.
func ProcessNodeID(hostID string, pid uint32, pidInc uint64) string {
	return fmt.Sprintf("%s%s/%d@%d", prefixProcess, hostID, pid, pidInc)
}

// FileNodeID names a file by host and path: "file:<host_id>:<path>". The
// same path on two hosts is two files. The colon keeps the host and the
// absolute path apart without a double slash, and lets the normalizer
// swap each segment for a symbol on its own — goldens declare
// "file:$h1:$tmp/file-basic.dat".
func FileNodeID(hostID, path string) string {
	return prefixFile + hostID + ":" + path
}

// AppNodeID names an app by the project and the target pattern of the
// session that matched it: "app:<project>/<pattern>". Neither part is
// run-specific, so the id survives restarts, re-runs and normalization
// unchanged, and two hosts tracing one project with one pattern name the
// same app. A project name holds no '/', so the first one splits the id.
func AppNodeID(project, pattern string) string {
	return prefixApp + project + "/" + pattern
}

// SocketNodeID names a connection by its conn_id (Event.ConnID; across
// hosts the merge joins by the TCP ISN pair, decision #26).
func SocketNodeID(connID string) string { return prefixConn + connID }

// Sort puts nodes and edges into canonical export order, in place (the
// slices share their backing arrays, so a value receiver suffices). Go map
// iteration order must never reach an output file; builders collect into
// maps freely and call Sort once before export.
func (g Graph) Sort() {
	slices.SortFunc(g.Nodes, compareNodes)
	slices.SortFunc(g.Edges, compareEdges)
}

// MarshalJSON writes nil node and edge slices as [] rather than null:
// readers require both keys as arrays, and an empty graph is still a
// graph, not a missing one.
func (g Graph) MarshalJSON() ([]byte, error) {
	type plain Graph // drops this method, so json.Marshal does not recurse
	p := plain(g)
	if p.Nodes == nil {
		p.Nodes = []Node{}
	}
	if p.Edges == nil {
		p.Edges = []Edge{}
	}
	return json.Marshal(p)
}

func compareNodes(a, b Node) int {
	return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID))
}

func compareEdges(a, b Edge) int {
	return cmp.Or(cmp.Compare(a.Type, b.Type),
		cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
}
