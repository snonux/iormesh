package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.net.DatagramPacket;
import java.net.DatagramSocket;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.SocketAddress;
import java.net.StandardProtocolFamily;
import java.nio.ByteBuffer;
import java.nio.channels.DatagramChannel;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.Map;
import java.util.Set;

/**
 * M2's UDP: one datagram each way on 127.0.0.1 between two processes,
 * the per-packet peer exchange "What we trace" names for UDP.
 *
 * <pre>
 *   --role server [--port N]   bind, receive one datagram (5 bytes),
 *                              send 5 back to its sender
 *   --role client  --port N    send 5, receive 5
 * </pre>
 *
 * Like tcp-loopback, the server prints {@code listening <port>} after
 * the PID ({@code --port 0}, the default, lets the kernel pick). The
 * channels are opened as AF_INET (StandardProtocolFamily.INET), so the
 * addresses are plain 127.0.0.1, and are never connected: every
 * datagram names its peer, which is what the tracer must record.
 * Syscalls (strace, JDK 25, 26 and 27-ea):
 *
 * <pre>
 *   server: socket(AF_INET, SOCK_DGRAM) = S; bind(S, 127.0.0.1:port);
 *           recvfrom(S, 5) from client; sendto(S, 5) to client; close(S)
 *   client: socket(AF_INET, SOCK_DGRAM) = C; bind(C, 0.0.0.0:0) (the
 *           JDK binds an unbound channel itself before its first send);
 *           sendto(C, 5) to server; recvfrom(C, 5) from server; close(C)
 * </pre>
 *
 * The client is bound to the wildcard address, so only its port is
 * declared: its local address is 0.0.0.0, not 127.0.0.1.
 *
 * Blocking receives with no timeout, on purpose: a timeout turns the
 * JDK's receive into poll(2) plus recvfrom(2). Loopback does not drop
 * these datagrams; a harness bounds a hung run, as for tcp-loopback.
 * Only --self-test, which no tracer watches and no harness bounds, sets
 * one, so a role whose peer failed fails too instead of hanging.
 * There is no run-failure check for a client on a closed port: an
 * unconnected UDP socket never sees the ICMP error, so it just waits.
 */
public final class UdpLoopback implements Scenario {
    static final String NAME = "udp-loopback";
    // The self-test's receive timeout; 0 (the traced runs) blocks forever.
    static final int SELF_TEST_TIMEOUT_MILLIS = 30_000;

    private static final InetAddress LOOPBACK = loopback();

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("client", "server"));
        Options.requireKnown(opts, Set.of("port"));
        // Parse the port before any I/O: a bad option is a usage error.
        int port = role.equals("server") ? Options.port(opts, 0) : Options.requiredPort(opts);
        if (role.equals("client")) {
            client(port, 0);
            return;
        }
        try (DatagramChannel ch = bind(port)) {
            System.out.println("listening " + localPort(ch));
            System.out.flush();
            serve(ch, 0);
        }
    }

    /** Both roles in one process: the server on a second thread. */
    @Override
    public void selfTest() throws Exception {
        try (DatagramChannel ch = bind(0)) {
            Peer server = Peer.start(NAME + "-server",
                    () -> serve(ch, SELF_TEST_TIMEOUT_MILLIS));
            try {
                client(localPort(ch), SELF_TEST_TIMEOUT_MILLIS);
            } finally {
                // Bounded either way: the server's receive times out too.
                server.join();
            }
        }
    }

    private static DatagramChannel bind(int port) throws IOException {
        DatagramChannel ch = DatagramChannel.open(StandardProtocolFamily.INET);
        try {
            ch.bind(new InetSocketAddress(LOOPBACK, port));
        } catch (IOException e) {
            ch.close();
            throw e;
        }
        return ch;
    }

    /** Answers the first datagram with PONG, to whoever sent it. */
    private static void serve(DatagramChannel ch, int timeoutMillis) throws IOException {
        SocketAddress from = receive(ch, Wire.PING, timeoutMillis);
        ch.send(ByteBuffer.wrap(Wire.PONG), from);
    }

    private static void client(int port, int timeoutMillis) throws IOException {
        try (DatagramChannel ch = DatagramChannel.open(StandardProtocolFamily.INET)) {
            InetSocketAddress server = new InetSocketAddress(LOOPBACK, port);
            ch.send(ByteBuffer.wrap(Wire.PING), server);
            SocketAddress from = receive(ch, Wire.PONG, timeoutMillis);
            if (!server.equals(from)) {
                throw new IOException("pong from " + from + ", want " + server);
            }
        }
    }

    /**
     * Receives one datagram and fails unless it is exactly want. The
     * buffer is one byte larger, so an oversized datagram is caught
     * instead of silently truncated to a match. A non-zero timeout goes
     * through the socket adaptor, the channel's only timed receive.
     */
    private static SocketAddress receive(DatagramChannel ch, byte[] want, int timeoutMillis)
            throws IOException {
        byte[] buf = new byte[want.length + 1];
        SocketAddress from;
        int n;
        if (timeoutMillis == 0) {
            ByteBuffer bb = ByteBuffer.wrap(buf);
            from = ch.receive(bb);
            n = bb.position();
        } else {
            DatagramSocket s = ch.socket();
            s.setSoTimeout(timeoutMillis);
            DatagramPacket pkt = new DatagramPacket(buf, buf.length);
            s.receive(pkt);
            from = pkt.getSocketAddress();
            n = pkt.getLength();
        }
        byte[] got = Arrays.copyOf(buf, n);
        if (!Arrays.equals(want, got)) {
            throw new IOException("datagram of " + got.length + " bytes is not "
                    + new String(want, StandardCharsets.US_ASCII));
        }
        return from;
    }

    private static int localPort(DatagramChannel ch) throws IOException {
        return ((InetSocketAddress) ch.getLocalAddress()).getPort();
    }

    private static InetAddress loopback() {
        try {
            // A literal address, never a lookup: no resolver I/O.
            return InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        } catch (IOException e) {
            // getByAddress only throws for a wrong-length array.
            throw new IllegalStateException(e);
        }
    }
}
