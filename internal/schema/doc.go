// Package schema is the trace output contract: one Event envelope, one
// Graph export, and the validation both pass before anything downstream
// trusts them. It is the Go form of docs/output-contract.md's "Trace output
// contract".
//
// The package is cgo-free on purpose: the tracer (cgo, libbpfgo) writes
// these types and the controller (CGO_ENABLED=0, darwin too) reads them,
// so it must never import anything that pulls in cgo.
//
// Version 1 was drafted in M1 and is frozen: spikes S1 (Java ↔ syscall
// join) and S2 (cross-host join) settled the java and conn_id shapes
// (docs/spikes/), M2's acceptance the rest of the traced surface, and
// M3's acceptance http, dns, otel and the Java tables. Fields are only
// ever appended, and every appended field is optional (omitempty), so a
// reader of an older file sees the new ones as absent, never as a
// different meaning — ior's parquet rule.
package schema

// Version is the schema_version every event and graph of this schema
// carries. Readers reject any other value instead of guessing.
const Version = 1
