package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.UsageException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
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
 *   --role billing   [--bind A] [--port N] [--dir D]
 *   --role inventory [--bind A] [--port N] [--dir D]
 *   --role orders    [--bind A] [--port N] [--dir D]
 *                    --inventory-port N [--inventory-host A]
 *                    --billing-port N   [--billing-host A]
 *   --role gateway                         [--dir D]
 *                    --orders-port N    [--orders-host A]
 *                    --inventory-port N [--inventory-host A]
 * </pre>
 *
 * A server prints {@code listening <port>} after the PID once it listens
 * (and, if TLS, has published its certificate to D/role.crt); start them
 * leaves first — billing, inventory, orders — each only after the one
 * before announced itself, then the gateway, whose checkout drives the
 * run. Every server answers its declared calls and exits; the gateway
 * exits after its checkout. A caller pins the certificate its TLS callee
 * published in D, never anything else: one shared D on one host; across
 * hosts the deployment copies the public certificate into the caller's D
 * (ServiceNode).
 *
 * Addresses (M4, one service per host): a server binds --bind and its
 * certificate names that address; a caller dials each callee at its
 * --callee-host, which must be the callee's --bind for TLS to verify.
 * Both default to 127.0.0.1 — the loopback run and its golden are what
 * they were — and are IPv4 literals, never names ({@link TcpHosts#ipv4}:
 * no resolver I/O in the bracket). 0.0.0.0 is refused: a wildcard bind
 * names no one address a certificate or a caller could use.
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
        Options.requireRole(NAME, role, roles());
        Service svc = Service.of(role);
        Options.requireKnown(opts, options(svc));
        // Every option is parsed before any I/O: a bad one is a usage
        // error with an empty bracket, never a half-booted service.
        Map<Service, InetSocketAddress> callees = callees(svc, opts);
        boolean server = !svc.serves.isEmpty();
        InetAddress bind = server ? address(opts, TcpHosts.BIND) : null;
        int port = server ? Options.port(opts, 0) : 0;
        try (Workdir wd = Workdir.open(opts)) {
            ServiceNode node = new ServiceNode(svc, wd, callees);
            if (server) {
                node.serve(bind, port, Microservices::announce, 0);
            } else {
                node.checkout();
            }
        }
    }

    /**
     * The options a role takes: --dir; --bind and --port if it serves; a
     * port and a host per callee.
     */
    private static Set<String> options(Service svc) {
        Set<String> known = new TreeSet<>(Set.of(Workdir.OPTION));
        if (!svc.serves.isEmpty()) {
            known.add(TcpHosts.BIND);
            known.add("port");
        }
        for (Service to : svc.downstream()) {
            known.add(to.portOption());
            known.add(to.hostOption());
        }
        return known;
    }

    /** Where each callee listens: its --callee-host (default 127.0.0.1) and port. */
    private static Map<Service, InetSocketAddress> callees(Service svc, Map<String, String> opts)
            throws UsageException {
        Map<Service, InetSocketAddress> out = new EnumMap<>(Service.class);
        for (Service to : svc.downstream()) {
            InetAddress host = address(opts, to.hostOption());
            out.put(to, new InetSocketAddress(host, Options.requiredPort(opts, to.portOption())));
        }
        return out;
    }

    /**
     * Option key as an IPv4 literal, 127.0.0.1 when absent. A wildcard is
     * a usage error: no certificate names it and no caller can dial it.
     */
    static InetAddress address(Map<String, String> opts, String key) throws UsageException {
        if (!opts.containsKey(key)) {
            return ServiceNode.LOOPBACK;
        }
        InetAddress a = TcpHosts.ipv4(opts, key);
        if (a.isAnyLocalAddress()) {
            throw new UsageException("--" + key + " " + opts.get(key)
                    + " is a wildcard; name the one address the service is reached at");
        }
        return a;
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
            Map<Service, InetSocketAddress> listening = new EnumMap<>(Service.class);
            List<Peer> servers = new ArrayList<>();
            Exception failure = null;
            try {
                for (Service s : SERVER_START_ORDER) {
                    servers.add(startServer(s, wd, listening));
                }
                new ServiceNode(Service.GATEWAY, wd, listening).checkout();
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
     * Starts server s on loopback on a peer thread and waits for its
     * port; adds its address to listening for the services started after.
     */
    private static Peer startServer(Service s, Workdir wd,
            Map<Service, InetSocketAddress> listening) throws Exception {
        CompletableFuture<Integer> bound = new CompletableFuture<>();
        Map<Service, InetSocketAddress> known = new EnumMap<>(listening);
        Peer peer = Peer.start(NAME + "-" + s.role, () -> {
            try {
                new ServiceNode(s, wd, known).serve(ServiceNode.LOOPBACK, 0, bound::complete,
                        SELF_TEST_WAIT_SECONDS);
            } catch (Exception e) {
                // A server that dies before listening must not leave the
                // self-test waiting for a port that never comes.
                bound.completeExceptionally(e);
                throw e;
            }
        });
        int port = bound.get(SELF_TEST_WAIT_SECONDS, TimeUnit.SECONDS);
        listening.put(s, new InetSocketAddress(ServiceNode.LOOPBACK, port));
        return peer;
    }
}
