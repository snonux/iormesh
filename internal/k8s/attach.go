package k8s

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
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
// end reason after a space, and running, ended and dead end with the
// session's AttachStartedKey word.
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

// AttachStartedKey prefixes the last word of a running, ended or dead
// answer: "started=" and the session's StartedAt, RFC3339 (as iormesh
// writes it, in UTC with nanoseconds) or unix nanoseconds (task eh4).
// An answer without it (an older tracer image) is still read; the wait
// then falls back to the state-change check alone.
const AttachStartedKey = "started="

// attachSkew is how much earlier than the tracer container's start a
// session's StartedAt may lie and still be taken as that container's
// run (task eh4). Both instants come from the same node — the kubelet
// (or its container runtime) stamps the container's running.startedAt,
// the tracer's markRunning its StartedAt, after its probes attached and
// so after the container started — so no clock skew between hosts is in
// play. The window covers the rest: the status's whole seconds, the
// runtime's stamp versus the process's own clock read, and a clock step
// on the node. A predecessor's state lies further back than that: it
// attached before its app container died, and the new tracer was only
// started after deploy saw the old one ended.
const attachSkew = 2 * time.Second

// The bounds of a started= time: state.json is written in the pod, so
// the word is parsed strictly — at most the length of an RFC3339 time
// with nanoseconds and a zone offset, all digits (unix ns) or RFC3339,
// and within these years.
var (
	attachStartedMin = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	attachStartedMax = time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)
	attachUnixNS     = regexp.MustCompile(`^[0-9]{1,19}$`)
)

const attachStartedMaxLen = len("2006-01-02T15:04:05.999999999-07:00")

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
//
// After a Deploy that left or started a tracer with an ended
// predecessor (superseded, task ah4), an answer may be the
// predecessor's: its state.json stays in the shared trace dir until the
// new run's markRunning replaces it, and the new tracer reads it as dead
// (its process is gone with the old PID namespace; running, should its
// pid be reused). The answer's started= time tells whose it is (task
// eh4): one more than attachSkew older than the new tracer container's
// running.startedAt (one pod read, kept in predecessor) is the
// predecessor's and is waited past; any other is the new run's own and
// is acted on — running is attached, ended or dead fails at once. So a
// new run that ended before deploy saw it attached, or that lingers
// under -linger after attaching, fails at once rather than timing out.
//
// Without a time to compare — an answer without started= (an older
// image), a pod read that failed or has no start for the container —
// the ah4 rule is the fallback: dead and ended are waited past, and
// the answer changing from the first one seen (dead to ended, say, or
// another started= time) fails at once, as only the new run's own
// state.json can do that. Either way a run that fails before it
// attaches writes no state.json: under -linger it lingers, its
// container keeps running, the predecessor's state stays, and the wait
// times out saying so and naming the container's logs. A run that
// exits (a crash) takes its container down, which the probe sees and
// fails on.
func (k *Kubectl) WaitAttached(ctx context.Context, proj string, timeout time.Duration) (string, error) {
	if !project.Valid(proj) {
		return "", fmt.Errorf("%w: project %q: %s", ErrName, proj, project.Rule)
	}
	// Made once and kept across waits on this transport: the
	// predecessor's state does not change, so a later wait still knows
	// the first answer.
	if k.superseded && k.pred == nil {
		k.pred = &predecessor{logs: fmt.Sprintf("kubectl logs %s --container=%s", k.t.Pod, k.tracer())}
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
				k.t.Pod, k.tracer())
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
		answer := strings.TrimSpace(string(out))
		if k.pred != nil && k.pred.start.IsZero() && strings.Contains(answer, " "+AttachStartedKey) {
			// Read once per transport: an ephemeral container is never
			// restarted, so its start does not change.
			k.pred.start = k.containerStart(ctx)
		}
		return parseAttach(answer, k.tracerName(), k.pred)
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
	if st, ok := statusOf(p.Status.EphemeralContainerStatuses, k.tracer()); ok && st.State.Terminated != nil {
		return "", "", fmt.Errorf("%w: %s %s before attaching; see kubectl logs %s --container=%s",
			ErrNotAttached, k.tracerName(), st.notRunning(), k.t.Pod, k.tracer())
	}
	return "", firstLine(rerr.Error()), nil
}

// containerStart is the tracer container's running.startedAt from one
// pod read, zero when the read fails or the status has none (not
// running, an odd time): the wait then falls back to the state-change
// check (predecessor.seen).
func (k *Kubectl) containerStart(ctx context.Context) time.Time {
	p, err := k.getPod(ctx)
	if err != nil {
		return time.Time{}
	}
	st, ok := statusOf(p.Status.EphemeralContainerStatuses, k.tracer())
	if !ok || st.State.Running == nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, st.State.Running.StartedAt)
	if err != nil {
		return time.Time{}
	}
	return t
}

// predecessor is WaitAttached's memory of a superseded tracer's state
// (task ah4): the first dead or ended answer, taken as the
// predecessor's, the logs command its messages name, and the new tracer
// container's start once a pod read gave it (task eh4; zero before).
type predecessor struct {
	first, logs string
	start       time.Time
}

// owner says whose state.json an answer read: the predecessor's, the
// new run's own, or unknown without a time to compare.
type owner int

const (
	ownerUnknown owner = iota
	ownerPredecessor
	ownerNew
)

