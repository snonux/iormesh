// Package redact is the one redaction policy for everything iormesh records
// that may carry a secret (PLAN.md decisions #9 and #22;
// docs/implementation.md M2 step 8; docs/schema.md "Redaction"): exec argv
// now, HTTP headers, URIs and query parameters in M3, and whatever M5 adds.
//
// The policy is a denylist: a value is masked when its key — a command-line
// option, a KEY=VALUE name, a header or a query-parameter name — matches a
// deny glob, and URL userinfo is always masked. Masking keeps the structure
// ("-Dpassword=***", "--token ***", "https://user:***@host/?q=***"), so a
// reader still sees which option was given, never its value.
//
// A denylist is best effort by nature: a secret passed under an innocuous
// name ("--db x"), inside a JSON blob, or as a bare positional argument is
// not recognized. The defaults therefore err on the side of masking too
// much (a "--password-stdin" flag masks the next argument), never too
// little, and environment variables are never captured at all (decision #9:
// env off), so a secret passed the conventional way never reaches the
// policy.
//
// It is cgo-free and has no dependencies beyond the standard library, so
// the correlator, the controller and any later component can share it.
package redact
