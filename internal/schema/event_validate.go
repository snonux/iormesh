package schema

import "fmt"

// Validate checks the envelope's invariants — the ones every consumer
// (normalizer, golden check, merge, LLM) relies on without rechecking.
// It returns the first violation, wrapped in ErrInvalid. The receiver is
// a pointer, as for every Event method: the sink validates each event,
// and copying the envelope into each check cost more than the checks
// (decision #25's per-event budget, task wc4).
func (e *Event) Validate() error {
	if e.SchemaVersion != Version {
		return invalidf("schema_version %d, want %d", e.SchemaVersion, Version)
	}
	if e.Project == "" {
		return invalidf("empty project")
	}
	if e.HostID == "" {
		return invalidf("empty host_id")
	}
	if e.Seq == 0 {
		return invalidf("seq 0: sequence numbers start at 1")
	}
	if !KnownKind(e.Event) {
		return invalidf("unknown event kind %q", e.Event)
	}
	if err := e.validateFD(); err != nil {
		return err
	}
	if e.Err != "" && e.Ret >= 0 {
		return invalidf("err %q with non-negative ret %d", e.Err, e.Ret)
	}
	if err := e.validateAttribution(); err != nil {
		return err
	}
	if (e.Event == KindSession) != (e.Session != nil) {
		return invalidf("session object on a %s event (set exactly on session events)", e.Event)
	}
	// The host-level args belong to their kind alone: a lost count on a
	// read, say, would make "sum args.lost" over a trace lie.
	if (e.Args.Lost != nil || e.Args.DropReason != nil) && e.Event != KindDrop {
		return invalidf("args.lost or args.drop_reason on a %s event (drop only)", e.Event)
	}
	if e.Args.EndReason != nil && e.Event != KindEnd {
		return invalidf("args.end_reason on a %s event (end only)", e.Event)
	}
	// argv is recorded through the redaction policy on exec events only;
	// one anywhere else came from a producer that may have skipped it.
	if e.Args.Argv != nil && e.Event != KindExec {
		return invalidf("args.argv on a %s event (exec only)", e.Event)
	}
	if e.StackID != nil && *e.StackID == 0 {
		return invalidf("stack_id 0: stack table ids start at 1")
	}
	if err := e.validateIPCArgs(); err != nil {
		return err
	}
	return e.validateKindArgs()
}

// KnownKind reports whether kind is part of schema v1: an M1
// kind, exec (M2 step 8), one of the M2 file surface (kinds_file.go) or of
// M2 step 7's socket addresses and opt-in families (kinds_ipc.go).
func KnownKind(kind string) bool {
	switch kind {
	case KindOpen, KindOpenat, KindOpenat2, KindRead, KindWrite,
		KindClose, KindConnect, KindAccept, KindAccept4,
		KindTCPState, KindMarker, KindDrop, KindSession, KindEnd, KindExec:
		return true
	}
	return fileKind(kind) || ipcKind(kind)
}

// validateFD enforces the fd conventions: NoFD is the only negative fd,
// and an absent fd has no generation to count.
func (e *Event) validateFD() error {
	if e.FD < NoFD {
		return invalidf("fd %d below %d", e.FD, NoFD)
	}
	if e.FD == NoFD && e.FDGen != 0 {
		return invalidf("fd_gen %d without an fd", e.FDGen)
	}
	return nil
}

// validateAttribution keeps the attribution flag and the Java payload in
// agreement, so "java" never points at an empty stack and "none" never
// hides one.
func (e *Event) validateAttribution() error {
	switch e.Attribution {
	case AttributionJava:
		if e.Java == nil || len(e.Java.Stack) == 0 {
			return invalidf("attribution java without a java stack")
		}
	case AttributionNone:
		if e.Java != nil {
			return invalidf("java payload with attribution none")
		}
	default:
		return invalidf("attribution %q, want %q or %q",
			e.Attribution, AttributionJava, AttributionNone)
	}
	return nil
}

