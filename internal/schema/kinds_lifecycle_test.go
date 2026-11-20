package schema

import (
	"strings"
	"testing"
)

// validFork is a fork event of a child with its parent named (task ze4).
func validFork() Event {
	e := validEvent()
	e.Event, e.FD, e.FDGen, e.Err, e.Ret, e.ConnID = KindFork, NoFD, 0, "", 0, ""
	e.Attribution, e.Java = AttributionNone, nil
	e.Args = Args{ParentPID: ptr(uint32(4700)), ParentInc: ptr(uint64(91600000000))}
	return e
}

// TestLifecycleKindsValid: fork with and without a parent, attach with
// and without a pattern, and fcntl F_SETFD with its flags validate and
// round-trip through NDJSON, the new keys appended after tcp_seq.
func TestLifecycleKindsValid(t *testing.T) {
	orphan := validFork()
	orphan.Args = Args{}
	attach := validFork()
	attach.Event, attach.Args = KindAttach, Args{Pattern: ptr("*Orders*")}
	plain := attach
	plain.Args = Args{}
	setfd := validFork()
	setfd.Event, setfd.FD, setfd.Args = KindFcntl, 4, Args{Cmd: ptr(uint32(fcntlSetFD)), Flags: ptr(uint32(1))}
	for name, tc := range map[string]struct {
		ev   Event
		want string
	}{
		"fork":            {validFork(), `"args":{"parent_pid":4700,"parent_incarnation":91600000000}`},
		"orphan fork":     {orphan, `"args":{}`},
		"pattern attach":  {attach, `"args":{"pattern":"*Orders*"}`},
		"pid attach":      {plain, `"args":{}`},
		"F_SETFD's flags": {setfd, `"args":{"flags":1,"cmd":2}`},
	} {
		if err := tc.ev.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		line := AppendEvent(nil, &tc.ev)
		if !strings.Contains(string(line), tc.want) {
			t.Errorf("%s: %s lacks %s", name, line, tc.want)
		}
		got, err := DecodeEvent(line)
		if err != nil || string(AppendEvent(nil, &got)) != string(line) {
			t.Errorf("%s: round trip %v:\n%s\n%s", name, err, AppendEvent(nil, &got), line)
		}
	}
}

// TestLifecycleArgsRejected: each new arg only where it belongs.
func TestLifecycleArgsRejected(t *testing.T) {
	for name, breakIt := range map[string]func(*Event){
		"pattern on a fork":       func(e *Event) { e.Args.Pattern = ptr("*x*") },
		"empty pattern":           func(e *Event) { e.Event, e.Args = KindAttach, Args{Pattern: ptr("")} },
		"parent on an attach":     func(e *Event) { e.Event = KindAttach },
		"parent on an exec":       func(e *Event) { e.Event = KindExec },
		"parent pid alone":        func(e *Event) { e.Args.ParentInc = nil },
		"parent incarnation only": func(e *Event) { e.Args.ParentPID = nil },
		"fork of pid 0":           func(e *Event) { e.PID, e.PIDInc, e.TID, e.Comm = 0, 0, 0, "" },
		"attach of pid 0": func(e *Event) {
			e.Event, e.Args, e.PID, e.PIDInc, e.TID = KindAttach, Args{}, 0, 0, 0
		},
		"flags on F_GETFD": func(e *Event) {
			e.Event, e.FD, e.Args = KindFcntl, 4, Args{Cmd: ptr(uint32(1)), Flags: ptr(uint32(1))}
		},
		"flags on a fcntl without cmd": func(e *Event) {
			e.Event, e.FD, e.Args = KindFcntl, 4, Args{Flags: ptr(uint32(1))}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := validFork()
			breakIt(&e)
			checkRejected(t, e)
		})
	}
}
