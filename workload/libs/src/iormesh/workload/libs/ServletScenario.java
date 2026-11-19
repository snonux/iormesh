package iormesh.workload.libs;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.scenarios.HttpCall;
import iormesh.workload.scenarios.HttpFetch;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * The Servlet flavor of the HTTP scenarios (decision #22's third-party
 * set, task de4): the JDK HttpClient sends http-plain's three calls to a
 * servlet in an embedded Tomcat on 127.0.0.1, one HTTP/1.1 keep-alive
 * connection between two processes. The client end is the JDK's own
 * (already hooked); the server end is what this scenario is for — the
 * agent's Servlet hook, rewritten into an application class loader.
 *
 * <pre>
 *   http-servlet  GET /servlet/hello (200), POST /servlet/echo (200),
 *                 GET /servlet/missing (404)
 *
 *   --role server [--port N]   serve the calls, then stop
 *   --role client  --port N    send the calls, then close
 * </pre>
 *
 * Tomcat runs one request thread (maxThreads 1), so the golden can name
 * it: every request is read, answered and recorded there.
 */
public final class ServletScenario implements Scenario {
    static final String NAME = "http-servlet";

    static final List<HttpCall> CALLS = List.of(
            HttpCall.get("/servlet/hello", 200, "hello"),
            HttpCall.post("/servlet/echo", "ping!", 200, "ping!"),
            HttpCall.get("/servlet/missing", 404, ""));

    private static final long SELF_TEST_WAIT_SECONDS = 30;

    /** Public no-argument constructor: ServiceLoader instantiates it. */
    public ServletScenario() {}

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
            HttpFetch.run(base(port), CALLS, null, false);
        }
    }

    /** Both roles in one process: Tomcat's threads, this client. */
    @Override
    public void selfTest() throws Exception {
        try (TomcatServe serve = TomcatServe.start(0, CALLS)) {
            HttpFetch.run(base(serve.port()), CALLS, null, false);
            serve.awaitAll(SELF_TEST_WAIT_SECONDS);
        }
    }

    // No bound on the wait on purpose, as in HttpScenario: a standalone
    // server waits for its client; the harness kills it on failure.
    private static void server(int port) throws Exception {
        try (TomcatServe serve = TomcatServe.start(port, CALLS)) {
            System.out.println("listening " + serve.port());
            System.out.flush();
            serve.awaitAll();
        }
    }

    private static URI base(int port) {
        return URI.create("http://127.0.0.1:" + port + "/");
    }
}
