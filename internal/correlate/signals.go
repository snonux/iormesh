package correlate

import (
	"iormesh/internal/schema"
	"iormesh/internal/types"
)

// Signals (M2 step 7, the signal family; PLAN.md "What we trace": delivery
// with sender PID). One signal_generate record (signal.c) names both ends:
// the sender's pending kill, tgkill or tkill gets the target and the signal
// number, and a traced receiver gets a signal event naming its sender —
// also when the sender is not traced (a supervisor's SIGTERM). Both traced:
// a signaled edge, sender → receiver. Only a task-sent signal has a sender
// (signal.c reads the siginfo): a kernel-raised SIGALRM, SIGCHLD or SIGIO
// fires in whatever task runs, which the record's scope leaves without
// SIGNAL_SCOPE_USER, so it gets no sender_pid and no edge.

// Results of signal_generate (include/trace/events/signal.h) that mean the
// signal is queued for the receiver. An ignored one (a default-ignored
// SIGCHLD, say) or one the kernel could not queue is no delivery and is
// not recorded as one.
const (
	signalDelivered      = 0
	signalAlreadyPending = 2
)

// sigInfo is what a sender's call learned from its signal_generate record.
type sigInfo struct {
	target uint32
	sig    int32
}

// fillKill sets a kill's target and signal; a call that generated nothing
// (signal 0, no such process, no permission) has none to report.
func (s *sigInfo) fillKill(args *schema.Args) {
	if s == nil {
		return
	}
	target, sig := s.target, s.sig
	args.TargetPID, args.Signal = &target, &sig
}

// signalRecord applies one signal_generate record to both ends.
func (c *Correlator) signalRecord(r *types.SignalEvent) {
	c.advance(r.Time)
	if r.Scope&types.SIGNAL_SCOPE_SENDER != 0 {
		c.signalSender(r)
	}
	if r.Scope&types.SIGNAL_SCOPE_TARGET == 0 ||
		(r.Result != signalDelivered && r.Result != signalAlreadyPending) {
		return
	}
	c.signalReceived(r)
	if r.Scope&types.SIGNAL_SCOPE_SENDER != 0 && r.Pid != r.TargetPid {
		sender, target := c.proc(r.Pid), c.proc(r.TargetPid)
		c.graph.signaled(r.Pid, sender.inc, r.TargetPid, target.inc)
	}
}

// signalSender attaches target and signal to the sender's pending call.
// SIGNAL_SCOPE_SENDER is set only for a task-sent signal, so a kernel
// signal raised while the call ran never lands on it.
// The record fires inside that call, so the call is still pending; kill of
// a process group generates one record per member, and the first names the
// call's target.
func (c *Correlator) signalSender(r *types.SignalEvent) {
	en, ok := c.enters[r.Tid]
	if !ok || en.sig != nil {
		return
	}
	switch en.name {
	case schema.KindKill, schema.KindTgkill, schema.KindTkill:
		en.sig = &sigInfo{target: r.TargetPid, sig: r.Sig}
		c.enters[r.Tid] = en
	}
}

// signalReceived records the delivery on the receiving process and thread.
func (c *Correlator) signalReceived(r *types.SignalEvent) {
	p := c.proc(r.TargetPid)
	sig := r.Sig
	ev := schema.Event{SchemaVersion: schema.Version, Project: c.cfg.Host.Project,
		HostID: c.cfg.Host.ID, BootNS: r.Time, WallNS: c.wall(r.Time), PID: r.TargetPid,
		PIDInc: p.inc, TID: r.TargetTid, Comm: p.comm, Event: schema.KindSignal, FD: schema.NoFD,
		Attribution: schema.AttributionNone}
	ev.Args.Signal = &sig
	if r.Scope&types.SIGNAL_SCOPE_USER != 0 {
		sender := r.Pid
		ev.Args.SenderPID = &sender
	}
	c.enqueue(ev, nil)
}
