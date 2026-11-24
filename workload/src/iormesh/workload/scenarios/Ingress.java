package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.UsageException;
import java.net.InetAddress;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.regex.Pattern;

/**
 * M7's ingress scenario (docs/milestones.md M0 "Ingress scenario"): the
 * microservices scenario's front door — the gateway asking for the
 * inventory's stock — through an ingress controller (Traefik in kind) by
 * an FQDN, so no tracer sees the hop in the middle and the route map has
 * to complete the edge (client → FQDN → ingress → Service → workload).
 *
 * <pre>
 *   --role backend --bind A [--port N]   serve the one call, then stop
 *   --role gateway --fqdn NAME [--port N] GET http://NAME[:N]/inventory/
 *                                         stock/widget, then close
 * </pre>
 *
 * The route map needs the request's host, which only the Java agent
 * reports, and a debug container live-attaches the agent only to JVMs
 * already running when it starts (docs/java-attach.md). So in kind the
 * gateway is started behind Main's startup gate
 * (IORMESH_WORKLOAD_STARTUP_FILE), before its bracket, and released once
 * the tracer is attached.
 *
 * The gateway names the FQDN on purpose — unlike every other scenario,
 * it resolves a name inside its bracket: the route map matches the
 * request's HTTP host (and the agent's DNS lookup), and the FQDN is what
 * the ingress routes by. In kind the name resolves through the pod's
 * hostAliases to the ingress controller's ClusterIP, as a real FQDN
 * resolves to the controller's load balancer. The backend binds its pod
 * address (an IPv4 literal, {@link TcpHosts#ipv4}); the ingress
 * controller connects to it directly, so both of the backend's and the
 * gateway's connections end at an untraced peer.
 *
 * {@code --self-test} runs both roles in this process on loopback, the
 * gateway calling 127.0.0.1 directly: one JVM has no ingress, and a
 * self-test resolves nothing.
 */
public final class Ingress implements Scenario {
    static final String NAME = "ingress";
    static final String FQDN = "fqdn";
    private static final long SELF_TEST_WAIT_SECONDS = 30;
    // Two or more DNS labels, letters, digits and inner hyphens: what a
    // route map's FQDN may be (internal/routemap), never a literal.
    private static final Pattern NAME_RULE = Pattern.compile(
            "(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+");
    /** The gateway's stock query, as microservices' gateway asks it. */
    static final List<HttpCall> CALLS = List.of(
            HttpCall.get("/inventory/stock/widget", 200, "widget: 7"));

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public Set<String> roles() {
        return Set.of("backend", "gateway");
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, roles());
        boolean backend = role.equals("backend");
        Options.requireKnown(opts, Set.of(backend ? TcpHosts.BIND : FQDN, "port"));
        // Every option is parsed before any I/O: a bad one is a usage
        // error with an empty bracket.
        if (backend) {
            InetAddress bind = TcpHosts.ipv4(opts, TcpHosts.BIND);
            serve(bind, Options.port(opts, 0));
        } else {
            String host = fqdn(opts);
            int port = Options.port(opts, 80);
            HttpFetch.run(URI.create("http://" + host + (port == 80 ? "" : ":" + port) + "/"),
                    CALLS, null, false);
        }
    }

    // No bound on the wait: a standalone backend waits for the ingress as
    // long as it takes; the harness deletes the pod on failure.
    private static void serve(InetAddress bind, int port) throws Exception {
        try (HttpServe serve = HttpServe.start(bind, port, CALLS, null)) {
            System.out.println("listening " + serve.port());
            System.out.flush();
            serve.awaitAll();
        }
    }

    /** The --fqdn option, checked against NAME_RULE. */
    static String fqdn(Map<String, String> opts) throws UsageException {
        String v = opts.get(FQDN);
        if (v == null) {
            throw new UsageException("missing --" + FQDN);
        }
        if (v.length() > 253 || !NAME_RULE.matcher(v).matches() || v.matches("[0-9.]+")) {
            throw new UsageException("--" + FQDN + " is no DNS name of two or more labels: " + v);
        }
        return v;
    }

    @Override
    public void selfTest() throws Exception {
        InetAddress lo = InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        try (HttpServe serve = HttpServe.start(lo, 0, CALLS, null)) {
            HttpFetch.run(URI.create("http://127.0.0.1:" + serve.port() + "/"), CALLS, null, false);
            serve.awaitAll(SELF_TEST_WAIT_SECONDS);
        }
    }
}
