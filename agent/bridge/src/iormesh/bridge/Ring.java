package iormesh.bridge;

import java.io.IOException;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.VarHandle;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.channels.FileChannel;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.LinkOption;
import java.nio.file.OpenOption;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.nio.file.attribute.PosixFilePermissions;
import java.util.Set;

/**
 * The agent → tracer transport (decision #27): one memory-mapped ring
 * per JVM, a file {@code <spool dir>/<pid>.ring} in the tracer-owned
 * spool directory, mapped shared by the agent (writers: every target
 * thread that closes a bracket, plus the watermark thread) and the
 * tracer (one reader, internal/agentspool). After {@link #create} a
 * record is plain memory stores and one CAS — no syscall, no allocation
 * for BRACKET and WATERMARK — so the transport adds nothing to the
 * target's syscall story.
 *
 * Layout, little endian (internal/agentspool mirrors it; bump VERSION on
 * any change):
 * <pre>
 *   0    magic "IORMRING", stored last with release: the header is complete
 *   8    version u32           12  header size u32 (4096)
 *   16   capacity u64 (data bytes, a power of two)
 *   24   writer pid u64        32  CLOCK_MONOTONIC ns at create
 *   64   head — bytes reserved by writers (CAS), its own cache line
 *   128  tail — bytes consumed by the reader, written by the reader only
 *   192  dropped records per type, u64[8] (index = record type)
 *   256  dropped bytes        264  abandoned records (fill threw)
 *   4096 data[capacity]
 * </pre>
 * A record is one 8-byte word {@code len | type << 32} (len counts the
 * word, a multiple of 8) and its body. Writers reserve with a CAS on
 * head, fill the body, then publish the word with a release store; the
 * reader sees 0 until then, copies the record, zeroes it and moves tail
 * with a release store, so every lap starts from zeros. A record that
 * would straddle the end of the data area is preceded by a PAD filling
 * the rest of the lap, so records are always contiguous.
 *
 * Concurrency: one shared ring with a CAS on head, not one ring per
 * thread. Per-thread rings would need a file or a slot table per thread
 * (thread count unknown at setup, so either more syscalls on target
 * threads or a fixed cap), and the reader would have to merge them to
 * keep the WATERMARK promise. The CAS costs one contended cache line;
 * the bracket's dominant cost is the stack walk (S1: ≈8 µs vs ≈0.3 µs),
 * so contention on head is not what limits the agent.
 *
 * Back-pressure: never block the target. When the reader is a full
 * capacity behind, the record is dropped and counted in the header by
 * type; the tracer turns the counters into the trace's loss accounting.
 */
