package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"iormesh/internal/bpf"
)

// openTracer loads the BPF side (bpf.Open). A variable so a test can run
// a whole session — and its linger — with a fake tracer, no kernel.
var openTracer = func(cfg bpf.Config) (tracer, error) { return bpf.Open(cfg) }

// runSession is run once its flags are parsed: the CPU profile around
// the trace, then, with -linger, the wait (linger); without it, a run
// in a pod whose trace dir is over its disk limits waits for collect to
// drain it (awaitDrain, drain.go) instead of exiting into a crash loop.
// Either wait returns the session's own result, so a preflight refusal
// still exits non-zero once drained.
func runSession(ctx context.Context, cfg runConfig, stderr io.Writer) error {
	stopProfile, err := startCPUProfile(cfg.cpuProfile)
	if err != nil {
		return err
	}
	err = errors.Join(runTrace(ctx, cfg, stderr), stopProfile())
	if cfg.linger {
		linger(ctx, err, stderr)
	} else {
		awaitDrain(ctx, cfg, stderr)
	}
	return err
}

// linger waits for ctx — SIGINT or SIGTERM — after the session ended
// (PLAN.md #18, task af4 review B1). A k8s debug container's process is
// its tracer run: were it to exit with the session's -duration, the
// container would stop, `kubectl exec` could no longer enter it, and the
// last segments would sit unreachable in the pod's emptyDir until the pod
// went. runTrace has returned by now, so finish has closed the tracer —
// every BPF program detached, every map and link freed — and published
// the final segment into ready/: lingering holds no probe and no kernel
// memory, only an idle process for `iormesh ready` to run beside. A
// session that failed lingers too (the error is printed first), since it
// may have published segments before failing. A signal that ended the
// session already ends the linger at once.
func linger(ctx context.Context, sessionErr error, stderr io.Writer) {
	if ctx.Err() != nil {
		return
	}
	status := "ended"
	if sessionErr != nil {
		status = fmt.Sprintf("failed: %v", sessionErr)
	}
	_, _ = fmt.Fprintf(stderr, "iormesh: session %s; BPF released, lingering for collect until SIGTERM (-linger)\n",
		status)
	<-ctx.Done()
}
