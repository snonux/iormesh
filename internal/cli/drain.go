package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"iormesh/internal/k8s"
	"iormesh/internal/segment"
)

// drainPollEvery is how often a run waiting at its disk limit
// (awaitDrain) checks whether collect has drained the trace dir: the
// watchdog's own pace, a walk of a few hundred files at most. A variable
// so tests need not wait seconds.
var drainPollEvery = 2 * time.Second

// awaitDrain keeps a run in a pod alive while its trace dir is over the
// disk watchdog's limits — found so at preflight, or the session just
// ended with disk_limit (task ki4). Exiting there would crash-loop the
// pod: the kubelet restarts a sidecar's container, the new run fails
// its preflight on the same full dir, the restarting container makes
// the pod NotReady (out of its Services' endpoints), and collect cannot
// exec into a stopped container to drain it — the segments would stay
// stranded until the pod is deleted, and lost with it. So the run waits
// as -linger does: runTrace has returned, the tracer is closed (no
// probe, no map) and the last segment published; only an idle process
// stays, for `iormesh ready` to run beside. It returns once the dir is
// back under its limits (collect's `ready -remove` deleted what it
// fetched; the kubelet then restarts the container into a fresh
// session) or on SIGTERM. A run outside a pod, a run without a trace
// dir, one ended by a signal and one whose dir is within its limits
// return at once, as before.
func awaitDrain(ctx context.Context, cfg runConfig, stderr io.Writer) {
	wd := cfg.watchdog()
	if wd == nil || ctx.Err() != nil || !runsInPod() {
		return
	}
	err := wd.Check()
	if !errors.Is(err, segment.ErrDiskLimit) {
		return
	}
	_, _ = fmt.Fprintf(stderr, "iormesh: %v; BPF released, waiting for collect to drain it (or SIGTERM)\n", err)
	tick := time.NewTicker(drainPollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if err := wd.Check(); !errors.Is(err, segment.ErrDiskLimit) {
			_, _ = fmt.Fprintln(stderr, "iormesh: trace dir drained under its limits; exiting")
			return
		}
	}
}

// runsInPod reports whether the run is a pod's: all three downward-API
// variables set (k8s.PodFromEnv). parseRunFlags has refused a partial
// or malformed set already, so an error here only means "not a pod".
func runsInPod() bool {
	_, ok, err := k8s.PodFromEnv(podEnv)
	return ok && err == nil
}
