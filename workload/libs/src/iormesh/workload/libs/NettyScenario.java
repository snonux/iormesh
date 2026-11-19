package iormesh.workload.libs;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.scenarios.HttpCall;
import iormesh.workload.scenarios.HttpFetch;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * The Netty flavor of the HTTP scenarios (decision #22's third-party
 * set, task de4): the JDK HttpClient sends http-plain's three calls to a
 * Netty HTTP/1.1 server on 127.0.0.1, one keep-alive connection between
 * two processes. The client end is the JDK's own (already hooked); the
 * server end is what this scenario is for — the agent's Netty codec
 * hooks, rewritten into an application class loader.
 *
 * <pre>
 *   http-netty  GET /netty/hello (200), POST /netty/echo (200),
 *               GET /netty/missing (404)
 *
 *   --role server [--port N]   serve the calls, then stop
 *   --role client  --port N    send the calls, then close
 * </pre>
 */
public final class NettyScenario implements Scenario {
    static final String NAME = "http-netty";

    static final List<HttpCall> CALLS = List.of(
            HttpCall.get("/netty/hello", 200, "hello"),
            HttpCall.post("/netty/echo", "ping!", 200, "ping!"),
            HttpCall.get("/netty/missing", 404, ""));

    private static final long SELF_TEST_WAIT_SECONDS = 30;

    /** Public no-argument constructor: ServiceLoader instantiates it. */
    public NettyScenario() {}

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
            HttpFetch.run(Loopback.base(port), CALLS, null, false);
        }
    }

    /** Both roles in one process: Netty's event loops, this client. */
    @Override
    public void selfTest() throws Exception {
        try (NettyServe serve = NettyServe.start(0, CALLS)) {
            HttpFetch.run(Loopback.base(serve.port()), CALLS, null, false);
            serve.awaitAll(SELF_TEST_WAIT_SECONDS);
        }
    }

    // No bound on the wait on purpose, as in HttpScenario: a standalone
    // server waits for its client; the harness kills it on failure.
    private static void server(int port) throws Exception {
        try (NettyServe serve = NettyServe.start(port, CALLS)) {
            System.out.println("listening " + serve.port());
            System.out.flush();
            serve.awaitAll();
        }
    }
}
