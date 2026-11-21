package k8s

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"iormesh/internal/project"
)

// Waiting for a debug tracer to attach (task ef4). `kubectl debug`
// returns once the API server took the ephemeral container, before the
// image is pulled, the tracer started and its probes loaded — seconds on
// kind, longer on a cold node. A workload started in that gap goes
// untraced, so deploy waits for the tracer's own word: the state.json
// its run writes once every probe is attached (cli markRunning), read
// in the pod by `iormesh ready -project P -state` over kubectl exec.

// The answers of `iormesh ready -state`, one line on stdout: the first
// word is the state, a running or ended session adds its session id or
// end reason after a space.
const (
	// AttachPending: no state.json yet — the run has not attached.
	AttachPending = "pending"
	// AttachRunning: the probes are attached and the session traces.
	AttachRunning = "running"
	// AttachEnded: the session attached and has ended already.
	AttachEnded = "ended"
	// AttachDead: state.json says running, but its process is gone.
	AttachDead = "dead"
)

// ReadyStateFlag makes `iormesh ready` print the project's attach state
// instead of streaming its segments.
const ReadyStateFlag = "-state"

// DefaultAttachTimeout bounds deploy's wait for one tracer: the image
// pull on a node that lacks it dominates, the probes load in seconds.
const DefaultAttachTimeout = 2 * time.Minute

// attachPoll is the pause between two probes, a variable so a test can
// shorten it. Each probe is one kubectl exec (and a get after a failed
// one): a second apart keeps the API server's load trivial and adds at
// most a second to a deploy.
var attachPoll = time.Second

// ErrNotAttached: the tracer did not report itself attached — its
// session ended or died first, its container exited, or the wait timed
// out.
var ErrNotAttached = errors.New("k8s: tracer not attached")

// WaitAttached waits until the pod's debug tracer reports project's
// session attached and returns the session id. It fails at once when
// the session already ended or died or the tracer container exited, and
// with ErrNotAttached naming the last answer once timeout passed.
func (k *Kubectl) WaitAttached(ctx context.Context, proj string, timeout time.Duration) (string, error) {
	if !project.Valid(proj) {
		return "", fmt.Errorf("%w: project %q: %s", ErrName, proj, project.Rule)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	last := "no answer yet"
	for {
		session, seen, err := k.attachProbe(ctx, proj)
		if err != nil || session != "" {
			return session, err
		}
		last = cmp.Or(seen, last)
		timer := time.NewTimer(attachPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", fmt.Errorf("%w: %s, project %s, not attached after %s (last: %s); "+
				"see kubectl logs %s --container=%s", ErrNotAttached, k.tracerName(), proj, timeout, last,
				k.t.Pod, TracerContainer)
		case <-timer.C:
		}
	}
}

// attachProbe asks the tracer once. It returns the session id once
// attached, else what it saw for the timeout's message ("" when the
// timeout cut the probe off); an error only
// for an answer no wait can change.
func (k *Kubectl) attachProbe(ctx context.Context, proj string) (session, last string, err error) {
	out, rerr := k.Run(ctx, TracerBinary, "ready", "-project", proj, ReadyStateFlag)
	if rerr == nil {
		return parseAttach(strings.TrimSpace(string(out)), k.tracerName())
	}
	if ctx.Err() != nil {
		return "", "", nil // cut off by the timeout: the answer before it says more
	}
	// The exec failed: the container is not running yet (pulling,
	// creating), or no longer. Only the pod's status tells which.
	p, gerr := k.getPod(ctx)
	if gerr != nil {
		if ctx.Err() != nil {
			// The timeout cut the get off: the exec's failure says more.
			return "", firstLine(rerr.Error()), nil
		}
		return "", firstLine(gerr.Error()), nil
	}
	if st, ok := tracerStatus(p.Status.EphemeralContainerStatuses); ok && st.State.Terminated != nil {
		return "", "", fmt.Errorf("%w: %s %s before attaching; see kubectl logs %s --container=%s",
			ErrNotAttached, k.tracerName(), st.notRunning(), k.t.Pod, TracerContainer)
	}
	return "", firstLine(rerr.Error()), nil
}

// parseAttach reads one `iormesh ready -state` answer.
func parseAttach(answer, where string) (session, last string, err error) {
	word, rest, _ := strings.Cut(answer, " ")
	switch word {
	case AttachRunning:
		if rest == "" {
			return "", "", fmt.Errorf("%w: %s answered %q: no session id", ErrNotAttached, where, answer)
		}
		return rest, "", nil
	case AttachPending:
		return "", "probes not attached yet", nil
	case AttachEnded:
		return "", "", fmt.Errorf("%w: %s: the session already ended (%s); restart the pod for a new one",
			ErrNotAttached, where, rest)
	case AttachDead:
		return "", "", fmt.Errorf("%w: %s: the tracer died without its end; restart the pod for a new session",
			ErrNotAttached, where)
	}
	return "", "", fmt.Errorf("%w: %s answered %q, not an attach state", ErrNotAttached, where, firstLine(answer))
}

// firstLine is s up to its first newline, at most 200 bytes: enough of
// a kubectl error or an odd answer for a message.
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
