package k8s

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
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

// attachProbeTimeout bounds one probe (its exec and the get after a
// failed one), a variable so a test can shorten it. Each probe has its
// own bound rather than the wait's deadline (task 5g4): a probe in
// flight when the deadline passes still gets its answer, so the timeout
// message names what the tracer last said and not "signal: killed",
// and a short wait is not lost to a slow first exec. The wait may so
// end up to this much after its timeout.
var attachProbeTimeout = 30 * time.Second

// ErrNotAttached: the tracer did not report itself attached — its
// session ended or died first, its container exited, `iormesh ready
// -state` failed in it, the wait timed out or was interrupted.
var ErrNotAttached = errors.New("k8s: tracer not attached")

// The words of an answer after its state, checked because state.json
// is written in the pod (task 5g4): whatever the pod's tracer
// container holds, an answer either is one iormesh writes or fails the
// wait, and the session id deploy prints to the operator's terminal is
// 8 lowercase hex (cli newSessionID), the end reason a snake_case word
// (cli endReason).
var (
	attachSession = regexp.MustCompile(`^[0-9a-f]{8}$`)
	attachReason  = regexp.MustCompile(`^[a-z_]{1,32}$`)
)

// WaitAttached waits until the pod's debug tracer reports project's
// session attached and returns the session id. It fails at once when
// the session already ended or died, the tracer container exited or the
// probe itself failed in it, and with ErrNotAttached naming the last
// answer once timeout passed or ctx ended — the message tells the two
// apart, and an ended ctx's error is wrapped as well.
func (k *Kubectl) WaitAttached(ctx context.Context, proj string, timeout time.Duration) (string, error) {
	if !project.Valid(proj) {
		return "", fmt.Errorf("%w: project %q: %s", ErrName, proj, project.Rule)
	}
	deadline := time.Now().Add(timeout)
	last := "no answer yet"
	for {
		session, seen, err := k.probeOnce(ctx, proj)
		if err != nil || session != "" {
			return session, err
		}
		if ctx.Err() != nil {
			return "", k.interrupted(ctx, proj, last)
		}
		last = cmp.Or(seen, last)
		left := time.Until(deadline)
		if left <= 0 {
			return "", fmt.Errorf("%w: %s, project %s, not attached after %s (last: %s); "+
				"see kubectl logs %s --container=%s", ErrNotAttached, k.tracerName(), proj, timeout, last,
				k.t.Pod, TracerContainer)
		}
		timer := time.NewTimer(min(attachPoll, left))
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", k.interrupted(ctx, proj, last)
		case <-timer.C:
		}
	}
}

// interrupted is WaitAttached's error for a ctx that ended first (an
// operator's Ctrl-C, mostly): not a timeout, so it says so.
func (k *Kubectl) interrupted(ctx context.Context, proj, last string) error {
	return fmt.Errorf("%w: %s, project %s: wait interrupted before the tracer attached (last: %s): %w",
		ErrNotAttached, k.tracerName(), proj, last, context.Cause(ctx))
}

// probeOnce is attachProbe under its own attachProbeTimeout. A probe the
// bound cut off says so as its answer.
func (k *Kubectl) probeOnce(ctx context.Context, proj string) (session, last string, err error) {
	pctx, cancel := context.WithTimeout(ctx, attachProbeTimeout)
	defer cancel()
	session, last, err = k.attachProbe(pctx, proj)
	if last == "" && ctx.Err() == nil && pctx.Err() != nil {
		last = fmt.Sprintf("no answer within %s", attachProbeTimeout)
	}
	return session, last, err
}

// attachProbe asks the tracer once. It returns the session id once
// attached, else what it saw for the timeout's message ("" when ctx cut
// the probe off); an error only for an answer no wait can change: an
// ended or dead session, an exited container, or `iormesh ready -state`
// failing in the running container (a remote exit status: an old image,
// a corrupt state.json — task 5g4).
func (k *Kubectl) attachProbe(ctx context.Context, proj string) (session, last string, err error) {
	out, rerr := k.Run(ctx, TracerBinary, "ready", "-project", proj, ReadyStateFlag)
	if rerr == nil {
		return parseAttach(strings.TrimSpace(string(out)), k.tracerName())
	}
	if ctx.Err() != nil {
		return "", "", nil // cut off: the answer before it says more
	}
	if exitErr := (*exec.ExitError)(nil); errors.As(rerr, &exitErr) {
		// Only a command that ran wraps the ExitError (explain): the
		// container runs, and the probe's own failure will not change.
		return "", "", fmt.Errorf("%w: %s: `%s ready %s` failed in the tracer container: %s",
			ErrNotAttached, k.tracerName(), TracerBinary, ReadyStateFlag, firstLine(rerr.Error()))
	}
	// kubectl itself failed: the container is not running yet (pulling,
	// creating), or no longer. Only the pod's status tells which.
	p, gerr := k.getPod(ctx)
	if gerr != nil {
		if ctx.Err() != nil {
			// The bound cut the get off: the exec's failure says more.
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

// parseAttach reads one `iormesh ready -state` answer, refusing words
// iormesh never writes (attachSession, attachReason): pending and dead
// take none, running a session id, ended an end reason or none.
func parseAttach(answer, where string) (session, last string, err error) {
	word, rest, _ := strings.Cut(answer, " ")
	switch {
	case word == AttachRunning && attachSession.MatchString(rest):
		return rest, "", nil
	case word == AttachRunning:
		return "", "", fmt.Errorf("%w: %s answered %q: no session id", ErrNotAttached, where, firstLine(answer))
	case word == AttachPending && rest == "":
		return "", "probes not attached yet", nil
	case word == AttachEnded && (rest == "" || attachReason.MatchString(rest)):
		return "", "", fmt.Errorf("%w: %s: the session already ended (%s); restart the pod for a new one",
			ErrNotAttached, where, cmp.Or(rest, "no reason given"))
	case word == AttachDead && rest == "":
		return "", "", fmt.Errorf("%w: %s: the tracer died without its end; restart the pod for a new session",
			ErrNotAttached, where)
	}
	return "", "", fmt.Errorf("%w: %s answered %q, not an attach state", ErrNotAttached, where, firstLine(answer))
}

// firstLine is s up to its first newline, at most 200 bytes: enough of
// a kubectl error or an odd answer for a message. iormeshctl prints
// errors terminal-safe (ctl errText), whatever the pod wrote.
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
