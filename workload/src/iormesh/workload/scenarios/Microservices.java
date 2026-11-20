package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.util.ArrayList;
import java.util.EnumMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeSet;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.TimeUnit;

/**
 * M4's microservices scenario (PLAN.md M0 "Microservices end-to-end
 * scenario"): four service roles talking REST — a chain, a fan-out and a
 * fan-in, plain HTTP and TLS — each also doing its own file work. The
 * topology is {@link Service}'s; one service is one process, as it is one
 * host or pod in a real deployment:
 *
 * <pre>
 *   --role billing   [--port N] [--dir D]
 *   --role inventory [--port N] [--dir D]
 *   --role orders    [--port N] [--dir D] --inventory-port N --billing-port N
 *   --role gateway              [--dir D] --orders-port N --inventory-port N
 * </pre>
 *
 * A server prints {@code listening <port>} after the PID once it listens
 * (and, if TLS, has published its certificate to D/role.crt); start them
 * leaves first — billing, inventory, orders — each only after the one
 * before announced itself, then the gateway, whose checkout drives the
 * run. Every server answers its declared calls and exits; the gateway
 * exits after its checkout. The TLS hops need one shared D: a caller pins
 * the certificate its callee published there, never anything else.
 *
 * Loopback only for now: the servers bind 127.0.0.1 and the certificates
 * name it; the cross-host run (one service per host) brings the address
 * options with M4's multi-host test.
 *
 * {@code --self-test} runs the whole topology in this one process on
 * loopback: each server on its own platform thread in the same order,
 * then the gateway's checkout on this one.
 */
public final class Microservices implements Scenario {
    static final String NAME = "microservices";
    private static final long SELF_TEST_WAIT_SECONDS = 30;
    private static final List<Service> SERVER_START_ORDER = List.of(Service.BILLING,
            Service.INVENTORY, Service.ORDERS);

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public Set<String> roles() {
        Set<String> roles = new HashSet<>();
        for (Service s : Service.values()) {
            roles.add(s.role);
        }
        return roles;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Service svc = Service.of(role);
        Options.requireRole(NAME, role, roles());
        Options.requireKnown(opts, options(svc));
        // Every option is parsed before any I/O: a bad one is a usage
        // error with an empty bracket, never a half-booted service.
        Map<Service, Integer> ports = downstreamPorts(svc, opts);
        int port = svc.serves.isEmpty() ? 0 : Options.port(opts, 0);
        try (Workdir wd = Workdir.open(opts)) {
            ServiceNode node = new ServiceNode(svc, wd, ports);
            if (svc.serves.isEmpty()) {
                node.checkout();
            } else {
                node.serve(port, Microservices::announce, 0);
            }
        }
    }

    /** The options a role takes: --dir, --port if it serves, a port per callee. */
    private static Set<String> options(Service svc) {
        Set<String> known = new TreeSet<>(Set.of(Workdir.OPTION));
        if (!svc.serves.isEmpty()) {
            known.add("port");
        }
        for (Service to : svc.downstream()) {
            known.add(to.portOption());
        }
        return known;
    }

    private static Map<Service, Integer> downstreamPorts(Service svc, Map<String, String> opts)
            throws Exception {
        Map<Service, Integer> ports = new EnumMap<>(Service.class);
        for (Service to : svc.downstream()) {
            ports.put(to, Options.requiredPort(opts, to.portOption()));
        }
        return ports;
    }

    private static void announce(int port) {
        System.out.println("listening " + port);
        System.out.flush();
    }

    /**
     * The whole topology in one process: the servers on their own threads,
     * leaves first, each told the ports of the ones already listening;
     * then the gateway's checkout on this thread. Every server is joined
     * even when the checkout failed, so its failure is reported too.
     */
    @Override
    public void selfTest() throws Exception {
        try (Workdir wd = Workdir.temp()) {
            Map<Service, Integer> ports = new EnumMap<>(Service.class);
            List<Peer> servers = new ArrayList<>();
            Exception failure = null;
            try {
                for (Service s : SERVER_START_ORDER) {
                    servers.add(startServer(s, wd, ports));
                }
                new ServiceNode(Service.GATEWAY, wd, ports).checkout();
            } catch (Exception e) {
                failure = e;
            }
            for (Peer p : servers) {
                try {
                    p.join();
                } catch (Exception e) {
                    failure = failure == null ? e : failure;
                }
            }
            if (failure != null) {
                throw failure;
            }
        }
    }

    /**
     * Starts server s on a peer thread and waits for its port, which it
     * adds to ports for the services started after it.
     */
    private static Peer startServer(Service s, Workdir wd, Map<Service, Integer> ports)
            throws Exception {
        CompletableFuture<Integer> listening = new CompletableFuture<>();
        Map<Service, Integer> known = new EnumMap<>(ports);
        Peer peer = Peer.start(NAME + "-" + s.role, () -> {
            try {
                new ServiceNode(s, wd, known).serve(0, listening::complete,
                        SELF_TEST_WAIT_SECONDS);
            } catch (Exception e) {
                // A server that dies before listening must not leave the
                // self-test waiting for a port that never comes.
                listening.completeExceptionally(e);
                throw e;
            }
        });
        ports.put(s, listening.get(SELF_TEST_WAIT_SECONDS, TimeUnit.SECONDS));
        return peer;
    }
}
