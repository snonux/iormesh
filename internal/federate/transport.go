// Package federate is the controller's cross-host side (PLAN.md M4): the
// server registry (registry.go), the Transport that reaches servers
// (transport.go, ssh.go), trace sync and the offline merge. It is cgo-free,
// shared with iormeshctl.
package federate

import "context"

// Transport is how the controller reaches a tracer host (PLAN.md M4 "No
// management plane", decision #12; docs/implementation.md M7). M4 codes
// against it from day one with SSH as the only implementation, so M7's
// kubectl transport slots in without touching deploy or collect.
type Transport interface {
	// Run executes argv on the remote host and returns its stdout. Each
	// element arrives as one argument, whatever it contains: an
	// implementation never joins untrusted text into a command string
	// unquoted.
	Run(ctx context.Context, argv ...string) ([]byte, error)
	// Fetch moves the files under remoteDir into localDir: each file is
	// deleted on the remote only after it transferred (the ready/
	// contract, PLAN.md M4 "Collect"), so a failed fetch loses nothing
	// and a repeated one picks up only what is new.
	Fetch(ctx context.Context, remoteDir, localDir string) error
}