// whose compares a session's StartedAt with the container's start: more
// than attachSkew before it is the predecessor's session, anything later
// the new run's. Either zero leaves it unknown.
func (p *predecessor) whose(started time.Time) owner {
	switch {
	case started.IsZero() || p.start.IsZero():
		return ownerUnknown
	case started.Before(p.start.Add(-attachSkew)):
		return ownerPredecessor
	}
	return ownerNew
}

// older is the last answer for a state whose StartedAt shows it the
// predecessor's: what the wait is still waiting for. The answer is
// remembered as the first one seen, as seen does, so a later answer
// without a time to compare still meets the state-change check.
func (p *predecessor) older(answer, state string, started time.Time) string {
	if p.first == "" {
		p.first = answer
	}
	return fmt.Sprintf("still the ended tracer's state (%s, started %s, before this container started at %s): "+
		"the new run has not attached, or failed before attaching; check %s", state,
		started.Format(time.RFC3339), p.start.Format(time.RFC3339), p.logs)
}

// seen takes one dead or ended answer: the predecessor's while it is
// the first one's (pending, with a last that says what it may mean),
// the new run's own once it differs — its session attached and is
// over, which no wait changes.
func (p *predecessor) seen(answer, where string) (last string, err error) {
	if p.first == "" {
		p.first = answer
	}
	if answer != p.first {
		return "", fmt.Errorf("%w: %s: the state changed from %q to %q: the new session attached and is "+
			"already over; see %s", ErrNotAttached, where, p.first, firstLine(answer), p.logs)
	}
	return fmt.Sprintf("still the ended tracer's state (%s): the new run has not attached, or failed before "+
		"attaching; check %s", answer, p.logs), nil
}

// parseAttach reads one `iormesh ready -state` answer, refusing words
// iormesh never writes (attachSession, attachReason, splitStarted):
// pending takes none, dead only a started= time, running a session id,
// ended an end reason or none, each then an optional started= time.
// With pred (the tracer has an ended predecessor, WaitAttached) the time
// decides whose state it is (pred.whose): the predecessor's is waited
// past, the new run's own acted on. Without a time to compare, a dead or
// ended state goes to pred.seen, as the predecessor's while unchanged.
func parseAttach(answer, where string, pred *predecessor) (session, last string, err error) {
	body, started, ok := splitStarted(answer)
	if !ok {
		return "", "", fmt.Errorf("%w: %s answered %q: its %s word is not an RFC3339 or unix-nanosecond time "+
			"in [%d, %d)", ErrNotAttached, where, firstLine(answer), AttachStartedKey,
			attachStartedMin.Year(), attachStartedMax.Year())
	}
	word, rest, _ := strings.Cut(body, " ")
	dead := word == AttachDead && rest == ""
	over := word == AttachEnded && (rest == "" || attachReason.MatchString(rest))
	running := word == AttachRunning && attachSession.MatchString(rest)
	if pred != nil && (dead || over || running) {
		switch pred.whose(started) {
		case ownerPredecessor:
			return "", pred.older(answer, body, started), nil
		case ownerUnknown:
			if !running {
				last, err := pred.seen(answer, where)
				return "", last, err
			}
		}
	}
	switch {
	case running:
		return rest, "", nil
	case word == AttachRunning:
		return "", "", fmt.Errorf("%w: %s answered %q: no session id", ErrNotAttached, where, firstLine(answer))
	case word == AttachPending && rest == "" && started.IsZero():
		return "", "probes not attached yet", nil
	case over:
		return "", "", fmt.Errorf("%w: %s: the session already ended (%s); restart the pod for a new one%s",
			ErrNotAttached, where, cmp.Or(rest, "no reason given"), seeLogs(pred))
	case dead:
		return "", "", fmt.Errorf("%w: %s: the tracer died without its end; restart the pod for a new session%s",
			ErrNotAttached, where, seeLogs(pred))
	}
	return "", "", fmt.Errorf("%w: %s answered %q, not an attach state", ErrNotAttached, where, firstLine(answer))
}

// seeLogs points a superseded tracer's failure at its own container's
// logs (pred's), "" without a predecessor.
func seeLogs(pred *predecessor) string {
	if pred == nil {
		return ""
	}
	return "; see " + pred.logs
}

// splitStarted cuts answer's last word off when it is a started= one,
// returning the rest and the time; an answer without one comes back
// whole with a zero time. ok is false for a started= word whose time
// parseStarted refuses.
func splitStarted(answer string) (body string, started time.Time, ok bool) {
	i := strings.LastIndexByte(answer, ' ')
	v, has := strings.CutPrefix(answer[i+1:], AttachStartedKey)
	if i < 0 || !has {
		return answer, time.Time{}, true
	}
	started, ok = parseStarted(v)
	return answer[:i], started, ok
}

// parseStarted reads a started= time, strictly and bounded (state.json
// is written in the pod): RFC3339 with or without fractional seconds,
// or unix nanoseconds as plain digits, no longer than
// attachStartedMaxLen, in [attachStartedMin, attachStartedMax).
func parseStarted(v string) (time.Time, bool) {
	if len(v) > attachStartedMaxLen {
		return time.Time{}, false
	}
	var t time.Time
	if attachUnixNS.MatchString(v) {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		t = time.Unix(0, n).UTC()
	} else {
		var err error
		if t, err = time.Parse(time.RFC3339Nano, v); err != nil {
			return time.Time{}, false
		}
	}
	if t.Before(attachStartedMin) || !t.Before(attachStartedMax) {
		return time.Time{}, false
	}
	return t, true
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
