package federate

import "context"

// Transport is how the controller reaches a tracer host (docs/milestones.md M4
// "No management plane", decision #12; docs/implementation.md M7). M4 codes
// against it from day one with SSH as the only implementation, so M7's
// kubectl transport slots in without touching deploy or collect.
type Transport interface {
	// Run executes argv on the remote host and returns its stdout. Each
	// element arrives as one argument, whatever it contains: an
	// implementation never joins untrusted text into a command string
	// unquoted. A command that ran and exited non-zero returns an error
	// wrapping *exec.ExitError with the remote status, so a caller can
	// tell `test` saying "no" (status 1) from a host it never reached.
	Run(ctx context.Context, argv ...string) ([]byte, error)
	// Fetch moves the segment files directly in remoteDir (non-dot
	// *.parquet; no subdirectory, symlink or other file) into localDir:
	// each file is deleted on the remote only after it transferred (the ready/
	// contract, docs/milestones.md M4 "Collect"), so a failed fetch loses nothing
	// and a repeated one picks up only what is new.
	Fetch(ctx context.Context, remoteDir, localDir string) error
	// Push copies the local files into remoteDir, which must exist; a
	// relative remoteDir is under the login user's home. It is deploy's
	// staging step (docs/milestones.md M4 "Deploy"): the files land as the login
	// user's own, never as root and never under /opt, and the server's
	// `iormesh upgrade` installs them only after verifying them itself.
	// A file already there with the same contents is skipped, so a
	// re-deploy of the same build transfers nothing.
	Push(ctx context.Context, remoteDir string, localFiles ...string) error
}