// validateKindArgs requires the arguments a kind cannot be read without:
// an open (creat is one) without its path, or a marker without its name,
// would make the correlator's and normalizer's joins silently miss.
func (e *Event) validateKindArgs() error {
	switch e.Event {
	case KindOpen, KindOpenat, KindOpenat2, KindCreat:
		if e.Args.Path == nil || *e.Args.Path == "" {
			return invalidf("%s without args.path", e.Event)
		}
	case KindMarker:
		if e.Args.Marker == nil || *e.Args.Marker == "" {
			return invalidf("marker without args.marker")
		}
	case KindTCPState:
		if e.Args.SockState == nil || *e.Args.SockState == "" {
			return invalidf("tcp_state without args.sock_state")
		}
	case KindSignal:
		if e.Args.Signal == nil || e.PID == 0 {
			return invalidf("signal without args.signal or a receiving pid")
		}
	default:
		return e.validateSyntheticArgs()
	}
	return nil
}

// validateSyntheticArgs checks the host-level kinds: a drop without its
// count, an end without a known reason or a header without the boot id
// would turn "the trace says how complete it is" into a guess.
func (e *Event) validateSyntheticArgs() error {
	switch e.Event {
	case KindDrop:
		if e.Args.Lost == nil || *e.Args.Lost == 0 {
			return invalidf("drop without a positive args.lost")
		}
		if r := e.Args.DropReason; r != nil && *r != DropAttachGap && *r != DropUnattributed {
			return invalidf("drop reason %q, want %q, %q or none", *r, DropAttachGap, DropUnattributed)
		}
		if e.Args.DropReason != nil && e.PID == 0 {
			// Both reasons are one process's loss; without its pid
			// nobody could tell whose records are missing.
			return invalidf("%s drop without a pid", *e.Args.DropReason)
		}
	case KindEnd:
		if e.Args.EndReason == nil || !KnownEndReason(*e.Args.EndReason) {
			return invalidf("end without a known args.end_reason")
		}
	case KindSession:
		s := e.Session
		if s.BootID == "" || s.FQDN == "" || s.Kernel == "" || s.Arch == "" {
			return invalidf("session with an empty boot_id, fqdn, kernel or arch")
		}
	}
	return nil
}

// KnownEndReason reports whether reason is one of the End* constants.
func KnownEndReason(reason string) bool {
	switch reason {
	case EndDuration, EndStop, EndTargetsExited, EndDiskLimit, EndError:
		return true
	}
	return false
}

// invalidf formats a validation failure wrapped in ErrInvalid.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

// validateIPCArgs keeps M2 step 7's args to their kinds: a second fd's
// generation names nothing without the fd, a second fd belongs to the kinds
// that have one (fd2Kind), and signal numbers or pids on
// other kinds would make "count the SIGTERMs" or "who signalled whom" lie.
func (e *Event) validateIPCArgs() error {
	a := e.Args
	if a.FD2Gen != nil && a.FD2 == nil {
		return invalidf("args.fd2_gen without args.fd2 on a %s event", e.Event)
	}
	if a.FD2 != nil && !fd2Kind(e.Event) {
		return invalidf("args.fd2 on a %s event", e.Event)
	}
	if (a.Signal != nil || a.TargetPID != nil) && !signalKind(e.Event) {
		return invalidf("args.signal or args.target_pid on a %s event", e.Event)
	}
	if a.SenderPID != nil && e.Event != KindSignal {
		return invalidf("args.sender_pid on a %s event (signal only)", e.Event)
	}
	return e.validateCount()
}

// validateCount keeps args.count (M2 step 11) to what it means: the calls
// of a folded read or write loop, at least two — a single call carries no
// count, so "count 1" and "absent" never both describe one call.
func (e *Event) validateCount() error {
	c := e.Args.Count
	if c == nil {
		return nil
	}
	if e.Event != KindRead && e.Event != KindWrite {
		return invalidf("args.count on a %s event (read and write only)", e.Event)
	}
	if *c < 2 {
		return invalidf("args.count %d, want ≥ 2 or none", *c)
	}
	return nil
}
