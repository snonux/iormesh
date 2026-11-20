package iormesh.workload.scenarios;

import com.sun.net.httpserver.Headers;
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
 * thread and one causing method: HttpServe#handle. Public for the OTEL
 * flavor's separate module (workload/otel), which serves its calls
 * through the same handler inside a {@link PerRequest} region. A
 * microservices service hangs its own {@link Work} on a request — the
 * downstream calls and the data write it makes before it answers — so
 * that work runs on the dispatcher thread inside the request, the way a
 * synchronous REST service handles it.
 */
public final class HttpServe implements AutoCloseable {
    /**
     * What one request's handling runs inside: opened from the request
     * headers before the handler touches the body, closed after the
     * exchange is. The OTEL flavor makes the request's server span current
     * here, so the reply's I/O and the exchange's close carry its ids; the
     * request line and headers were read before (by the dispatcher, ahead
     * of the handler), so those reads cannot. The plain scenarios open
     * nothing.
     */
    @FunctionalInterface
    public interface PerRequest {
        /** Opens the region for request n (0-based); must not throw. */
        Region enter(int n, Headers request);
    }

    /** An opened {@link PerRequest} region; closing it cannot fail. */
    @FunctionalInterface
    public interface Region extends AutoCloseable {
        @Override
        void close();
    }

    /**
     * What request n's handling does before the reply (the microservices
     * scenario's downstream calls and data write). It runs after the
     * request matched its declaration; a throw answers 500 and fails the
     * run like a mismatch, so a broken hop never passes as a served call.
     */
    @FunctionalInterface
    public interface Work {
        void before(int n) throws Exception;
    }

    private static final PerRequest NO_REGION = (n, request) -> () -> {};
    private static final Work NO_WORK = n -> {};

    private final HttpServer server;
    private final List<HttpCall> calls;
    private final PerRequest perRequest;
    private final Work work;
    private final AtomicInteger next = new AtomicInteger();
    private final CountDownLatch done;
    private volatile String failure;

    private HttpServe(HttpServer server, List<HttpCall> calls, PerRequest perRequest,
            Work work) {
        this.server = server;
        this.calls = calls;
        this.perRequest = perRequest;
        this.work = work;
        this.done = new CountDownLatch(calls.size());
    }

    /** Binds addr:port (0 = kernel-picked) and starts serving calls. */
    static HttpServe start(InetAddress addr, int port, List<HttpCall> calls, SSLContext tls)
            throws IOException {
        return start(addr, port, calls, tls, NO_REGION);
    }

    /** start, with each request handled inside perRequest's region. */
    public static HttpServe start(InetAddress addr, int port, List<HttpCall> calls,
            SSLContext tls, PerRequest perRequest) throws IOException {
        return start(addr, port, calls, tls, perRequest, NO_WORK);
    }

    /** start, with work run before each reply (a microservices service). */
    static HttpServe startWorking(InetAddress addr, int port, List<HttpCall> calls,
            SSLContext tls, Work work) throws IOException {
        return start(addr, port, calls, tls, NO_REGION, work);
    }

    private static HttpServe start(InetAddress addr, int port, List<HttpCall> calls,
            SSLContext tls, PerRequest perRequest, Work work) throws IOException {
        // Backlog 1: each client opens exactly one connection, and a
        // service called by two (microservices' inventory) is called by
        // one after the other, never both at once.
        InetSocketAddress at = new InetSocketAddress(addr, port);
        HttpServer server;
        if (tls == null) {
            server = HttpServer.create(at, 1);
        } else {
            HttpsServer s = HttpsServer.create(at, 1);
            s.setHttpsConfigurator(new HttpsConfigurator(tls));
            server = s;
        }
        HttpServe serve = new HttpServe(server, calls, perRequest, work);
        server.createContext("/", serve::handle);
        server.start();
        return serve;
    }

    public int port() {
        return server.getAddress().getPort();
    }

    /** Waits, without a bound, until every declared call was answered. */
    public void awaitAll() throws Exception {
        done.await();
        check();
    }

    /** awaitAll with a bound, for --self-test where both ends share a JVM. */
    public void awaitAll(long seconds) throws Exception {
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
     * Answers request n with call n, after its {@link Work}. A request the
     * declaration does not expect, or whose work throws, gets 500 and
     * fails the run; the latch is then released in full, so a standalone
     * server never waits for requests that will not come. The latch counts down only after the exchange is closed, so an
     * owner that stops the server next never cuts a reply short. The
     * region is closed after ex.close(), so the close runs inside it too.
     */
    private void handle(HttpExchange ex) throws IOException {
        int n = next.getAndIncrement();
        Region region = perRequest.enter(n, ex.getRequestHeaders());
        try {
            String body = new String(ex.getRequestBody().readAllBytes(), StandardCharsets.UTF_8);
            HttpCall call = n < calls.size() ? calls.get(n) : null;
            String problem = call == null ? "unexpected request " + (n + 1)
                    : call.mismatch(ex.getRequestMethod(), target(ex), ex.getRequestHeaders(),
                            body);
            if (problem == null) {
                problem = runWork(n);
            }
            if (problem != null) {
                failure = problem;
                ex.sendResponseHeaders(500, -1);
            } else {
                reply(ex, call);
            }
        } finally {
            // Nested: a close that throws must still close the region —
            // or the OTEL scope stays current on the reused dispatcher
            // thread and the next request's I/O carries stale ids — and
            // still count the request down.
            try {
                ex.close();
            } finally {
                region.close();
                done.countDown();
                while (failure != null && done.getCount() > 0) {
                    done.countDown();
                }
            }
        }
    }

    /** Runs request n's work; returns what went wrong, or null. */
    private String runWork(int n) {
        try {
            work.before(n);
            return null;
        } catch (Exception e) {
            return "request " + (n + 1) + ": " + e;
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
