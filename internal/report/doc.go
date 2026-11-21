// Package report builds the per-run summary rollup (PLAN.md M5 "Plus
// the per-run summary/report export", "Trace output contract" → "Summary
// rollup"): one entry per traced process — the files it read and wrote
// and what it did to them, its connections with the peers' names, its
// thread table with each thread's requests in order, and its top Java
// methods by I/O. It is the cheap context an LLM (or a human) reads
// before slicing the event stream with `iormeshctl query`, and the one
// document the LLM eval (internal/eval) hands the model besides the
// query tool.
//
// `iormeshctl build` feeds a Collector the events the merge reads anyway
// (no extra pass over the segments), then Build turns it into a Report
// with the merge's conn-id renames and the service database's endpoints;
// build writes it as report.json next to model.json, and
// `iormeshctl show report` renders it as text (WriteText) after the
// run summary.
//
// Rules, the view model's (internal/viewmodel) applied to the rollup:
//
//   - Deterministic: the same store, database and methods tables give
//     the same Report byte for byte — no maps reach the output, no
//     clocks, no local paths. Lists keep the order the events came in
//     (the store's: projects, hosts, sessions, segments in seq order),
//     which is also the order a reader wants: the first file a process
//     opened first. Only which files survive a process's file cap is
//     ranked by the work done on them (topFiles, task vg4).
//   - Bounded: every list is capped and says how much it cut (More*);
//     what the collector tracks while reading is capped too
//     (maxTracked), so a hostile trace cannot grow it without bound —
//     events past the cap are counted in Report.Untracked, never
//     silently dropped.
//   - Honest: a connection whose other end was not traced says so
//     (PeerState), I/O without a Java stack is counted as such, and a
//     method is named only by the rule the goldens use
//     (normalize.CausingMethod: the innermost non-JDK frame).
//
// The rollup is metadata only, as the trace is: paths, sizes, request
// lines (already redacted by the tracer), never payload.
//
// It is cgo-free: iormeshctl builds it on a laptop (CGO_ENABLED=0).
package report