public final class Ring {
    static final int PAD = 0;
    static final int BRACKET = 1;
    static final int STACK = 2;
    static final int THREAD = 3;
    static final int WATERMARK = 4;
    static final int HOOK = 5;
    static final int HTTP = 6;
    static final int DNS = 7;
    /**
     * Format version; the reader rejects any other. 2 (task 2b4): HOOK
     * records, and THREAD's reserved word carries flags (VIRTUAL). 3
     * (task 3b4): HTTP records. 4 (task 4b4): OTEL ids — a BRACKET of
     * BRACKET_OTEL_LEN, and three more words in HTTP's fixed part. 3 was
     * never released, but a bump costs nothing and a mismatched agent
     * and tracer then fail loudly instead of misreading HTTP records.
     * 5 (task wd4): HTTP's fixed part gains the request's start stamp
     * and its body sizes (latency and sizes, PLAN.md M3), and DNS
     * records (decision #8) are new. 6 (task wd4, review round 2): the
     * HTTP flags HTTP_ASYNC_MARK and HTTP_NET_HTTP.
     */
    static final int VERSION = 6;
    /** THREAD flag: the Java thread is a virtual thread (JDK 21+). */
    static final int VIRTUAL = 1;
    static final long MAGIC = 0x474e49524d524f49L; // "IORMRING", little endian
    static final int DATA = 4096;
    static final int HEAD = 64;
    static final int TAIL = 128;
    static final int DROPPED = 192;
    static final int DROPPED_BYTES = 256;
    static final int ABANDONED = 264;
    static final long MIN_CAPACITY = 1L << 16;
    static final long MAX_CAPACITY = 1L << 30;
    /** BRACKET body: hook, ktid, enter, exit, stack, thread. */
    static final int BRACKET_LEN = 8 + 40;
    /**
     * A BRACKET whose enter ran inside an OTEL span (Otel) appends the
     * span's trace id (upper, lower half) and span id: three words more,
     * only then, so the brackets of an app without OTEL cost what they
     * did.
     */
    static final int BRACKET_OTEL_LEN = BRACKET_LEN + 24;
    static final int WATERMARK_LEN = 8 + 8;
    /**
     * HTTP's fixed part: word, mono, stack, thread, ktid, status, n, nreq,
     * flags, exchanges, then the OTEL trace id halves and span id (all
     * zero: no span), then the request's start stamp (0: unknown) and its
     * request and response body sizes (-1: unknown). Fixed rather than
     * optional as in BRACKET: HTTP records are few, and the strings start
     * at one offset.
     */
    static final int HTTP_HEAD = 8 + 96;
    /**
     * The request's numbers a probe fills (Hooks.http's long[] meta), by
     * index: status, request-header count, flags (HTTP_SERVER), exchange
     * count, start stamp (System.nanoTime(), 0 unknown), request and
     * response body bytes (-1 unknown). META is the array's length.
     */
    static final int M_STATUS = 0;
    static final int M_NREQ = 1;
    static final int M_FLAGS = 2;
    static final int M_EXCHANGES = 3;
    static final int M_START = 4;
    static final int M_REQ_BYTES = 5;
    static final int M_RESP_BYTES = 6;
    static final int META = 7;
    /**
     * DNS's fixed part (task wd4): word, mono, stack, thread, ktid, n, then
     * the OTEL trace id halves and span id (zero: no span); then n strings,
     * the host name and the addresses it resolved to.
     */
    static final int DNS_HEAD = 8 + 56;
    /** A DNS record keeps at most this many addresses. */
    static final int MAX_DNS_ADDRS = 32;
    /** HTTP flag: the request was answered here (a server's), not sent. */
    static final int HTTP_SERVER = 1;
    /**
     * HTTP flag (task wd4 review): the record's stamp does not follow its
     * connection's request order (a sendAsync completion): no ordinal.
     */
    static final int HTTP_UNORDERED = 2;
    /**
     * HTTP flag (task wd4 review round 2): not a request but a marker,
     * written at the entry of HttpClient.sendAsync before the request can
     * reach any connection: from its stamp on, the tracer numbers no
     * java.net.http client end of this JVM. A pooled HTTP/1.1 connection
     * goes back to the pool when an async response's body is read, before
     * its (delayed) record, so another request's record on it can arrive
     * first and take the async one's ordinal. Its strings are empty.
     */
    static final int HTTP_ASYNC_MARK = 4;
    /** HTTP flag: the request went through java.net.http's HttpClient. */
    static final int HTTP_NET_HTTP = 8;
    /** HTTP's leading strings: method, scheme, host, path, version, local, remote. */
    static final int HTTP_FIXED = 7;
    /** Bounds a STACK or THREAD record, so one never needs a large share of the ring. */
    static final int MAX_RECORD = 16 * 1024;
    /** Each frame or thread name is cut to this many chars before encoding. */
    static final int MAX_STRING = 1024;

    // VarHandle coordinates are signature-polymorphic: they must be exactly
    // (ByteBuffer, int), so offsets are ints and buf a ByteBuffer.
    private static final VarHandle LONGS = MethodHandles.byteBufferViewVarHandle(
            long[].class, ByteOrder.LITTLE_ENDIAN);

    private final ByteBuffer buf;
    private final long capacity;
    private final Path path;

    private Ring(ByteBuffer buf, long capacity, Path path) {
        this.buf = buf;
        this.capacity = capacity;
        this.path = path;
    }

    /**
     * Creates and maps {@code dir/<pid>.ring}; install time only. A
     * stale file of a reused pid is deleted and a new one created, never
     * truncated in place: a tracer may still map the old inode, and
     * shrinking it would SIGBUS that reader. The directory is the
     * tracer's: the agent never creates it, so a missing directory means
     * no tracer is listening and fails the install. These are the agent's
     * own syscalls, which the tracer recognises by the spool path.
     */
    static Ring create(Path dir, long capacity) throws IOException {
        if (Long.bitCount(capacity) != 1 || capacity < MIN_CAPACITY || capacity > MAX_CAPACITY) {
            throw new IllegalArgumentException("ring size must be a power of two in ["
                    + MIN_CAPACITY + ", " + MAX_CAPACITY + "], got " + capacity);
        }
        long pid = ProcessHandle.current().pid();
        Path path = dir.resolve(pid + ".ring");
        Files.deleteIfExists(path);
        // CREATE_NEW and NOFOLLOW_LINKS: the spool is writable by every
        // user, so a file or symlink someone placed under our name after
        // the delete fails the install instead of being written through;
        // 0600, so no other user can read or truncate the ring. Mapping
        // past the end grows the file to its full size.
        try (FileChannel f = FileChannel.open(path, Set.<OpenOption>of(StandardOpenOption.CREATE_NEW,
                StandardOpenOption.READ, StandardOpenOption.WRITE, LinkOption.NOFOLLOW_LINKS),
                PosixFilePermissions.asFileAttribute(PosixFilePermissions.fromString("rw-------")))) {
            ByteBuffer b = f.map(FileChannel.MapMode.READ_WRITE, 0, DATA + capacity);
            b.order(ByteOrder.LITTLE_ENDIAN);
            b.putInt(8, VERSION);
            b.putInt(12, DATA);
            b.putLong(16, capacity);
            b.putLong(24, pid);
            b.putLong(32, System.nanoTime());
            // Magic last: a reader that sees it sees a complete header.
            LONGS.setRelease(b, 0, MAGIC);
            return new Ring(b, capacity, path);
        }
    }

