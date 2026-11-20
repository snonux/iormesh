package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.UsageException;
import java.io.IOException;
import java.io.InputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.Map;
import java.util.Set;

/**
 * M4's cross-host pair (PLAN.md scenario catalogue, M4): M0's client and
 * server — file work, then one TCP connection, 5 bytes each way, the
 * client closing first — on two hosts, each traced by its own tracer as
 * its own project. Registered twice, because the two M4 rows differ only
 * in the network between the hosts and so in what each end observes:
 *
 * <ul>
 *   <li>{@code tcp-two-host}: the client dials the server's own address;
 *       both ends see one 4-tuple, mirrored.</li>
 *   <li>{@code tcp-nat}: the client dials a published address that a
 *       DNAT hop (docker's {@code -p PUB:PORT}) rewrites to the server's;
 *       the two ends see different 4-tuples, and only the TCP ISN pair
 *       (decision #26) joins them.</li>
 * </ul>
 *
 * <pre>
 *   --role server --bind A [--port N] [--dir D]   bind A:N, accept one,
 *                                                 read 5, write 5, wait
 *                                                 for the client's EOF
 *   --role client --host A  --port N  [--dir D]   connect A:N, write 5,
 *                                                 read 5
 * </pre>
 *
 * Addresses are IPv4 literals, never names: a lookup would put resolver
 * I/O into the bracket, and the hosts of a test rig have no resolver.
 * The server prints {@code listening <port>} once it listens, as
 * tcp-loopback's does, so a harness releases the client only then. The
 * syscalls are tcp-loopback's (java.net sockets are dual-stack AF_INET6,
 * so the trace shows ::ffff:-mapped addresses, which the normalizer
 * unmaps); the data files are {@code <name>-<role>.dat}.
 */
public final class TcpHosts implements Scenario {
    static final String HOST = "host";
    static final String BIND = "bind";

    private static final byte[] PING = "ping!".getBytes(StandardCharsets.US_ASCII);
    private static final byte[] PONG = "pong!".getBytes(StandardCharsets.US_ASCII);

    private final String name;

    private TcpHosts(String name) {
        this.name = name;
    }

    /** tcp-two-host: client and server on two hosts, no NAT between. */
    public static TcpHosts twoHost() {
        return new TcpHosts("tcp-two-host");
    }

    /** tcp-nat: client and server with a DNAT hop between them. */
    public static TcpHosts nat() {
        return new TcpHosts("tcp-nat");
    }

    @Override
    public String name() {
        return name;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(name, role, Set.of("client", "server"));
        boolean server = role.equals("server");
        Options.requireKnown(opts, Set.of(server ? BIND : HOST, "port", Workdir.OPTION));
        // Parse everything before any I/O, so a bad option is a usage
        // error with an empty bracket rather than a half-run scenario.
        InetAddress addr = ipv4(opts, server ? BIND : HOST);
        int port = server ? Options.port(opts, 0) : Options.requiredPort(opts);
        try (Workdir wd = Workdir.open(opts)) {
            FileWork.run(wd.file(dataFile(role)));
            if (server) {
                server(addr, port);
            } else {
                client(addr, port);
            }
        }
    }

    /**
     * Both roles in one process on 127.0.0.1 for --self-test: the server
     * on a second platform thread with a kernel-picked port. One host is
     * all a self-test has; the second host is the harness's business.
     */
    @Override
    public void selfTest() throws Exception {
        InetAddress lo = InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        try (Workdir wd = Workdir.temp(); ServerSocket listener = bind(lo, 0)) {
            Peer server = Peer.start(name + "-server", () -> {
                // Closing the listener on any exit resets a client still
                // queued in the backlog, so a failed server never leaves
                // the client blocked in read forever.
                try (listener) {
                    FileWork.run(wd.file(dataFile("server")));
                    serve(listener);
                }
            });
            FileWork.run(wd.file(dataFile("client")));
            client(lo, listener.getLocalPort());
            server.join();
        }
    }

    private String dataFile(String role) {
        return name + "-" + role + ".dat";
    }

    /**
     * The --bind or --host option (named by key) as an IPv4 address,
     * parsed by hand: the
     * JDK's InetAddress.getByName would resolve anything that is not a
     * literal, and InetAddress.ofLiteral is JDK 22+ (the workload targets
     * 21). Four decimal parts in 0..255, nothing else.
     */
    static InetAddress ipv4(Map<String, String> opts, String key) throws UsageException {
        String v = opts.get(key);
        if (v == null) {
            throw new UsageException("missing --" + key);
        }
        String[] parts = v.split("\\.", -1);
        if (parts.length != 4) {
            throw new UsageException("--" + key + " is no IPv4 literal: " + v);
        }
        byte[] b = new byte[4];
        for (int i = 0; i < 4; i++) {
            if (!parts[i].matches("[0-9]{1,3}") || Integer.parseInt(parts[i]) > 255) {
                throw new UsageException("--" + key + " is no IPv4 literal: " + v);
            }
            b[i] = (byte) Integer.parseInt(parts[i]);
        }
        try {
            return InetAddress.getByAddress(b);
        } catch (IOException e) {
            // getByAddress only throws for a wrong-length array.
            throw new IllegalStateException(e);
        }
    }

    // No accept timeout on purpose: a standalone server waits for its
    // client as long as it takes; the harness kills it on failure.
    private static void server(InetAddress addr, int port) throws IOException {
        try (ServerSocket listener = bind(addr, port)) {
            System.out.println("listening " + listener.getLocalPort());
            System.out.flush();
            serve(listener);
        }
    }

    private static ServerSocket bind(InetAddress addr, int port) throws IOException {
        ServerSocket listener = new ServerSocket();
        try {
            // Backlog 1: the scenario accepts exactly one connection.
            listener.bind(new InetSocketAddress(addr, port), 1);
        } catch (IOException e) {
            listener.close();
            throw e;
        }
        return listener;
    }

    /**
     * Accepts one connection, reads PING, answers PONG, then waits for the
     * client's EOF before closing — the client always closes first, so
     * the teardown's TCP states are the same in every run (tcp-loopback's
     * reasoning).
     */
    private static void serve(ServerSocket listener) throws IOException {
        try (Socket conn = listener.accept()) {
            InputStream in = conn.getInputStream();
            expect(in, PING);
            conn.getOutputStream().write(PONG);
            if (in.read() != -1) {
                throw new IOException("want EOF after pong, got more data");
            }
        }
    }

    private static void client(InetAddress addr, int port) throws IOException {
        try (Socket conn = new Socket()) {
            conn.connect(new InetSocketAddress(addr, port));
            conn.getOutputStream().write(PING);
            expect(conn.getInputStream(), PONG);
        }
    }

    /** Reads exactly want.length bytes and fails unless they equal want. */
    private static void expect(InputStream in, byte[] want) throws IOException {
        byte[] got = in.readNBytes(want.length);
        if (!Arrays.equals(want, got)) {
            throw new IOException("want " + new String(want, StandardCharsets.US_ASCII)
                    + ", got " + new String(got, StandardCharsets.US_ASCII));
        }
    }
}
