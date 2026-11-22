// Package query cuts the merged trace into a slice a human or an LLM can
// read whole (PLAN.md M5 "Query / slice interface", task pb4):
// `iormeshctl query` filters the controller's collected segments by time
// window, host, app, thread, connection, file and Java method — and, by
// exact value, event kind, pid, errno and OpenTelemetry trace id (task
// wh4) — and writes
// the matching events as schema NDJSON (docs/schema.md), at most
// MaxBytes of it. A cut result ends in an explicit truncation record,
//
//	{"schema_version":1,"event":"truncated","args":{"emitted":N,"remaining":M}}
//
// so a reader always knows it saw a partial answer and how much it
// missed. An event stream at syscall frequency never fits a context
// window; the slice is the unit that does.
//
// The package is the pure part: Filter (what the flags say, validated),
// Run (one or two passes over the sources, in their order) and the
// capped writer. Loading the store — every segment checked in a
// memory-capped child, the merge that gives connections their merged
// ids, the methods tables -method needs — is internal/ctl's, which hands
// Run only what passed those checks.
//
// Determinism: the sources are read in the order given (ctl passes them
// sorted by store name, each session's segments in seq order), every
// filter is a pure function of the event and of state built from earlier
// events in that order, and the output is the schema's canonical
// encoding, so the same store and flags give the same bytes.
//
// Terminal safety: every line is escaped by termsafe.JSON after
// encoding — the strings in it came from servers — and stays valid JSON
// that decodes to the same event. The package is cgo-free
// (internal/buildgate checks iormeshctl's closure).
package query