    /** The ring file, for the debug dump and for discard. */
    Path path() {
        return path;
    }

    /**
     * Deletes the ring file after a failed install, so the tracer does
     * not wait on a ring nobody writes. The mapping stays valid.
     */
    void discard() throws IOException {
        Files.deleteIfExists(path);
    }

    /** Writes a BRACKET outside any OTEL span (tests, RingStress). */
    boolean bracket(int hook, int ktid, long enter, long exit, long stack, long thread) {
        return bracket(hook, ktid, enter, exit, stack, thread, 0, 0, 0);
    }

    /**
     * Writes a BRACKET; false if it was dropped. No allocation. With a
     * span (span id non-zero; Otel never hands over half an id) it is
     * the BRACKET_OTEL_LEN form carrying the ids.
     */
    boolean bracket(int hook, int ktid, long enter, long exit, long stack, long thread,
                    long traceHi, long traceLo, long span) {
        int len = span != 0 ? BRACKET_OTEL_LEN : BRACKET_LEN;
        int off = reserve(len, BRACKET);
        if (off < 0) {
            return false;
        }
        try {
            buf.putInt(off + 8, hook);
            buf.putInt(off + 12, ktid);
            buf.putLong(off + 16, enter);
            buf.putLong(off + 24, exit);
            buf.putLong(off + 32, stack);
            buf.putLong(off + 40, thread);
            if (span != 0) {
                buf.putLong(off + 48, traceHi);
                buf.putLong(off + 56, traceLo);
                buf.putLong(off + 64, span);
            }
            publish(off, len, BRACKET);
            return true;
        } catch (Throwable e) {
            return abandon(off, len);
        }
    }

    /**
     * Writes a WATERMARK: every bracket that exited before mono is in
     * the ring ahead of this record (or counted as dropped). No allocation.
     */
    boolean watermark(long mono) {
        int off = reserve(WATERMARK_LEN, WATERMARK);
        if (off < 0) {
            return false;
        }
        try {
            buf.putLong(off + 8, mono);
            publish(off, WATERMARK_LEN, WATERMARK);
            return true;
        } catch (Throwable e) {
            return abandon(off, WATERMARK_LEN);
        }
    }

    /**
     * Writes a STACK: id u64, n u32, 4 zero bytes, then n strings
     * (u32 byte length + UTF-8), zero-padded to 8. Allocates the encoded
     * strings — once per distinct stack, not per bracket.
     */
    boolean stack(long id, String[] frames) {
        byte[][] enc = new byte[frames.length][];
        int len = 8 + 16;
        for (int i = 0; i < frames.length; i++) {
            enc[i] = utf8(frames[i]);
            len += 4 + enc[i].length;
        }
        return strings(STACK, align(len), id, frames.length, 0, enc);
    }

    /**
     * Writes a THREAD: Java thread id u64, kernel tid i32 (0 = unknown),
     * flags u32 (VIRTUAL), then one string, the thread's name (u32
     * length + UTF-8), padded. A virtual thread's ktid is the carrier it
     * first ran a bracket on; its brackets carry their own (Brackets).
     */
    boolean thread(long threadId, int ktid, int flags, String name) {
        byte[][] enc = {utf8(name)};
        return strings(THREAD, align(8 + 16 + 4 + enc[0].length), threadId, ktid, flags, enc);
    }

