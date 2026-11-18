package iormesh.workload.scenarios;

import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.channels.ReadableByteChannel;
import java.nio.channels.WritableByteChannel;
import java.nio.charset.StandardCharsets;
import java.util.Arrays;

/**
 * Exact-size exchanges over blocking NIO channels, shared by the M2
 * socket and pipe scenarios. Each payload is small enough that the
 * kernel moves it in one read(2) or write(2), so the loops below run
 * once and the declared syscall counts hold; they loop anyway so a
 * short transfer is a slower run, never a wrong one.
 */
final class Wire {
    static final byte[] PING = "ping!".getBytes(StandardCharsets.US_ASCII);
    static final byte[] PONG = "pong!".getBytes(StandardCharsets.US_ASCII);

    private Wire() {}

    static void send(WritableByteChannel ch, byte[] data) throws IOException {
        ByteBuffer buf = ByteBuffer.wrap(data);
        while (buf.hasRemaining()) {
            ch.write(buf);
        }
    }

    /** Reads exactly want.length bytes and fails unless they equal want. */
    static void expect(ReadableByteChannel ch, byte[] want) throws IOException {
        ByteBuffer buf = ByteBuffer.allocate(want.length);
        while (buf.hasRemaining()) {
            if (ch.read(buf) < 0) {
                throw new IOException("EOF after " + buf.position() + " of " + want.length
                        + " bytes");
            }
        }
        if (!Arrays.equals(want, buf.array())) {
            throw new IOException("want " + show(want) + ", got " + show(buf.array()));
        }
    }

    /**
     * Fails unless the peer's next move is EOF. A server reads to EOF
     * before closing so the client always closes first: the same
     * teardown order in every run (see TcpLoopback.serve).
     */
    static void expectEof(ReadableByteChannel ch) throws IOException {
        if (ch.read(ByteBuffer.allocate(1)) != -1) {
            throw new IOException("want EOF, got more data");
        }
    }

    private static String show(byte[] b) {
        return new String(b, StandardCharsets.US_ASCII);
    }
}
