package iormesh.workload.otel;

import io.opentelemetry.api.trace.Span;
import io.opentelemetry.api.trace.propagation.W3CTraceContextPropagator;
import io.opentelemetry.context.Context;
import io.opentelemetry.context.Scope;
import iormesh.workload.scenarios.HttpCall;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.Proxy;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * The otel scenario's client: each call is sent inside its declared
 * client span, with the trace context injected by OpenTelemetry's W3C
 * propagator.
 *
 * <p>HttpURLConnection, not the JDK HttpClient the http-* scenarios use:
 * OTEL's current span lives in a thread-local, and HttpClient does its
 * socket I/O on its own selector thread, where no span is current — the
 * client's connect, writes and reads would carry no ids (only the http
 * event, recorded on the calling thread, would). HttpURLConnection does
 * blocking I/O on the calling thread, inside the span, so every client
 * event of a call carries that call's ids. It is also on decision #22's
 * v1 hook list. One keep-alive connection serves both calls; the last
 * call closes it (disconnect), so the close is in the bracket too.
 */
final class OtelFetch {
    // Same bound as HttpFetch: only stops a run against a hung server.
    private static final int TIMEOUT_MILLIS = 30_000;

    private OtelFetch() {}

    /** Sends each traced call in order to base, inside its client span. */
    static void run(URI base, List<Traced> traced) throws IOException {
        for (int i = 0; i < traced.size(); i++) {
            Traced t = traced.get(i);
            // try/finally, not try-with-resources: the scope is never
            // read inside, which javac's -Xlint:try (and -Werror) rejects.
            Scope scope = Span.wrap(t.clientContext()).makeCurrent();
            try {
                call(base, t.call(), i == traced.size() - 1);
            } finally {
                scope.close();
            }
        }
    }

    /**
     * One call on the pooled connection: the causing method of the
     * client's http event. last closes the connection after the reply.
     */
    static void call(URI base, HttpCall c, boolean last) throws IOException {
        // NO_PROXY: a proxy setting on the host must not reroute the call.
        HttpURLConnection conn = (HttpURLConnection) base.resolve(c.target()).toURL()
                .openConnection(Proxy.NO_PROXY);
        conn.setConnectTimeout(TIMEOUT_MILLIS);
        conn.setReadTimeout(TIMEOUT_MILLIS);
        conn.setRequestMethod(c.method());
        injectTraceContext(conn, c);
        if (!c.body().isEmpty()) {
            conn.setDoOutput(true);
            try (OutputStream out = conn.getOutputStream()) {
                out.write(c.body().getBytes(StandardCharsets.UTF_8));
            }
        }
        int status = conn.getResponseCode();
        InputStream in = status >= 400 ? conn.getErrorStream() : conn.getInputStream();
        String reply = in == null ? "" : new String(in.readAllBytes(), StandardCharsets.UTF_8);
        if (last) {
            // After a full read, disconnect closes the idle connection at
            // once; closing the stream would park it in the keep-alive
            // cache, whose timer thread closes it seconds later, outside
            // the bracket.
            conn.disconnect();
        } else if (in != null) {
            in.close();
        }
        if (status != c.status() || !reply.equals(c.reply())) {
            throw new IOException(c.where() + ": want " + c.status() + " " + c.reply()
                    + ", got " + status + " " + reply);
        }
    }

    /**
     * Injects the current context's W3C headers and checks them against
     * the call's declared ones: the ids the golden names must be the ids
     * on the wire.
     */
    private static void injectTraceContext(HttpURLConnection conn, HttpCall c)
            throws IOException {
        Map<String, String> carrier = new LinkedHashMap<>();
        W3CTraceContextPropagator.getInstance().inject(Context.current(), carrier, Map::put);
        for (String h : List.of("traceparent", "tracestate")) {
            if (!c.headers().get(h).equals(carrier.get(h))) {
                throw new IOException(c.where() + ": propagator wrote " + h + " "
                        + carrier.get(h) + ", declared " + c.headers().get(h));
            }
        }
        carrier.forEach(conn::setRequestProperty);
    }
}
