// Package target is the session's target spec (PLAN.md M2 "Target spec",
// decision #23; docs/implementation.md M2 step 2): which processes a
// tracing session binds to — explicit PIDs, a glob over the command line
// matched on exec, or the owner of a listening TCP port — and the /proc
// reads that resolve a spec and tell whether a target still lives.
//
// It is cgo-free: the BPF side (internal/bpf) reports execs, and the
// caller (internal/cli) asks Spec.Matches about each one.
package target
