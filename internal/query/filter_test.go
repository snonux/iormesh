package query

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// otelTrace is a W3C trace id for the -otel-trace tests.
const otelTrace = "4bf92f3577b34da6a3ce929d0e0e4736"

// kindEvents are bare events for the -event, -pid, -errno and
// -otel-trace predicates (task wh4): seqs 1–5.
func kindEvents() []schema.Event {
	evs := []schema.Event{
		fdEvent(1, schema.KindOpenat, 3, 3, "/a"),
		fdEvent(2, schema.KindOpenat, -1, -2, "/b"),
		fdEvent(3, schema.KindConnect, 4, -115, ""),
		fdEvent(4, schema.KindRead, 3, 10, ""),
		fdEvent(5, schema.KindWrite, 3, 10, ""),
	}
	evs[1].Err, evs[2].Err = "ENOENT", "EINPROGRESS"
	evs[2].PID, evs[4].PID = 2, 2
	evs[3].Otel = &schema.Otel{TraceID: otelTrace, SpanID: "00f067aa0ba902b7"}
	evs[4].Otel = &schema.Otel{TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331"}
	return evs
}

// TestKindPidErrnoOtel: each new predicate alone, and ANDed with the
// others and with an existing one.
func TestKindPidErrnoOtel(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Filter
		want []uint64
	}{
		{"event", Filter{Event: schema.KindOpenat}, []uint64{1, 2}},
		{"event none", Filter{Event: schema.KindClose}, nil},
		{"pid", Filter{PID: 2, HasPID: true}, []uint64{3, 5}},
		{"pid zero", Filter{HasPID: true}, nil},
		{"errno", Filter{Errno: "ENOENT"}, []uint64{2}},
		{"otel", Filter{OtelTrace: otelTrace}, []uint64{4}},
		{"event and errno", Filter{Event: schema.KindOpenat, Errno: "ENOENT"}, []uint64{2}},
		{"event and pid", Filter{Event: schema.KindOpenat, PID: 2, HasPID: true}, nil},
		{"pid and errno", Filter{PID: 2, HasPID: true, Errno: "EINPROGRESS"}, []uint64{3}},
		{"pid and otel", Filter{PID: 1, HasPID: true, OtelTrace: otelTrace}, []uint64{4}},
		{"event and thread", Filter{Event: schema.KindWrite, Thread: "1"}, []uint64{5}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.f.Validate(); err != nil {
				t.Fatal(err)
			}
			if got := matchedSeqs(t, tc.f, kindEvents()); !slices.Equal(got, tc.want) {
				t.Errorf("matched seqs %v, want %v", got, tc.want)
			}
		})
	}
}

// matchedSeqs runs events through a matcher of f and returns the seqs
// it matched.
func matchedSeqs(t *testing.T, f Filter, events []schema.Event) []uint64 {
	t.Helper()
	m := newMatcher(f, Input{})
	var got []uint64
	for i := range events {
		ok, err := m.match(&Source{}, &events[i])
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			got = append(got, events[i].Seq)
		}
	}
	return got
}

// TestInvalidKindErrnoOtel: an unknown kind (the error lists the
// schema's kinds), an errno name the tracer never writes, a malformed
// trace id.
func TestInvalidKindErrnoOtel(t *testing.T) {
	for _, tc := range []struct {
		f   Filter
		msg string
	}{
		{Filter{Event: "opennat"}, "openat"},
		{Filter{Event: "truncated"}, "unknown event kind"},
		{Filter{Errno: "ENOPE"}, "ENOPE"},
		{Filter{Errno: "enoent"}, "enoent"},
		// ParseErrno maps an alias; Validate wants the recorded name.
		{Filter{Errno: "EWOULDBLOCK"}, "EWOULDBLOCK"},
		{Filter{OtelTrace: "4BF92F3577B34DA6A3CE929D0E0E4736"}, "32 lowercase hex"},
		{Filter{OtelTrace: strings.Repeat("0", 32)}, "32 lowercase hex"},
		{Filter{OtelTrace: "abc"}, "32 lowercase hex"},
	} {
		err := tc.f.Validate()
		if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%+v: err %v, want ErrInvalid naming %q", tc.f, err, tc.msg)
		}
	}
}

// TestParseErrno: a name in any case, an alias the tracer records under
// another name, or a number, to the name events carry; the rest refused.
func TestParseErrno(t *testing.T) {
	for in, want := range map[string]string{"ENOENT": "ENOENT", "enoent": "ENOENT", "2": "ENOENT",
		"115": "EINPROGRESS", "EWOULDBLOCK": "EAGAIN", "EDEADLOCK": "EDEADLK",
		"EOPNOTSUPP": "ENOTSUP", "95": "ENOTSUP", "EUCLEAN": "EFSCORRUPTED", " EIO ": "EIO"} {
		got, err := ParseErrno(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "41", "-2", "99999", "ENOPE", "E", "0x2"} {
		if got, err := ParseErrno(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %q, %v, want ErrInvalid", bad, got, err)
		}
	}
}
