package iormesh.workload.otel;

import io.opentelemetry.api.trace.SpanContext;
import io.opentelemetry.api.trace.TraceFlags;
import io.opentelemetry.api.trace.TraceState;
import iormesh.workload.scenarios.HttpCall;

/**
 * One declared call of the otel scenario and the spans it runs in: the
 * client's span (trace id, client span id), current while the client
 * sends the call, and the server's child span (same trace id, server span
 * id), current while the server answers it. The ids are fixed so the
 * golden can name them (expect-pending.json's otel objects); a real SDK
 * would draw them at random.
 *
 * <p>{@link #call} carries the W3C trace-context headers the client's
 * propagator must produce for the client span, as declared request
 * headers: the server's HttpCall check fails the run unless they arrive,
 * so the propagation is proven on the wire, not assumed.
 *
 * @param call        the HTTP call with its traceparent/tracestate headers
 * @param traceId     32 lowercase hex digits, shared by both spans
 * @param clientSpan  16 hex digits, the client span's id
 * @param serverSpan  16 hex digits, the server span's id
 */
record Traced(HttpCall call, String traceId, String clientSpan, String serverSpan) {
    // A vendor entry rides along in tracestate, so the scenario exercises
    // both W3C headers; the propagator forwards it unchanged.
    static final TraceState STATE = TraceState.builder().put("iormesh", "otel").build();

    /** Declares call inside the given spans, adding its trace headers. */
    static Traced of(HttpCall call, String traceId, String clientSpan, String serverSpan) {
        HttpCall withHeaders = call
                .withHeader("traceparent", "00-" + traceId + "-" + clientSpan + "-01")
                .withHeader("tracestate", "iormesh=otel");
        return new Traced(withHeaders, traceId, clientSpan, serverSpan);
    }

    /** The client span, sampled, as the client makes it current. */
    SpanContext clientContext() {
        return SpanContext.create(traceId, clientSpan, TraceFlags.getSampled(), STATE);
    }
}
