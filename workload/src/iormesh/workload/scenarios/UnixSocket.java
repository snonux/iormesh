package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.UsageException;
import java.io.IOException;
import java.net.StandardProtocolFamily;
import java.net.UnixDomainSocketAddress;
import java.nio.channels.ServerSocketChannel;
import java.nio.channels.SocketChannel;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.Set;

/**
 * M2's unix domain socket: one stream connection on a filesystem
 * socket between two processes, 5 bytes each way — tcp-loopback's
 * exchange, minus the network (Java 16+ NIO, so JDK-only).
 *
 * <pre>
 *   --role server [--dir D]     bind D/unix-socket.sock, accept one,
 *                               read 5, write 5, wait for EOF, unlink
 *   --role client  --path P     connect to P, write 5, read 5
 * </pre>
 *
 * The server prints {@code listening <socket path>} as the stdout line
 * after the PID; the harness passes that path to the client. The path
 * must fit sun_path (107 bytes); a longer --dir fails the server with
 * exit 1. Syscalls (strace, JDK 25, 26 and 27-ea):
 *
 * <pre>
 *   server: socket(AF_UNIX, SOCK_STREAM) = L; bind(L, D/unix-socket.sock);
 *           listen(L, 1); accept(L) = A; read(A, 5); write(A, 5);
 *           read(A) = 0; close(A); close(L); unlink(D/unix-socket.sock)
 *   client: socket(AF_UNIX, SOCK_STREAM) = C; connect(C, P) = 0;
 *           write(C, 5); read(C, 5); close(C)
 * </pre>
 *
 * The socket path is the only name either end has: accept reports an
 * unnamed peer and the client never binds, so joining the two ends —
 * and naming the peer PID (SO_PEERCRED) — is the tracer's job. The
 * bind creates the socket file and the server unlinks it itself, so
 * the file's whole lifetime is inside the bracket.
 */
public final class UnixSocket implements Scenario {
    static final String NAME = "unix-socket";
    static final String SOCKET_FILE = NAME + ".sock";
    static final String PATH_OPTION = "path";

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("client", "server"));
        if (role.equals("client")) {
            Options.requireKnown(opts, Set.of(PATH_OPTION));
            client(requiredPath(opts));
            return;
        }
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            Path sock = wd.file(SOCKET_FILE);
            try (ServerSocketChannel listener = bind(sock)) {
                System.out.println("listening " + sock);
                System.out.flush();
                serve(listener, sock);
            }
        }
    }

    /** Both roles in one process: the server on a second thread. */
    @Override
    public void selfTest() throws Exception {
        try (Workdir wd = Workdir.temp()) {
            Path sock = wd.file(SOCKET_FILE);
            ServerSocketChannel listener = bind(sock);
            // Closing the listener on any server exit resets a client
            // queued in the backlog, so it never blocks.
            Peer server = Peer.start(NAME + "-server", () -> {
                try (listener) {
                    serve(listener, sock);
                }
            });
            try {
                client(sock);
            } catch (Throwable t) {
                // A client that failed before connecting leaves the server
                // blocked in accept; closing the listener wakes it (an
                // AsynchronousCloseException), so the join cannot hang.
                listener.close();
                try {
                    server.join();
                } catch (Exception e) {
                    t.addSuppressed(e);
                }
                throw t;
            }
            server.join();
        }
    }

    private static Path requiredPath(Map<String, String> opts) throws UsageException {
        String p = opts.get(PATH_OPTION);
        if (p == null || p.isEmpty()) {
            throw new UsageException("missing --" + PATH_OPTION);
        }
        return Path.of(p);
    }

    private static ServerSocketChannel bind(Path sock) throws IOException {
        ServerSocketChannel listener = ServerSocketChannel.open(StandardProtocolFamily.UNIX);
        try {
            // Backlog 1: the scenario accepts exactly one connection.
            listener.bind(UnixDomainSocketAddress.of(sock), 1);
        } catch (IOException e) {
            listener.close();
            throw e;
        }
        return listener;
    }

    /**
     * Serves one connection, then unlinks the socket file — as a
     * declared syscall on success; on failure the file is removed
     * anyway, so a temp Workdir can still be deleted.
     */
    private static void serve(ServerSocketChannel listener, Path sock) throws IOException {
        try {
            try (SocketChannel conn = listener.accept()) {
                Wire.expect(conn, Wire.PING);
                Wire.send(conn, Wire.PONG);
                Wire.expectEof(conn);
            }
            listener.close();
            Files.delete(sock);
        } catch (IOException e) {
            Files.deleteIfExists(sock);
            throw e;
        }
    }

    private static void client(Path sock) throws IOException {
        try (SocketChannel conn = SocketChannel.open(StandardProtocolFamily.UNIX)) {
            conn.connect(UnixDomainSocketAddress.of(sock));
            Wire.send(conn, Wire.PING);
            Wire.expect(conn, Wire.PONG);
        }
    }
}
