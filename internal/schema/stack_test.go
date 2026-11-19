package schema

import (
	"bytes"
	"errors"
	"reflect"
	"testing"
)

func validStack() Stack {
	off := uint64(0x1234)
	return Stack{SchemaVersion: Version, StackID: 1, PID: 7, PIDInc: 99, Frames: []StackFrame{
		{Addr: "0x401234", Module: "/usr/bin/x", Offset: &off, BuildID: "ab", Symbol: "main.f+0x4"},
		{Addr: "0x7f0000001000"}, // JIT code: anonymous, a bare address
	}}
}

func TestStackRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := NewStackWriter(&buf)
	want := []Stack{validStack(), validStack()}
	want[1].StackID = 2
	for _, st := range want {
		if err := w.Stack(st); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStacks(&buf)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read back %+v, %v; want %+v", got, err, want)
	}
}

func TestStackValidate(t *testing.T) {
	cases := map[string]func(*Stack){
		"version":            func(s *Stack) { s.SchemaVersion = 2 },
		"id 0":               func(s *Stack) { s.StackID = 0 },
		"pid 0":              func(s *Stack) { s.PID = 0 },
		"no frames":          func(s *Stack) { s.Frames = nil },
		"decimal addr":       func(s *Stack) { s.Frames[1].Addr = "4096" },
		"offset sans module": func(s *Stack) { s.Frames[1].Offset = s.Frames[0].Offset },
	}
	for name, mutate := range cases {
		st := validStack()
		mutate(&st)
		if err := st.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate = %v, want ErrInvalid", name, err)
		}
		if err := NewStackWriter(&bytes.Buffer{}).Stack(st); err == nil {
			t.Errorf("%s: the writer wrote an invalid stack", name)
		}
	}
}

func TestDecodeStackStrict(t *testing.T) {
	for name, line := range map[string]string{
		"unknown key":  `{"schema_version":1,"stack_id":1,"pid":7,"pid_incarnation":1,"frames":[{"addr":"0x1"}],"x":1}`,
		"frame key":    `{"schema_version":1,"stack_id":1,"pid":7,"pid_incarnation":1,"frames":[{"addr":"0x1","Symbol":"a"}]}`,
		"missing pid":  `{"schema_version":1,"stack_id":1,"pid_incarnation":1,"frames":[{"addr":"0x1"}]}`,
		"missing addr": `{"schema_version":1,"stack_id":1,"pid":7,"pid_incarnation":1,"frames":[{"module":"m"}]}`,
	} {
		if _, err := DecodeStack([]byte(line)); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: DecodeStack = %v, want ErrInvalid", name, err)
		}
	}
}

// TestStackIDValidated: an event's stack_id, when set, names a table id.
func TestStackIDValidated(t *testing.T) {
	ev := Event{SchemaVersion: Version, Project: "p", HostID: "h", Seq: 1, Event: KindRead,
		FD: 3, FDGen: 1, Attribution: AttributionNone}
	for id, ok := range map[uint32]bool{0: false, 1: true} {
		ev.StackID = &id
		if err := ev.Validate(); (err == nil) != ok {
			t.Errorf("stack_id %d: Validate = %v", id, err)
		}
	}
}
