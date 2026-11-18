package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.ScenarioSkipped;
import java.io.IOException;
import java.io.InputStream;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.StandardProtocolFamily;
import java.net.URISyntaxException;
import java.nio.channels.FileChannel;
import java.nio.channels.SocketChannel;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.Set;

/**
 * M2's non-Java peer: this JVM talks to a process that is not a JVM —
 * workload/echopeer, a cgo-free Go echo server built next to
 * workload.jar by {@code mage workloadBuild} — so the tracer has to show
 * the degraded node shape: files and sockets only, every event
 * {@code attribution: none} (PLAN.md "Non-Java processes"). Solo; options
 * {@code --dir} and {@code --peer PATH} (default: echopeer next to the
 * jar). Syscalls (strace, JDK 25, 26 and 27-ea; Go 1.26):
 *
 * <pre>
 *   java: spawn echopeer -dir D (stdout pipe); read "listening P"
 *   peer: socket(AF_INET) = L; bind(L, 127.0.0.1:0); listen(L, 1);
 *         setsockopt(L, SO_RCVTIMEO); getsockname(L) = P;
 *         write(1, "listening P\n"); accept4(L) = A; read(A, 5);
 *         write(A, 5); read(A) = 0; close(A); close(L);
 *         openat(D/non-java-peer.log, O_WRONLY|O_CREAT|O_TRUNC) = F;
 *         write(F, 5); close(F)
 *   java: socket(AF_INET) = C; connect(C, 127.0.0.1:P); write(C, 5);
 *         read(C, 5); close(C); wait4(peer);
 *         openat(D/non-java-peer.log, O_RDONLY) = G; read(G, 5); close(G)
 * </pre>
 *
 * The peer brackets itself with this scenario's markers, so the
 * normalizer keeps it although it is a forked child. The Java socket is
 * AF_INET (an explicit protocol family, as in tcp-ipv4), so both ends
 * name 127.0.0.1 and the golden declares full 4-tuples.
 *
 * Without --peer and with no peer binary built, the scenario skips (exit
 * 3): a jar run on its own has nothing to talk to, which is not a
 * failure. An explicit --peer that is not an executable file fails
 * (exit 1).
 */
public final class NonJavaPeer implements Scenario {
    static final String NAME = "non-java-peer";
    static final String PEER_OPTION = "peer";
    static final String PEER_BINARY = "echopeer";
    static final String LOG = NAME + ".log";
    private static final String LISTENING = "listening ";

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION, PEER_OPTION));
        // The workdir first: a bad --dir is a run failure (exit 1) even on
        // a host where the missing peer would otherwise make it a skip.
        try (Workdir wd = Workdir.open(opts)) {
            Path peer = peerBinary(opts);
            Path log = wd.file(LOG);
            try {
                exchange(peer, log);
                verifyLog(log);
            } finally {
                Files.deleteIfExists(log);
            }
        }
    }

    /** --peer, or echopeer next to the jar (skip when that is missing). */
    private static Path peerBinary(Map<String, String> opts) throws Exception {
        String given = opts.get(PEER_OPTION);
        if (given != null) {
            Path p = Path.of(given);
            if (!Files.isRegularFile(p) || !Files.isExecutable(p)) {
                throw new IOException("--peer is not an executable file: " + p);
            }
            return p;
        }
        Path p = besideCode().resolve(PEER_BINARY);
        if (!Files.isRegularFile(p) || !Files.isExecutable(p)) {
            throw new ScenarioSkipped("no non-JVM peer at " + p
                    + " (mage workloadBuild builds it; or pass --peer)", null);
        }
        return p;
    }

    /**
     * The directory holding workload.jar, where mage builds echopeer. Run
     * from a class directory instead, the code source is that directory
     * and this is its parent (workload/ in the repo), which holds no
     * echopeer binary — the scenario then skips unless --peer is given.
     */
    private static Path besideCode() throws URISyntaxException {
        Path code = Path.of(NonJavaPeer.class.getProtectionDomain().getCodeSource()
                .getLocation().toURI());
        return code.toAbsolutePath().getParent();
    }

    /** Starts the peer, talks to it once and waits for its clean exit. */
    private static void exchange(Path peer, Path log) throws Exception {
        String dir = log.toAbsolutePath().getParent().toString();
        Process p = new ProcessBuilder(peer.toString(), "-dir", dir)
                .redirectError(ProcessBuilder.Redirect.INHERIT)
                .start();
        try {
            talk(awaitListening(p));
            int code = p.waitFor();
            if (code != 0) {
                throw new IOException("echopeer exited " + code);
            }
        } finally {
            // Only on failure does this kill anything: a reaped peer
            // makes it a no-op without a syscall (see Signals.signal).
            p.destroyForcibly();
            p.waitFor();
        }
    }

    /**
     * Reads the peer's "listening P" line, then closes the pipe at once:
     * the peer writes nothing more there, and a closed read end leaves
     * the JDK's reaper nothing to drain on its own thread (see PipeIpc).
     */
    private static int awaitListening(Process p) throws IOException {
        String line;
        try (InputStream in = p.getInputStream()) {
            line = readLine(in);
        }
        if (!line.startsWith(LISTENING)) {
            throw new IOException("echopeer said: " + line);
        }
        try {
            return Integer.parseInt(line.substring(LISTENING.length()));
        } catch (NumberFormatException e) {
            throw new IOException("echopeer said: " + line, e);
        }
    }

    /** One line, byte by byte: the pipe holds nothing after it anyway. */
    private static String readLine(InputStream in) throws IOException {
        StringBuilder sb = new StringBuilder();
        for (int b = in.read(); b != '\n'; b = in.read()) {
            if (b < 0) {
                throw new IOException("echopeer exited before listening: " + sb);
            }
            sb.append((char) b);
        }
        return sb.toString();
    }

    private static void talk(int port) throws IOException {
        InetAddress lo = InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        try (SocketChannel conn = SocketChannel.open(StandardProtocolFamily.INET)) {
            conn.connect(new InetSocketAddress(lo, port));
            Wire.send(conn, Wire.PING);
            Wire.expect(conn, Wire.PING);
        }
    }

    /** The peer logged what it echoed: its file work, checked from here. */
    private static void verifyLog(Path log) throws IOException {
        try (FileChannel ch = FileChannel.open(log)) {
            Wire.expect(ch, Wire.PING);
        }
    }
}
