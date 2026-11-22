// Package servicedb is the controller's service-interaction database
// (PLAN.md M4 "Service-interaction database", decision #24): one sqlite
// file under ~/.local/share/iormesh/, next to the collected traces, that
// every build folds the merged mesh into and `iormeshctl services` reads.
//
// What it holds, per observed TCP connection (table conn): the client
// app (project, host, app identity), the server endpoint (hostname from
// the client's DNS lookups or its HTTP requests, IP, port), the merge's
// verdict (fused, or unresolved with the merge's reason), protocol hints
// (plain or TLS, HTTP host and path template per request — ids folded
// to {id}, internal/pathtmpl; table http_request) and
// the activity window. Naming rules (table naming_rule) map a hostname
// or IP glob plus a port to a service name; they are human input, which
// the build never touches.
//
// Three properties shape the design:
//
//   - Idempotent: a row is keyed by its anchor end — the client half's
//     host and host-local conn id when the client was traced, else the
//     server half's — which is stable across rebuilds, and every merged
//     value is a MIN, MAX or keep-if-unknown; request counts are kept
//     per source (the trace unit read) and a source's import replaces
//     its earlier counts, so importing the same mesh twice changes
//     nothing.
//   - Additive: a later import never forgets what an earlier one saw. A
//     build over fewer segments (the operator deleted old ones) leaves
//     rows, windows and the deleted segments' request counts as they
//     were, so a connection's requests spread over several segments
//     still add up exactly. The client's and the server's view of the
//     same requests are stored apart and the reads count the larger,
//     never both. A fused verdict never reverts to unresolved, and a
//     connection that fuses once its far end is traced takes over the
//     row (window, TLS hint, requests) its server end had alone.
//   - Retroactive names: services are resolved at read time by joining
//     the rules against the endpoints, so a new rule names every past
//     connection without rewriting a row.
//
// Redaction happened upstream (the tracer masks query values and
// headers, decision #9); the database still holds hostnames, addresses
// and paths, so the file is created 0600 and a group- or world-readable
// one is refused. It is cgo-free (modernc.org/sqlite, a pure-Go
// sqlite) because iormeshctl cross-compiles with CGO_ENABLED=0.
package servicedb
