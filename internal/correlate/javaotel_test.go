package correlate

import (
	"testing"

	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// testSpan is the otel scenario's first client span as the ring carries it.
var testSpan = agentspool.Otel{TraceHi: 0xa3ce929d0e0e4736, TraceLo: 0x4bf92f3577b34d01,
	Span: 0xc11e0000000000a1}

var wantSpan = schema.Otel{TraceID: "a3ce929d0e0e47364bf92f3577b34d01", SpanID: "c11e0000000000a1"}

// TestJavaJoinOtel (task 4b4): a syscall joined to a bracket opened
// inside a span carries the span's ids; one joined to a bracket outside
// any span, and one no bracket explains, carry none.
func TestJavaJoinOtel(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	inSpan := jt.bracket(1, mainTID, at-50, at+500, 10, 1)
	inSpan.Otel = testSpan
	jt.ring(inSpan, jt.bracket(1, mainTID, at+950, at+1500, 10, 1),
		jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "write", at)
	jt.event(jvmPID, mainTID, "write", at+1000)
	jt.event(jvmPID, mainTID, "write", at+3000)
	jt.close()
	evs := jt.sink.events
	if len(evs) != 3 || evs[0].Otel == nil || *evs[0].Otel != wantSpan {
		t.Fatalf("events %+v, want the first in span %+v", evs, wantSpan)
	}
	if evs[1].Attribution != schema.AttributionJava || evs[1].Otel != nil {
		t.Errorf("bracket outside a span: %s otel %+v, want java without otel",
			evs[1].Attribution, evs[1].Otel)
	}
	if evs[2].Attribution != schema.AttributionNone || evs[2].Otel != nil {
		t.Errorf("unbracketed: %s otel %+v, want none without otel", evs[2].Attribution, evs[2].Otel)
	}
}

// TestJavaJoinHTTPOtel: an http event carries its record's span; one
// whose stack never came stays attribution none and so carries no ids
// either — the schema ties otel to java.
func TestJavaJoinHTTPOtel(t *testing.T) {
	jt := newJoinTest(t)
	at := jt.now + 1000
	withStack := jt.request(at+1000, "/a", "", "")
	withStack.Otel = testSpan
	noStack := jt.request(at+2000, "/b", "", "")
	noStack.StackID, noStack.Otel = 0, testSpan
	jt.ring(withStack, noStack, jt.watermark(at+20_000_000))
	jt.event(jvmPID, mainTID, "write", at) // the stream's envelope (project, host)
	jt.close()
	evs := jt.sink.events[1:]
	if len(evs) != 2 || evs[0].Otel == nil || *evs[0].Otel != wantSpan {
		t.Fatalf("events %+v, want the first in span %+v", evs, wantSpan)
	}
	if evs[1].Attribution != schema.AttributionNone || evs[1].Otel != nil {
		t.Errorf("stackless http event: %s otel %+v", evs[1].Attribution, evs[1].Otel)
	}
}
