package iormesh.s1.bridge;

import java.io.RandomAccessFile;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.VarHandle;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.MappedByteBuffer;
import java.nio.channels.FileChannel;

/**
 * Throwaway (S1, task xa4): decision #27's memory-mapped spool. One file
 * in a tracer-owned directory, mapped MAP_SHARED by the agent (writer)
 * and the tracer (reader, spoolread.py here). After the map, writing a
 * record is plain memory stores — no syscall — so the agent's transport
 * adds nothing to the target's syscall story.
 *
 * Layout (little endian):
 * <pre>
 *   0    magic "IORMSPL1"          8   capacity (data bytes, 2^k)
 *   16   writer pid
 *   64   head  — bytes reserved by writers (CAS), own cache line
 *   128  tail  — bytes consumed by the reader, written by the reader
 *   192  dropped records          200  dropped bytes
 *   4096 data[capacity]
 * </pre>
 * A record starts with one 8-byte word {@code len | type << 32} (len
 * includes the word, multiple of 8). Writers reserve with a CAS on head,
 * fill the body, then publish the word with a release store; the reader
 * sees 0 until then. The reader zeroes what it consumed before moving
 * tail, so the next lap starts from zeros. A record that would straddle
 * the end is preceded by a PAD record filling the rest of the lap.
 *
 * Back-pressure: never block the target. When the reader is a full
 * capacity behind, the record is dropped and counted in the header — the
 * tracer turns the counters into the trace's loss accounting.
 */
final class Ring {
    static final int PAD = 0;
    static final int BRACKET = 1;
    static final int STACK = 2;
    static final int THREAD = 3;

    private static final int DATA = 4096;
    // VarHandle coordinates are signature-polymorphic: they must be
    // exactly (ByteBuffer, int), so offsets are ints and buf a ByteBuffer.
    private static final int HEAD = 64;
    private static final int TAIL = 128;
    private static final int DROPPED = 192;
    private static final int DROPPED_BYTES = 200;
    private static final VarHandle LONGS = MethodHandles.byteBufferViewVarHandle(
            long[].class, ByteOrder.LITTLE_ENDIAN);

    private final ByteBuffer buf;
    private final long capacity;

    private Ring(ByteBuffer buf, long capacity) {
        this.buf = buf;
        this.capacity = capacity;
    }

    /** Creates (truncates) and maps the spool file; startup only. */
    static Ring create(String path, long capacity) throws Exception {
        if (Long.bitCount(capacity) != 1) {
            throw new IllegalArgumentException("capacity must be a power of two");
        }
        try (RandomAccessFile f = new RandomAccessFile(path, "rw")) {
            f.setLength(0);
            f.setLength(DATA + capacity);
            MappedByteBuffer b = f.getChannel().map(FileChannel.MapMode.READ_WRITE, 0,
                    DATA + capacity);
            b.order(ByteOrder.LITTLE_ENDIAN);
            b.putLong(8, capacity);
            b.putLong(16, ProcessHandle.current().pid());
            // Magic last: a reader that sees it sees a complete header.
            LONGS.setRelease((ByteBuffer) b, 0, 0x314C50534D524F49L); // "IORMSPL1"
            return new Ring(b, capacity);
        }
    }

    /**
     * Reserves len bytes (multiple of 8) and returns the record's buffer
     * offset, or -1 after counting a drop when the reader is too far behind.
     */
    long reserve(int len) {
        while (true) {
            long head = (long) LONGS.getVolatile(buf, HEAD);
            long tail = (long) LONGS.getAcquire(buf, TAIL);
            long pos = head & (capacity - 1);
            long pad = pos + len > capacity ? capacity - pos : 0;
            if (head + pad + len - tail > capacity) {
                LONGS.getAndAdd(buf, DROPPED, 1L);
                LONGS.getAndAdd(buf, DROPPED_BYTES, (long) len);
                return -1;
            }
            if (LONGS.compareAndSet(buf, HEAD, head, head + pad + len)) {
                if (pad > 0) {
                    publish(DATA + (int) pos, (int) pad, PAD);
                    return DATA;
                }
                return DATA + (int) pos;
            }
        }
    }

    /** Makes a filled record visible to the reader (release store). */
    void publish(long off, int len, int type) {
        LONGS.setRelease(buf, (int) off, (len & 0xffffffffL) | ((long) type << 32));
    }

    void putBytes(long off, byte[] b, int len) {
        buf.put((int) off, b, 0, len);
    }
}
