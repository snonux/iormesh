package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
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
 * M1's socket scenario: one TCP connection on 127.0.0.1, 5 bytes each
 * way, between two processes. Each side first does {@link FileWork}
 * (file "tcp-loopback-&lt;role&gt;.dat"), so both PIDs get file edges.
 *
 * <pre>
 *   --role server [--port N] [--dir D]   bind, accept one, read 5, write 5
 *   --role client  --port N  [--dir D]   connect, write 5, read 5
 * </pre>
 *
 * The server prints {@code listening <port>} on stdout once it listens:
 * the line after the PID, so the PID-first contract holds, and it names
 * the real port when {@code --port 0} (the default) let the kernel pick.
 * A harness starts (or releases) the client only after that line, so
 * the connect never races the listen. Blocking java.net sockets on
 * platform threads only; virtual threads arrive with M3's scenarios.
 *
 * Observed with strace (JDK 25): the JVM opens a dual-stack AF_INET6
 * socket, so addresses show as ::ffff:127.0.0.1 (plain 127.0.0.1 on a
 * host without IPv6) — expect.json therefore declares ports, never
 * addresses. With no timeout set, connect(2) blocks and returns 0 and
 * the server uses accept(2), not accept4(2).
 */
public final class TcpLoopback implements Scenario {
    static final String NAME = "tcp-loopback";
    static final byte[] PING = "ping!".getBytes(StandardCharsets.US_ASCII);
    static final byte[] PONG = "pong!".getBytes(StandardCharsets.US_ASCII);

    // A literal address, never a lookup: no resolver I/O in the bracket.
    private static final InetAddress LOOPBACK = loopback();

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("client", "server"));
        Options.requireKnown(opts, Set.of("port", Workdir.OPTION));
        // Parse the port before any I/O, so a bad option is a usage error
        // with an empty bracket rather than a half-run scenario.
        int port = role.equals("server") ? Options.port(opts, 0) : Options.requiredPort(opts);
        try (Workdir wd = Workdir.open(opts)) {
            FileWork.run(wd.file(dataFile(role)));
            if (role.equals("server")) {
                server(port);
            } else {
                client(port);
            }
        }
    }

    /**
     * Both roles in one process for --self-test: the server on a second
     * platform thread with a kernel-picked port, the client on this one.
     */
    @Override
    public void selfTest() throws Exception {
        try (Workdir wd = Workdir.temp(); ServerSocket listener = bind(0)) {
            Peer server = Peer.start(NAME + "-server", () -> {
                // Closing the listener on any exit resets a client still
                // queued in the backlog, so a failed server never leaves
                // the client blocked in read forever.
                try (listener) {
                    FileWork.run(wd.file(dataFile("server")));
                    serve(listener);
                }
            });
            FileWork.run(wd.file(dataFile("client")));
            client(listener.getLocalPort());
            server.join();
        }
    }

    static String dataFile(String role) {
        return NAME + "-" + role + ".dat";
    }

    private static void server(int port) throws IOException {
        try (ServerSocket listener = bind(port)) {
            System.out.println("listening " + listener.getLocalPort());
            System.out.flush();
            serve(listener);
        }
    }

    private static ServerSocket bind(int port) throws IOException {
        ServerSocket listener = new ServerSocket();
        try {
            // Backlog 1: the scenario accepts exactly one connection.
            listener.bind(new InetSocketAddress(LOOPBACK, port), 1);
        } catch (IOException e) {
            listener.close();
            throw e;
        }
        return listener;
    }

    /** Accepts one connection, reads PING, answers PONG. */
    private static void serve(ServerSocket listener) throws IOException {
        try (Socket conn = listener.accept()) {
            expect(conn.getInputStream(), PING);
            conn.getOutputStream().write(PONG);
        }
    }

    private static void client(int port) throws IOException {
        try (Socket conn = new Socket()) {
            conn.connect(new InetSocketAddress(LOOPBACK, port));
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

    private static InetAddress loopback() {
        try {
            return InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        } catch (IOException e) {
            // getByAddress only throws for a wrong-length array.
            throw new IllegalStateException(e);
        }
    }
}