    /**
     * Writes a HOOK: one row of the agent's hook table — id u64, n u32 =
     * 5, 4 zero bytes, then kind, class, method, native and the
     * comma-separated syscall allowlist as strings. The agent writes the
     * whole table at install, into the still empty ring, so the joiner
     * learns which syscalls each bracket may stand for from the ring
     * itself, whatever table variant (or table= override) the JVM chose.
     */
    boolean hook(int id, String kind, String cls, String method, String nativeName,
                 String syscalls) {
        String[] f = {kind, cls, method, nativeName, syscalls};
        byte[][] enc = new byte[f.length][];
        int len = 8 + 16;
        for (int i = 0; i < f.length; i++) {
            enc[i] = utf8(f[i]);
            len += 4 + enc[i].length;
        }
        return strings(HOOK, align(len), id, f.length, 0, enc);
    }

    /**
     * Writes an HTTP record: one finished request as an HTTP hook saw it
     * (decision #22; iormesh.agent.http) — exit stamp, stack id, Java
     * thread id, kernel tid, then meta (indices M_*): status, the
     * request-header count, flags (HTTP_SERVER), the number of HTTP
     * exchanges the request took (more than 1 when the library followed a
     * redirect or answered an authentication challenge on its own: the
     * tracer then stops numbering the connection), the start stamp and the
     * body sizes; the OTEL span current when the request was recorded
     * (trace id halves, span id; zeros for none), then the strings: the
     * HTTP_FIXED leading ones (method, scheme, host, path, version, local
     * and remote socket address), meta[M_NREQ] request headers and the
     * response headers, each "name: value", already redacted by the probe
     * that built them. A record past MAX_RECORD loses its headers rather
     * than the request: the request is what pairs the hops.
     */
    boolean http(long mono, long stack, long thread, int ktid, String[] s, long[] meta,
                 long traceHi, long traceLo, long span) {
        int n = s.length;
        int nreq = (int) meta[M_NREQ];
        byte[][] enc = encode(s, n);
        int len = align(HTTP_HEAD + size(enc, n));
        if (len > MAX_RECORD && n > HTTP_FIXED) {
            n = HTTP_FIXED;
            nreq = 0;
            len = align(HTTP_HEAD + size(enc, n));
        }
        if (len > MAX_RECORD) {
            countDrop(HTTP, len);
            return false;
        }
        int off = reserve(len, HTTP);
        if (off < 0) {
            return false;
        }
        try {
            putHttpHead(off, mono, stack, thread, ktid, n, nreq, meta);
            buf.putLong(off + 56, traceHi);
            buf.putLong(off + 64, traceLo);
            buf.putLong(off + 72, span);
            putStrings(off + HTTP_HEAD, enc, n);
            publish(off, len, HTTP);
            return true;
        } catch (Throwable e) {
            return abandon(off, len);
        }
    }

    /** HTTP's fixed words but the OTEL ones (see http). */
    private void putHttpHead(int off, long mono, long stack, long thread, int ktid, int n,
                             int nreq, long[] meta) {
        buf.putLong(off + 8, mono);
        buf.putLong(off + 16, stack);
        buf.putLong(off + 24, thread);
        buf.putInt(off + 32, ktid);
        buf.putInt(off + 36, (int) meta[M_STATUS]);
        buf.putInt(off + 40, n);
        buf.putInt(off + 44, nreq);
        buf.putInt(off + 48, (int) meta[M_FLAGS]);
        buf.putInt(off + 52, (int) Math.max(meta[M_EXCHANGES], 1));
        buf.putLong(off + 80, meta[M_START]);
        buf.putLong(off + 88, meta[M_REQ_BYTES]);
        buf.putLong(off + 96, meta[M_RESP_BYTES]);
    }

    /**
     * Writes a DNS record: one name lookup a DNS hook saw resolve
     * (decision #8; iormesh.agent.http.JdkDns) — exit stamp, stack id,
     * Java thread id, kernel tid, the string count and the OTEL span,
     * then the host and its addresses (s), at most MAX_DNS_ADDRS of them.
     */
    boolean dns(long mono, long stack, long thread, int ktid, String[] s,
                long traceHi, long traceLo, long span) {
        int n = Math.min(s.length, 1 + MAX_DNS_ADDRS);
        byte[][] enc = encode(s, n);
        int len = align(DNS_HEAD + size(enc, n));
        if (len > MAX_RECORD) {
            countDrop(DNS, len);
            return false;
        }
        int off = reserve(len, DNS);
        if (off < 0) {
            return false;
        }
        try {
            buf.putLong(off + 8, mono);
            buf.putLong(off + 16, stack);
            buf.putLong(off + 24, thread);
            buf.putInt(off + 32, ktid);
            buf.putInt(off + 36, n);
            buf.putLong(off + 40, traceHi);
            buf.putLong(off + 48, traceLo);
            buf.putLong(off + 56, span);
            putStrings(off + DNS_HEAD, enc, n);
            publish(off, len, DNS);
            return true;
        } catch (Throwable e) {
            return abandon(off, len);
        }
    }

