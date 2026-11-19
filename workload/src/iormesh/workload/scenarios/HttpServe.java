package iormesh.workload.scenarios;

import com.sun.net.httpserver.HttpExchange;
import com.sun.net.httpserver.HttpServer;
import com.sun.net.httpserver.HttpsConfigurator;
import com.sun.net.httpserver.HttpsServer;
import java.io.IOException;
import java.io.OutputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import javax.net.ssl.SSLContext;

/**
 * The server end of the HTTP scenarios: the JDK's built-in HttpServer (or
 * HttpsServer), one of decision #22's v1 hook targets and the
 * microservices workload's REST server. It answers exactly the declared
 * calls, in order, then lets its owner stop it. With no executor set the
 * JDK runs {@link #handle} on its dispatcher thread, so every server-side
 * request — and the http event the agent records for it — has one
 * thread and one causing method: HttpServe#handle.
 */
final class HttpServe implements AutoCloseable {
    private final HttpServer server;
    private final List<HttpCall> calls;
    private final AtomicInteger next = new AtomicInteger();
    private final CountDownLatch done;
    private volatile String failure;

    private HttpServe(HttpServer server, List<HttpCall> calls) {
        this.server = server;
        this.calls = calls;
        this.done = new CountDownLatch(calls.size());
    }

    /** Binds addr:port (0 = kernel-picked) and starts serving calls. */
    static HttpServe start(InetAddress addr, int port, List<HttpCall> calls, SSLContext tls)
            throws IOException {
        // Backlog 1: the client opens exactly one connection.
        InetSocketAddress at = new InetSocketAddress(addr, port);
        HttpServer server;
        if (tls == null) {
            server = HttpServer.create(at, 1);
        } else {
            HttpsServer s = HttpsServer.create(at, 1);
            s.setHttpsConfigurator(new HttpsConfigurator(tls));
            server = s;
        }
        HttpServe serve = new HttpServe(server, calls);
        server.createContext("/", serve::handle);
        server.start();
        return serve;
    }

    int port() {
        return server.getAddress().getPort();
    }

    /** Waits, without a bound, until every declared call was answered. */
    void awaitAll() throws Exception {
        done.await();
        check();
    }

    /** awaitAll with a bound, for --self-test where both ends share a JVM. */
    void awaitAll(long seconds) throws Exception {
        if (!done.await(seconds, TimeUnit.SECONDS)) {
            throw new IOException("server answered " + next.get() + " of " + calls.size()
                    + " requests");
        }
        check();
    }

    private void check() throws IOException {
        if (failure != null) {
            throw new IOException("server: " + failure);
        }
    }

    /**
     * Answers request n with call n. A request the declaration does not
     * expect gets 500 and fails the run; the latch is then released in
     * full, so a standalone server never waits for requests that will not
     * come. The latch counts down only after the exchange is closed, so an
     * owner that stops the server next never cuts a reply short.
     */
    private void handle(HttpExchange ex) throws IOException {
        int n = next.getAndIncrement();
        try {
            String body = new String(ex.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
            HttpCall call = n < calls.size() ? calls.get(n) : null;
            String problem = call == null ? "unexpected request " + (n + 1)
                    : call.mismatch(ex.getRequestMethod(), target(ex), ex.getRequestHeaders(),
                            body);
            if (problem != null) {
                failure = problem;
                ex.sendResponseHeaders(500, -1);
            } else {
                reply(ex, call);
            }
        } finally {
            ex.close();
            done.countDown();
            while (failure != null && done.getCount() > 0) {
                done.countDown();
            }
        }
    }

    /** The request's raw path and query, as the client wrote them. */
    private static String target(HttpExchange ex) {
        String query = ex.getRequestURI().getRawQuery();
        return ex.getRequestURI().getRawPath() + (query == null ? "" : "?" + query);
    }

    private static void reply(HttpExchange ex, HttpCall call) throws IOException {
        for (Map.Entry<String, String> h : call.replyHeaders().entrySet()) {
            ex.getResponseHeaders().add(h.getKey(), h.getValue());
        }
        byte[] reply = call.reply().getBytes(StandardCharsets.UTF_8);
        // -1: no body at all (Content-Length 0), the 404's shape.
        ex.sendResponseHeaders(call.status(), reply.length == 0 ? -1 : reply.length);
        try (OutputStream out = ex.getResponseBody()) {
            out.write(reply);
        }
    }

    /** Stops at once: every declared exchange is complete by now. */
    @Override
    public void close() {
        server.stop(0);
    }
}
