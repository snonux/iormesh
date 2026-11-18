package schema

import (
	"cmp"
	"fmt"
	"slices"
)

// Node types. The set only grows (app, service, pod, … arrive with later
// milestones), and Graph.Validate rejects types not listed here.
const (
	NodeHost    = "host"
	NodeProcess = "process"
	NodeFile    = "file"
	// NodeSocket is one TCP connection — the socket pair, not one end of
	// it — so both processes of a fused pair point at the same node. Its
	// id is "conn:<conn_id>".
	NodeSocket = "socket"
)

// Edge types.
const (
	EdgeRunsOn    = "runs_on"   // process → host
	EdgeOpened    = "opened"    // process → file
	EdgeConnected = "connected" // process → socket; carries a State
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

// HostNodeID names a host by its machine id.
func HostNodeID(hostID string) string { return prefixHost + hostID }

// ProcessNodeID names one process incarnation: the incarnation keeps a
// reused PID from merging with its predecessor.
func ProcessNodeID(hostID string, pid uint32, pidInc uint64) string {
	return fmt.Sprintf("%s%s/%d@%d", prefixProcess, hostID, pid, pidInc)
}

// FileNodeID names a file by host and path; the same path on two hosts
// is two files.
func FileNodeID(hostID, path string) string {
	return prefixFile + hostID + "/" + path
}

// SocketNodeID names a connection by its conn_id (provisional until S2,
// like Event.ConnID).
func SocketNodeID(connID string) string { return prefixConn + connID }

// Sort puts nodes and edges into canonical export order, in place (the
// slices share their backing arrays, so a value receiver suffices). Go map
// iteration order must never reach an output file; builders collect into
// maps freely and call Sort once before export.
func (g Graph) Sort() {
	slices.SortFunc(g.Nodes, compareNodes)
	slices.SortFunc(g.Edges, compareEdges)
}

func compareNodes(a, b Node) int {
	return cmp.Or(cmp.Compare(a.Type, b.Type), cmp.Compare(a.ID, b.ID))
}

func compareEdges(a, b Edge) int {
	return cmp.Or(cmp.Compare(a.Type, b.Type),
		cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
}
