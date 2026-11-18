package iormesh.s1.bridge;

import java.io.PrintStream;
import java.io.RandomAccessFile;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.channels.FileChannel;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Throwaway (S1, tasks wa4 + xa4). Loaded by the bootstrap loader, because
 * the rewritten java.base methods can only link against classes it sees.
 * Each hooked call is one bracket: (kernel TID, hook, enter/exit nanoTime,
 * the caller stack). Where the brackets go is the transport experiment
 * (decision #27), picked by {@link #configure}:
 * <ul>
 * <li>{@code mem} — kept in memory, dumped as text at shutdown (wa4's
 *     join run: no agent I/O inside the scenario at all);
 * <li>{@code ring} — binary records in the mmap spool ({@link Ring}): no
 *     syscall per record;
 * <li>{@code file} — the same records appended to a spool file through a
 *     64 KiB per-thread buffer: one write(2) per flush, which the tracer
 *     sees as the target's I/O unless it filters the spool fd;
 * <li>{@code noop} — enter/exit return at once (rewrite cost only).
 * </ul>
 * {@code stack=false} records stack id 0 instead of walking the stack, so
 * the cost runs can split out the stack walk's share.
 *
 * Clocks: System.nanoTime() is CLOCK_MONOTONIC. The BPF side must stamp
 * whatever it joins against these brackets with bpf_ktime_get_ns (also
 * MONOTONIC) — see spikes/s1/clock — not the boot clock.
 */
public final class Hooks {
    private static final int MAX_DEPTH = 16;
    private static final int FLUSH_BYTES = 64 * 1024;
    private static final StackWalker WALKER = StackWalker.getInstance();

    private static final int MEM = 0;
    private static final int RING = 1;
    private static final int FILE = 2;
    private static final int NOOP = 3;

    private static int mode = MEM;
    private static boolean walkStack = true;
    private static Ring ring;
    private static FileChannel spoolFile;
    private static final List<Rec> DONE = new ArrayList<>();
    private static final List<ByteBuffer> FILE_BUFS = new ArrayList<>();
    private static final ConcurrentHashMap<Long, Boolean> KNOWN_STACKS =
            new ConcurrentHashMap<>();
    private static volatile boolean dumping;

    private static final ThreadLocal<TState> STATE = ThreadLocal.withInitial(TState::new);

    /** One hooked call, mem mode only. */
    private record Rec(int hook, long ktid, String thread, long enter, String stack,
                       long exit) {}

    /** Per-thread bracket stack and scratch buffers; no allocation per call. */
    private static final class TState {
        boolean busy;
        boolean announced;
        long ktid = -2;   // -2 = not looked up yet, -1 = lookup failed
        int depth;
        final int[] hooks = new int[MAX_DEPTH];
        final long[] enters = new long[MAX_DEPTH];
        final long[] stacks = new long[MAX_DEPTH];
        final String[] texts = new String[MAX_DEPTH];
        final ByteBuffer scratch = ByteBuffer.allocate(4096).order(ByteOrder.LITTLE_ENDIAN);
        ByteBuffer fileBuf;
    }

    private Hooks() {}

    /** Called once by the agent before any class is rewritten. */
    public static void configure(String m, boolean stack, String spool) throws Exception {
        // "ring:<k>" sizes the ring at 2^k bytes (default 2^28) — small
        // rings show the drop accounting.
        long ringBytes = 1L << 28;
        if (m.startsWith("ring:")) {
            ringBytes = 1L << Integer.parseInt(m.substring(5));
            m = "ring";
        }
        mode = switch (m) {
            case "mem" -> MEM;
            case "ring" -> RING;
            case "file" -> FILE;
            case "noop" -> NOOP;
            default -> throw new IllegalArgumentException("mode " + m);
        };
        walkStack = stack;
        if (mode == RING) {
            ring = Ring.create(spool, ringBytes);
        } else if (mode == FILE) {
            RandomAccessFile f = new RandomAccessFile(spool, "rw");
            f.setLength(0);
            spoolFile = f.getChannel();
        }
    }

    /**
     * Loads and initializes what the hot path touches (thread state, the
     * stack walker's stream machinery) at startup, so no agent class
     * loading — and none of its jimage reads — lands inside the target's
     * own I/O later. Emits nothing.
     */
    public static void warmUp() {
        STATE.get();
        WALKER.walk(s -> s.skip(0).limit(2).toList());
    }

    public static void enter(int hook) {
        if (mode == NOOP) {
            return;
        }
        TState t = STATE.get();
        if (t.busy || dumping || t.depth >= MAX_DEPTH) {
            // Keep enter/exit pairs balanced; -1 makes exit drop this one.
            if (t.depth < MAX_DEPTH) {
                t.hooks[t.depth] = -1;
            }
            t.depth++;
            return;
        }
        t.busy = true;
        try {
            int d = t.depth;
            t.hooks[d] = hook;
            t.stacks[d] = 0;
            t.texts[d] = null;
            if (t.ktid == -2) {
                t.ktid = kernelTid();
            }
            if (walkStack) {
                stackInto(t, d);
            }
            t.depth = d + 1;
            t.enters[d] = System.nanoTime();
        } finally {
            t.busy = false;
        }
    }

    public static void exit(int hook) {
        if (mode == NOOP) {
            return;
        }
        long t1 = System.nanoTime();
        TState t = STATE.get();
        if (t.depth == 0) {
            return;
        }
        int d = --t.depth;
        if (d >= MAX_DEPTH || t.hooks[d] != hook || t.busy || dumping) {
            return;   // a skipped (nested, agent-own or overflow) enter
        }
        t.busy = true;
        try {
            record(t, d, t1);
        } finally {
            t.busy = false;
        }
    }

    private static void record(TState t, int d, long exit) {
        if (mode == MEM) {
            synchronized (DONE) {
                DONE.add(new Rec(t.hooks[d], t.ktid, Thread.currentThread().getName(),
                        t.enters[d], t.texts[d], exit));
            }
            return;
        }
        if (!t.announced) {
            t.announced = true;
            emitThread(t);
        }
        ByteBuffer s = t.scratch.clear();
        s.putInt(t.hooks[d]).putInt((int) t.ktid).putLong(t.enters[d]).putLong(exit)
                .putLong(t.stacks[d]).putLong(Thread.currentThread().threadId());
        emit(t, Ring.BRACKET, s);
    }

    private static void emitThread(TState t) {
        byte[] name = Thread.currentThread().getName().getBytes(StandardCharsets.UTF_8);
        ByteBuffer s = t.scratch.clear();
        s.putLong(Thread.currentThread().threadId()).putInt((int) t.ktid)
                .putInt(name.length).put(name, 0, Math.min(name.length, 1024));
        emit(t, Ring.THREAD, s);
    }

    /**
     * Hashes the caller frames into a stack id; the first time an id is
     * seen its text goes out as a STACK record (mem mode keeps the text).
     */
    private static void stackInto(TState t, int d) {
        List<StackWalker.StackFrame> frames = WALKER.walk(s -> s.skip(2).limit(10).toList());
        long h = 1125899906842597L;
        for (StackWalker.StackFrame f : frames) {
            h = 31 * h + f.getClassName().hashCode();
            h = 31 * h + f.getMethodName().hashCode();
            h = 31 * h + f.getByteCodeIndex();
        }
        t.stacks[d] = h;
        boolean isNew = KNOWN_STACKS.putIfAbsent(h, Boolean.TRUE) == null;
        if (mode == MEM || isNew) {
            StringBuilder sb = new StringBuilder();
            for (StackWalker.StackFrame f : frames) {
                if (!sb.isEmpty()) {
                    sb.append(" < ");
                }
                sb.append(f.getClassName()).append('.').append(f.getMethodName())
                        .append(':').append(f.getLineNumber());
            }
            if (mode == MEM) {
                t.texts[d] = sb.toString();
            } else {
                byte[] b = sb.toString().getBytes(StandardCharsets.UTF_8);
                int n = Math.min(b.length, 3000);
                ByteBuffer s = t.scratch.clear();
                s.putLong(h).putInt(n).putInt(0).put(b, 0, n);
                emit(t, Ring.STACK, s);
            }
        }
    }

    /** Frames the scratch body as one record and hands it to the transport. */
    private static void emit(TState t, int type, ByteBuffer body) {
        int len = 8 + ((body.position() + 7) & ~7);
        if (mode == RING) {
            long off = ring.reserve(len);
            if (off >= 0) {
                // Padding bytes stay zero: the reader zeroes what it consumed.
                ring.putBytes(off + 8, body.array(), body.position());
                ring.publish(off, len, type);
            }
            return;
        }
        if (t.fileBuf == null) {
            t.fileBuf = ByteBuffer.allocate(FLUSH_BYTES).order(ByteOrder.LITTLE_ENDIAN);
            synchronized (FILE_BUFS) {
                FILE_BUFS.add(t.fileBuf);
            }
        }
        if (t.fileBuf.remaining() < len) {
            flush(t.fileBuf);
        }
        t.fileBuf.putLong((len & 0xffffffffL) | ((long) type << 32));
        t.fileBuf.put(body.array(), 0, body.position());
        t.fileBuf.position(t.fileBuf.position() + (len - 8 - body.position()));
    }

    /** One write(2) of a thread's buffer — the self-noise of file mode. */
    private static void flush(ByteBuffer b) {
        synchronized (FILE_BUFS) {
            b.flip();
            try {
                while (b.hasRemaining()) {
                    spoolFile.write(b);
                }
            } catch (Exception e) {
                // Spike: a failed flush loses the buffer; production counts it.
            }
            b.clear();
        }
    }

    /** /proc/thread-self resolves to /proc/PID/task/TID: one readlink per thread. */
    private static long kernelTid() {
        try {
            return Long.parseLong(Files.readSymbolicLink(Path.of("/proc/thread-self"))
                    .getFileName().toString());
        } catch (Exception e) {
            return -1;   // the record then cannot join, which agentjoin reports
        }
    }

    /**
     * At shutdown: mem mode writes its records as tab-separated text,
     * file mode flushes every thread's buffer; ring mode has nothing left
     * to do (the records are in the shared mapping already).
     */
    public static void dump(String path) {
        dumping = true;
        if (mode == FILE) {
            synchronized (FILE_BUFS) {
                for (ByteBuffer b : FILE_BUFS) {
                    flush(b);
                }
            }
            return;
        }
        if (mode != MEM) {
            return;
        }
        try (PrintStream out = new PrintStream(path)) {
            synchronized (DONE) {
                for (Rec r : DONE) {
                    out.printf("%d\t%d\t%s\t%d\t%d\t%s%n", r.hook(), r.ktid(), r.thread(),
                            r.enter(), r.exit(), r.stack());
                }
            }
        } catch (Exception e) {
            e.printStackTrace();
        }
    }
}
