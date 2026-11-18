package target

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
)

// Spec says which processes a session traces. Exactly one of the three
// is set (Validate).
type Spec struct {
	// PIDs are traced from the start, with their children (M2 step 1);
	// the session ends once all of them are gone.
	PIDs []uint32
	// Pattern is a glob (Glob) matched on exec against the command line
	// and the executable path — never the comm, whose 15 characters
	// cannot tell two JVMs apart. Decision #23's defaults: every match is
	// traced, and the session waits for a restarted app to re-appear
	// rather than ending when the matches exit.
	Pattern string
	// ListenPort binds the session to whichever processes own a TCP
	// socket listening on this port, resolved once at start (what M6's
	// expansion uses; following the port across restarts is open).
	ListenPort uint16
}

// Mode is which of Spec's three ways of naming a target is in use.
type Mode int

// The modes of a valid Spec.
const (
	ModePIDs Mode = iota + 1
	ModePattern
	ModeListenPort
)

// ErrSpec marks an unusable target spec.
var ErrSpec = errors.New("target spec")

// Validate requires exactly one way of naming the target, and a pattern
// that compiles.
func (s Spec) Validate() error {
	set := 0
	for _, on := range []bool{len(s.PIDs) > 0, s.Pattern != "", s.ListenPort != 0} {
		if on {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("%w: give exactly one of PIDs, a pattern or a listening port (%d given)",
			ErrSpec, set)
	}
	if s.Pattern != "" {
		if _, err := CompileGlob(s.Pattern); err != nil {
			return fmt.Errorf("%w: %w", ErrSpec, err)
		}
	}
	return nil
}

// Mode returns how a valid spec names its target.
func (s Spec) Mode() Mode {
	switch {
	case s.Pattern != "":
		return ModePattern
	case s.ListenPort != 0:
		return ModeListenPort
	}
	return ModePIDs
}

// Matches reports whether an exec of exe with argv is a target of a
// pattern spec: the glob matches the command line (argv joined by single
// spaces, as ps shows it) or the executable path. Nothing else counts —
// a process known only by its comm never matches, however well the comm
// fits. A spec without a pattern, or one that does not compile, matches
// nothing: Validate reports why.
func (s Spec) Matches(exe string, argv []string) bool {
	g, err := CompileGlob(s.Pattern)
	if err != nil {
		return false
	}
	if len(argv) > 0 && g.Match(strings.Join(argv, " ")) {
		return true
	}
	return exe != "" && g.Match(exe)
}

// SplitArgv turns an argument block — NUL-separated strings as in
// /proc/<pid>/cmdline and the exec record — into argv. Trailing NULs
// (the terminator of the last string, a truncated block's padding) are
// not empty arguments.
func SplitArgv(block []byte) []string {
	block = bytes.TrimRight(block, "\x00")
	if len(block) == 0 {
		return nil
	}
	return strings.Split(string(block), "\x00")
}
