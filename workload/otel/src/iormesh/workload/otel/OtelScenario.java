package iormesh.workload.otel;

import com.sun.net.httpserver.Headers;
import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.SpanContext;
import io.opentelemetry.api.trace.SpanId;
import io.opentelemetry.api.trace.TraceFlags;
import io.opentelemetry.api.trace.propagation.W3CTraceContextPropagator;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.Scope;
import io.opentelemetry.context.propagation.TextMapGetter;
import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.scenarios.HttpCall;
import iormesh.workload.scenarios.HttpServe;
import java.io.IOException;
import java.net.InetAddress;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * The OpenTelemetry-instrumented flavor (docs/milestones.md M0 catalogue, M3
 * "OpenTelemetry introspection", decision #20): an HTTP client and the
 * JDK HttpServer on 127.0.0.1, two processes, where every call runs in a
 * declared client span and is answered in a declared server span of the
 * same trace, the trace context crossing the wire as W3C traceparent and
 * tracestate headers.
 *
 * <pre>
 *   otel  GET  /otel/hello (200)  trace ...01, client span c11e...a1,
 *                                  server span 5e7e...a1
 *         POST /otel/echo  (200)  trace ...02, client span c11e...a2,
 *                                  server span 5e7e...a2
 *
 *   --role server [--port N]   serve both calls, then stop
 *   --role client  --port N    send both calls, then close
 * </pre>
 *
 * Only the OpenTelemetry API is used, no SDK: a span here is a span
 * context made current ({@code Span.wrap(...).makeCurrent()}), which is
 * exactly what the agent reads ({@code Span.current()}, docs/
 * implementation.md M3 step 8), with ids the golden can declare instead
 * of an SDK's random ones; nothing is exported. The API jars are vendored
 * under workload/otel/third_party and this module is its own jar
 * (workload-otel.jar), so the core workload stays JDK-only (decision #5).
 */
public final class OtelScenario implements Scenario {
    static final String NAME = "otel";

    static final List<Traced> TRACED = List.of(
            Traced.of(HttpCall.get("/otel/hello", 200, "hello"),
                    "a3ce929d0e0e47364bf92f3577b34d01", "c11e0000000000a1", "5e7e0000000000a1"),
            Traced.of(HttpCall.post("/otel/echo", "ping!", 200, "ping!"),
                    "a3ce929d0e0e47364bf92f3577b34d02", "c11e0000000000a2", "5e7e0000000000a2"));

    // A literal address, never a lookup: no resolver I/O in the bracket.
    private static final byte[] LOOPBACK = {127, 0, 0, 1};
    private static final long SELF_TEST_WAIT_SECONDS = 30;

    // Reads the incoming W3C headers from the server's request headers
    // (case-insensitive, as HTTP header names are). get is all the W3C
    // propagator calls; keys has a default.
    private static final TextMapGetter<Headers> HEADERS =
            (carrier, key) -> carrier == null ? null : carrier.getFirst(key);

    /** Public no-argument constructor: ServiceLoader instantiates it. */
    public OtelScenario() {}

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("client", "server"));
        Options.requireKnown(opts, Set.of("port"));
        // Parse the port before any I/O: a bad option is a usage error
        // with an empty bracket.
        int port = role.equals("server") ? Options.port(opts, 0) : Options.requiredPort(opts);
        if (role.equals("server")) {
            server(port);
        } else {
            OtelFetch.run(base(port), TRACED);
        }
    }

    /** Both roles in one process: the server's threads, this client. */
    @Override
    public void selfTest() throws Exception {
        try (HttpServe serve = serve(0)) {
            OtelFetch.run(base(serve.port()), TRACED);
            serve.awaitAll(SELF_TEST_WAIT_SECONDS);
        }
    }

    // No bound on the wait on purpose, as in HttpScenario: a standalone
    // server waits for its client; the harness kills it on failure.
    private static void server(int port) throws Exception {
        try (HttpServe serve = serve(port)) {
            System.out.println("listening " + serve.port());
            System.out.flush();
            serve.awaitAll();
        }
    }

    private static HttpServe serve(int port) throws IOException {
        List<HttpCall> calls = TRACED.stream().map(Traced::call).toList();
        return HttpServe.start(InetAddress.getByAddress(LOOPBACK), port, calls, null,
                OtelScenario::serverSpan);
    }

    /**
     * Makes request n's server span current: the parent comes from the
     * request's W3C headers, the span id from the declaration. A request
     * without valid headers still gets a region (an invalid span); its
     * HttpCall check fails the run anyway, since the headers are declared.
     */
    private static HttpServe.Region serverSpan(int n, Headers request) {
        Context parent = W3CTraceContextPropagator.getInstance()
                .extract(Context.root(), request, HEADERS);
        SpanContext remote = Span.fromContext(parent).getSpanContext();
        String id = n < TRACED.size() ? TRACED.get(n).serverSpan() : SpanId.getInvalid();
        Span span = Span.wrap(SpanContext.create(remote.getTraceId(), id,
                TraceFlags.getSampled(), remote.getTraceState()));
        Scope scope = parent.with(span).makeCurrent();
        return scope::close;
    }

    private static URI base(int port) {
        return URI.create("http://127.0.0.1:" + port + "/");
    }
}
