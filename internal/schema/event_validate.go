package schema

import "fmt"

// Validate checks the envelope's invariants — the ones every consumer
// (normalizer, golden check, merge, LLM) relies on without rechecking.
// It returns the first violation, wrapped in ErrInvalid.
func (e Event) Validate() error {
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
	return e.validateKindArgs()
}

// KnownKind reports whether kind is part of this schema draft.
func KnownKind(kind string) bool {
	switch kind {
	case KindOpen, KindOpenat, KindOpenat2, KindRead, KindWrite,
		KindClose, KindConnect, KindAccept, KindAccept4,
		KindTCPState, KindMarker, KindDrop, KindSession, KindEnd:
		return true
	}
	return false
}

// validateFD enforces the fd conventions: NoFD is the only negative fd,
// and an absent fd has no generation to count.
func (e Event) validateFD() error {
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
func (e Event) validateAttribution() error {
	switch e.Attribution {
	case AttributionJava:
		if e.Java == nil {
			return invalidf("attribution java without java payload")
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
// an open without its path, or a marker without its name, would make the
// correlator's and normalizer's joins silently miss.
func (e Event) validateKindArgs() error {
	switch e.Event {
	case KindOpen, KindOpenat, KindOpenat2:
		if e.Args.Path == nil {
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
	}
	return nil
}

// invalidf formats a validation failure wrapped in ErrInvalid.
func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}