    private static byte[][] encode(String[] s, int n) {
        byte[][] enc = new byte[n][];
        for (int i = 0; i < n; i++) {
            enc[i] = utf8(s[i] == null ? "" : s[i]);
        }
        return enc;
    }

    /** The encoded size of the first n strings (u32 length + bytes each). */
    private static int size(byte[][] enc, int n) {
        int len = 0;
        for (int i = 0; i < n; i++) {
            len += 4 + enc[i].length;
        }
        return len;
    }

    /** Counts a record of type the caller chose not to write (StackIds' cap). */
    void lost(int type) {
        countDrop(type, 0);
    }

    /** Records dropped so far, all types (the debug dump). */
    long dropped() {
        long n = 0;
        for (int t = 0; t < 8; t++) {
            n += (long) LONGS.getVolatile(buf, DROPPED + 8 * t);
        }
        return n;
    }

    /**
     * Fills and publishes a STACK, THREAD or HOOK record (shared shape:
     * id, a count or tid, a flags word, strings).
     */
    private boolean strings(int type, int len, long id, int word, int flags, byte[][] enc) {
        if (len > MAX_RECORD) {
            countDrop(type, len);
            return false;
        }
        int off = reserve(len, type);
        if (off < 0) {
            return false;
        }
        try {
            buf.putLong(off + 8, id);
            buf.putInt(off + 16, word);
            buf.putInt(off + 20, flags);
            putStrings(off + 24, enc, enc.length);
            publish(off, len, type);
            return true;
        } catch (Throwable e) {
            return abandon(off, len);
        }
    }

    /** Writes the first n strings at p, each a u32 length and its bytes. */
    private void putStrings(int p, byte[][] enc, int n) {
        for (int i = 0; i < n; i++) {
            buf.putInt(p, enc[i].length);
            buf.put(p + 4, enc[i], 0, enc[i].length);
            p += 4 + enc[i].length;
        }
    }

    /**
     * Reserves len bytes (a multiple of 8) and returns the record's
     * buffer offset, or -1 after counting a drop of type when the reader
     * is too far behind. A record that does not fit before the end of
     * the data area reserves a PAD for the rest of the lap as well. Once
     * the CAS succeeded every reserved byte must end up published, or
     * the reader waits on it for ever: a PAD whose publish throws is
     * abandoned, and so is the record behind it.
     */
    int reserve(int len, int type) {
        while (true) {
            long head = (long) LONGS.getVolatile(buf, HEAD);
            long tail = (long) LONGS.getAcquire(buf, TAIL);
            long pos = head & (capacity - 1);
            long pad = pos + len > capacity ? capacity - pos : 0;
            if (head + pad + len - tail > capacity) {
                countDrop(type, len);
                return -1;
            }
            if (LONGS.compareAndSet(buf, HEAD, head, head + pad + len)) {
                if (pad > 0) {
                    return padThenRecord(DATA + (int) pos, (int) pad, len);
                }
                return DATA + (int) pos;
            }
        }
    }

    /** Publishes the lap's closing PAD; the record then starts the next lap. */
    private int padThenRecord(int padOff, int pad, int len) {
        try {
            publish(padOff, pad, PAD);
            return DATA;
        } catch (Throwable e) {
            abandon(padOff, pad);
            abandon(DATA, len);
            return -1;
        }
    }

    private void countDrop(int type, int len) {
        LONGS.getAndAdd(buf, DROPPED + 8 * type, 1L);
        LONGS.getAndAdd(buf, DROPPED_BYTES, (long) len);
    }

    /**
     * A fill that threw (e.g. StackOverflowError deep in the target)
     * must still publish its reservation, or the reader would wait on it
     * forever: it becomes a PAD, counted as abandoned.
     */
    boolean abandon(int off, int len) {
        try {
            publish(off, len, PAD);
            LONGS.getAndAdd(buf, ABANDONED, 1L);
        } catch (Throwable ignored) {
            // Nothing left to try; the reader reports the ring stalled.
        }
        return false;
    }

    /** Makes a filled record visible to the reader (release store). */
    private void publish(int off, int len, int type) {
        LONGS.setRelease(buf, off, (len & 0xffffffffL) | ((long) type << 32));
    }

    private static int align(int len) {
        return (len + 7) & ~7;
    }

    private static byte[] utf8(String s) {
        String cut = s.length() > MAX_STRING ? s.substring(0, MAX_STRING) : s;
        return cut.getBytes(StandardCharsets.UTF_8);
    }
}
