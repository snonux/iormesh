// Package normalize makes traces comparable across runs and checks them
// against a scenario's declared expectations (PLAN.md M0 "Golden
// semantics", M1). It is cgo-free: the controller runs the same checks.
//
// Normalize cuts a trace to its scenario's marker bracket and rewrites
// every run-specific value — host ids, PIDs, TIDs, PID incarnations, fds,
// dynamic ports, connection ids, sequence numbers and times — to ids
// assigned in order of first appearance, so two runs of one scenario
// compare equal inside the bracket.
//
// Check asserts declared ⊆ collected: every declared event appears in
// its declared per-thread order, every declared edge exists, with the
// expectation's symbols ($p1, $t1, $fd1, $port1, $h1, $c1) bound
// consistently, and no must_not_appear string occurs anywhere.
package normalize
