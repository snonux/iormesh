// Package schema is the trace output contract: one Event envelope, one
// Graph export, and the validation both pass before anything downstream
// trusts them. It is the Go form of PLAN.md's "Trace output contract".
//
// The package is cgo-free on purpose: the tracer (cgo, libbpfgo) writes
// these types and the controller (CGO_ENABLED=0, darwin too) reads them,
// so it must never import anything that pulls in cgo.
//
// Version 1 is a draft (M1). It is not frozen until spikes S1 (Java ↔
// syscall join) and S2 (cross-host join) have reported; the fields they
// own are marked provisional where they are declared. Fields are only
// ever appended, and every appended field is optional (omitempty), so a
// reader of an older file sees the new ones as absent, never as a
// different meaning — ior's parquet rule.
package schema

// Version is the schema_version every event and graph of this draft
// carries. Readers reject any other value instead of guessing.
const Version = 1
