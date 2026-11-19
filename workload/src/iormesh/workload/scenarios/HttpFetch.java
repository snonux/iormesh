package iormesh.workload.scenarios;

import java.io.IOException;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import javax.net.ssl.SSLContext;

/**
 * The client end of the HTTP scenarios: the JDK's HttpClient, another of
 * decision #22's v1 hook targets. HTTP/1.1 is forced — v1 pairs requests
 * across the wire by connection and per-connection ordinal, which an
 * HTTP/2 connection's multiplexing would break (PLAN.md "Request pairing
 * across the wire"). The calls go out one after another on one client,
 * so the pool reuses one keep-alive connection for all of them. Each
 * request is sent from {@link #call}, the causing method the http event's
 * Java stack must name; the socket I/O itself may run on the client's
 * selector thread.
 */
final class HttpFetch {
    // Bounds a run against a server that accepted but never answers; far
    // above any loopback exchange, so it never decides a healthy run.
    private static final Duration TIMEOUT = Duration.ofSeconds(30);

    private HttpFetch() {}

    /**
     * Sends calls in order to base (http(s)://127.0.0.1:port, or
     * localhost for http-async); async sends each through sendAsync and
     * waits for it before the next, so the calls still share one
     * keep-alive connection in order.
     */
    static void run(URI base, List<HttpCall> calls, SSLContext tls, boolean async)
            throws Exception {
        HttpClient.Builder b = HttpClient.newBuilder()
                .version(HttpClient.Version.HTTP_1_1)
                .connectTimeout(TIMEOUT);
        if (tls != null) {
            b.sslContext(tls);
        }
        // close() (JDK 21) shuts the pooled connection down before the
        // scenario's end marker, so the client's close is in the bracket.
        try (HttpClient client = b.build()) {
            for (HttpCall c : calls) {
                if (async) {
                    callAsync(client, base, c);
                } else {
                    call(client, base, c);
                }
            }
        }
    }

    /** One declared call: send it, then check status, body and headers. */
    static void call(HttpClient client, URI base, HttpCall c) throws Exception {
        HttpResponse<String> resp = client.send(request(base, c),
                HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8));
        check(c, resp);
    }

    /**
     * The same through sendAsync, the causing method of http-async's
     * client events: the agent records the request from the completing
     * thread under this caller's stack.
     */
    static void callAsync(HttpClient client, URI base, HttpCall c) throws Exception {
        HttpResponse<String> resp = client.sendAsync(request(base, c),
                HttpResponse.BodyHandlers.ofString(StandardCharsets.UTF_8))
                .get(TIMEOUT.toSeconds(), java.util.concurrent.TimeUnit.SECONDS);
        check(c, resp);
    }

    private static HttpRequest request(URI base, HttpCall c) {
        HttpRequest.Builder req = HttpRequest.newBuilder(base.resolve(c.target()))
                .timeout(TIMEOUT)
                .method(c.method(), c.body().isEmpty() ? HttpRequest.BodyPublishers.noBody()
                        : HttpRequest.BodyPublishers.ofString(c.body(), StandardCharsets.UTF_8));
        c.headers().forEach(req::header);
        return req.build();
    }

    /** The reply must be the declared status, body and reply headers. */
    private static void check(HttpCall c, HttpResponse<String> resp) throws IOException {
        if (resp.statusCode() != c.status() || !resp.body().equals(c.reply())) {
            throw new IOException(c.where() + ": want " + c.status() + " " + c.reply()
                    + ", got " + resp.statusCode() + " " + resp.body());
        }
        for (Map.Entry<String, String> h : c.replyHeaders().entrySet()) {
            if (!resp.headers().allValues(h.getKey()).contains(h.getValue())) {
                // The value may be a planted secret: name the header only.
                throw new IOException(c.where() + ": reply header " + h.getKey()
                        + " missing or different");
            }
        }
    }
}
