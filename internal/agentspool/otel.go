package agentspool

import "fmt"

// Otel is the OpenTelemetry span a record ran in (decision #20, task
// 4b4): the agent reads the app's current span context at a bracket's
// enter and when an HTTP request is recorded. The trace id travels as
// its 32 hex digits' upper and lower 64 bits, the span id as 64 bits;
// the zero value means no span (W3C ids are never all zero).
type Otel struct {
	TraceHi, TraceLo, Span uint64
}

// Valid reports whether o names a span: a non-zero trace id and span id.
func (o Otel) Valid() bool {
	return (o.TraceHi|o.TraceLo) != 0 && o.Span != 0
}

// TraceID is the trace id in W3C form, 32 lowercase hex digits.
func (o Otel) TraceID() string {
	return fmt.Sprintf("%016x%016x", o.TraceHi, o.TraceLo)
}

// SpanID is the span id in W3C form, 16 lowercase hex digits.
func (o Otel) SpanID() string {
	return fmt.Sprintf("%016x", o.Span)
}

// decodeOtel reads the three id words at rec[off:]; ok is false for a
// half-set triple (a trace id without a span id, or the reverse), which
// the agent never writes.
func decodeOtel(rec []byte, off int) (o Otel, ok bool) {
	o = Otel{TraceHi: le.Uint64(rec[off:]), TraceLo: le.Uint64(rec[off+8:]),
		Span: le.Uint64(rec[off+16:])}
	return o, o.Valid() || o == Otel{}
}
