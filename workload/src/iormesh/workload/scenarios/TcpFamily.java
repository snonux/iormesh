package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.ScenarioSkipped;
import java.io.IOException;
import java.net.BindException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.SocketAddress;
import java.net.SocketException;
import java.net.StandardProtocolFamily;
import java.nio.channels.ServerSocketChannel;
import java.nio.channels.SocketChannel;
import java.util.Map;
import java.util.Set;

/**
 * M2's address families: one TCP connection inside one process, over a
 * socket of exactly one family — {@code tcp-ipv4} on 127.0.0.1 with an
 * AF_INET socket, {@code tcp-ipv6} on ::1 with an AF_INET6 socket. The
 * NIO channels are opened with an explicit ProtocolFamily for that:
 * java.net sockets (tcp-loopback) are always dual-stack AF_INET6, so
 * they never produce an AF_INET socket on a host with IPv6, and the
 * tracer's address decoding must be proven for both sockaddr shapes.
 * Solo, no options. Syscalls (strace, JDK 25, 26 and 27-ea; JDK 21
 * should match — these NIO paths predate it — but was not run):
 *
 * <pre>
 *   main:   socket(AF_INET[6], SOCK_STREAM) = L; bind(L, lo:0); listen(L, 1)
 *   server: accept(L) = A; read(A, 5); write(A, 5); read(A) = 0; close(A)
 *   main:   socket(AF_INET[6], SOCK_STREAM) = C; connect(C, lo:port) = 0;
 *           write(C, 5); read(C, 5); close(C)
 *   server: close(L)
 * </pre>
 *
 * The first NIO use also makes a socketpair (the JDK's pre-close fd)
 * and five throwaway AF_INET6 probe sockets (extended socket options),
 * all inside the bracket and none declared.
 *
 * The server is a second platform thread (see Peer), so its events
 * carry their own TID; the listener is opened on main. A host without
 * an IPv6 loopback (no AF_INET6, ::1 not configured, or
 * -Djava.net.preferIPv4Stack=true) skips tcp-ipv6 with exit code 3 —
 * a skip, not a pass: its golden is never checked there.
 */
public final class TcpFamily implements Scenario {
    private final String name;
    private final StandardProtocolFamily family;
    private final InetAddress loopback;

    private TcpFamily(String name, StandardProtocolFamily family, InetAddress loopback) {
        this.name = name;
        this.family = family;
        this.loopback = loopback;
    }

    /** tcp-ipv4: AF_INET on 127.0.0.1. */
    public static TcpFamily ipv4() {
        return new TcpFamily("tcp-ipv4", StandardProtocolFamily.INET,
                literal(new byte[] {127, 0, 0, 1}));
    }

    /** tcp-ipv6: AF_INET6 on ::1. */
    public static TcpFamily ipv6() {
        byte[] one = new byte[16];
        one[15] = 1;
        return new TcpFamily("tcp-ipv6", StandardProtocolFamily.INET6, literal(one));
    }

    @Override
    public String name() {
        return name;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(name, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of());
        try (ServerSocketChannel listener = listen()) {
            SocketAddress addr = listener.getLocalAddress();
            Peer server = Peer.start(name + "-server", () -> {
                // Closing the listener on any server exit resets a
                // client queued in the backlog, so it never blocks.
                try (listener) {
                    serve(listener);
                }
            });
            client(addr);
            server.join();
        }
    }

    /**
     * Binds the loopback of this family on a kernel-picked port. On IPv6
     * only the failures that mean "no usable ::1" skip (see noIpv6);
     * anything else, and every failure on IPv4, is a real failure.
     */
    private ServerSocketChannel listen() throws IOException, ScenarioSkipped {
        try {
            ServerSocketChannel listener = ServerSocketChannel.open(family);
            try {
                // Backlog 1: the scenario accepts exactly one connection.
                listener.bind(new InetSocketAddress(loopback, 0), 1);
            } catch (IOException e) {
                listener.close();
                throw e;
            }
            return listener;
        } catch (UnsupportedOperationException | SocketException e) {
            if (family != StandardProtocolFamily.INET6 || !noIpv6(e)) {
                throw e;
            }
            throw new ScenarioSkipped("no IPv6 loopback (::1): " + e, e);
        }
    }

    /**
     * Whether e says the host has no IPv6 loopback: the JDK runs
     * IPv4-only (UnsupportedOperationException), ::1 is not configured
     * (bind's EADDRNOTAVAIL) or the kernel lacks AF_INET6 (socket's
     * EAFNOSUPPORT). Anything else — EMFILE, say — must not hide as a
     * skip. The JDK passes errno on only as its strerror text, so that
     * is what is matched; a translated message fails instead of skipping,
     * the safe side.
     */
    private static boolean noIpv6(Exception e) {
        if (e instanceof UnsupportedOperationException) {
            return true;
        }
        String msg = String.valueOf(e.getMessage());
        if (e instanceof BindException) {
            return msg.contains("Cannot assign requested address");
        }
        return msg.contains("Address family not supported");
    }

    private static void serve(ServerSocketChannel listener) throws IOException {
        try (SocketChannel conn = listener.accept()) {
            Wire.expect(conn, Wire.PING);
            Wire.send(conn, Wire.PONG);
            Wire.expectEof(conn);
        }
    }

    private void client(SocketAddress addr) throws IOException {
        try (SocketChannel conn = SocketChannel.open(family)) {
            conn.connect(addr);
            Wire.send(conn, Wire.PING);
            Wire.expect(conn, Wire.PONG);
        }
    }

    private static InetAddress literal(byte[] addr) {
        try {
            // A literal address, never a lookup: no resolver I/O.
            return InetAddress.getByAddress(addr);
        } catch (IOException e) {
            // getByAddress only throws for a wrong-length array.
            throw new IllegalStateException(e);
        }
    }
}
