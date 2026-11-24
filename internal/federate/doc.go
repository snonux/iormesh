// Package federate is the controller's cross-host side (docs/milestones.md
// M4): the server registry (registry.go), the Transport that reaches servers
// (transport.go, ssh.go), trace sync and the offline merge.
//
// The merge (merge.go, fuse.go, half.go, key.go, source.go) joins the
// traces collected from several hosts — and several projects — into one
// graph, offline (docs/milestones.md M4 "Build"). It fuses the two ends of a
// TCP connection traced on different hosts into one cross-host edge by the
// TCP initial-sequence-number pair (decision #26, spike S2), which NAT
// never rewrites; an end without exactly one partner stays unresolved,
// never fused by a guess.
//
// Its inputs are Sources: NDJSON trace directories, or the collected
// parquet segments of the controller's store (segments.go), whose
// sessions' graphs are rebuilt from their events (rebuild.go; the
// server keeps none, decision #3).
//
// The fused connections then bound each host pair's clock offset
// (clock.go, bound.go: a send always precedes its receive, docs/milestones.md
// M4 "Clock reconciliation"), and order.go orders instants across hosts by
// those bounds alone, never by comparing two clocks' raw timestamps.
//
// The package is cgo-free on purpose: iormeshctl runs on a laptop
// (darwin or linux, CGO_ENABLED=0), so nothing here may import the
// tracer's libbpfgo side.
package federate
