package iormesh.workload.libs;

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
 * The Apache HttpClient flavor of the HTTP scenarios (decision #22's
 * third-party set, task de4): Apache HttpClient 5's classic client sends
 * http-plain's three calls to the JDK's HttpServer on 127.0.0.1, one
 * HTTP/1.1 keep-alive connection between two processes. The server end
 * is the JDK's own (already hooked); the client end is what this
 * scenario is for — the agent's Apache HttpClient hooks, rewritten into
 * an application class loader.
 *
 * <pre>
 *   http-apache  GET /apache/hello (200), POST /apache/echo (200),
 *                GET /apache/missing (404)
 *
 *   --role server [--port N]   serve the calls, then stop
 *   --role client  --port N    send the calls, then close
 * </pre>
 */
public final class ApacheScenario implements Scenario {
    static final String NAME = "http-apache";

    static final List<HttpCall> CALLS = List.of(
            HttpCall.get("/apache/hello", 200, "hello"),
            HttpCall.post("/apache/echo", "ping!", 200, "ping!"),
            HttpCall.get("/apache/missing", 404, ""));

    private static final long SELF_TEST_WAIT_SECONDS = 30;

    /** Public no-argument constructor: ServiceLoader instantiates it. */
    public ApacheScenario() {}

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
            ApacheFetch.run(base(port), CALLS);
        }
    }

    /** Both roles in one process: the JDK server's threads, this client. */
    @Override
    public void selfTest() throws Exception {
        try (HttpServe serve = serve(0)) {
            ApacheFetch.run(base(serve.port()), CALLS);
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

    static HttpServe serve(int port) throws IOException {
        // A literal address, never a lookup: no resolver I/O in the bracket.
        InetAddress loopback = InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        return HttpServe.start(loopback, port, CALLS, null, (n, request) -> () -> { });
    }

    private static URI base(int port) {
        return URI.create("http://127.0.0.1:" + port + "/");
    }
}
