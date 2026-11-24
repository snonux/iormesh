package iormesh.bridge;

import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.nio.file.Files;
import java.nio.file.Path;

/**
 * One thread's open brackets and its identity for the ring, kept in a
 * ThreadLocal by Hooks. Small until used (task dj4): an app may keep
 * many thousands of (virtual) threads alive, and the agent's cost must
 * scale with its I/O, not with its threads. So the bracket slots start
 * at FIRST_SLOTS and double up to Hooks.MAX_DEPTH when a deeper nesting
 * first needs them, and the HTTP stamps exist only once the thread ran
 * an entry-stamped HTTP hook. The slots grow at most a few times per
 * thread; past that a bracket's only allocation is its short-lived
 * entry in the watermark's set of closing threads (Watermarks.closing).
 *
 * {@link #closing} is the only field another thread reads: the
 * watermark thread (Watermarks) must know which threads are between
 * stamping a bracket's exit and publishing it. Nothing else holds this
 * object, so it goes with its thread; Watermarks sees it only while it
 * is closing.
 */
final class Brackets {
    /** {@link #closing} when no bracket of this thread is being written. */
    static final long IDLE = Long.MAX_VALUE;
    /** Kernel tid not looked up yet; 0 means the lookup failed. */
    static final int UNKNOWN_KTID = -1;
    /** {@link #hook} of a slot never allocated: no hook id is negative but SKIPPED. */
    static final int NO_SLOT = Integer.MIN_VALUE;
    /** Slots a thread's first bracket allocates; most I/O nests one or two deep. */
    static final int FIRST_SLOTS = 2;

    /**
     * One slot's fields in {@link #slots}, SLOT longs per depth: the
     * hook id, the enter stamp, the stack id, a virtual thread's carrier
     * tid at enter (unused otherwise), and the OTEL span current at enter
     * (Otel: trace id halves and span id, all zero for none). One array,
     * not one per field, so a small thread pays one array header.
     */
    private static final int HOOK = 0;
    private static final int ENTER = 1;
    private static final int STACK = 2;
    private static final int KTID = 3;
    private static final int TRACE_HI = 4;
    private static final int TRACE_LO = 5;
    private static final int SPAN = 6;
    private static final int SLOT = 7;

    /**
     * Thread.isVirtual(), JDK 21+; null on 17–20, which have no virtual
     * threads. Looked up reflectively: the bridge is built for 17.
     */
    private static final MethodHandle IS_VIRTUAL = isVirtualHandle();

    final Thread owner = Thread.currentThread();
    /**
     * Whether owner is a virtual thread. Its kernel tid is then its
     * current carrier's, which can change at every park, so it is
     * looked up per bracket instead of cached (see {@link #carrierTid}).
     */
    final boolean virtual = isVirtual(owner);
    boolean busy;
    int depth;
    /** The open brackets' slots (SLOT longs each), null before the first. */
    private long[] slots;
    /**
     * The HTTP hooks whose method is running on this thread and stamped
     * its entry (Hooks.httpEnter, task wd4): each one's start stamp and
     * its this, innermost last, so a request's latency starts where its
     * library began it. Deeper nesting is counted but not kept; selves are
     * cleared at the exit, so nothing of the target is held after it.
     * Null until the thread's first stamp (task dj4); httpStarts is
     * assigned last, so it being set means all three are.
     */
    private long[] httpStarts;
    private Object[] httpSelves;
    /** Each open stamp's hook id: an exit can tell it runs nested in its own hook (task de4). */
    private int[] httpIds;
    int httpDepth;
    /** The span Otel.read found last on this thread (all zero: none). */
    long otelHi;
    long otelLo;
    long otelSpan;
    /**
     * The span context otelHi/otelLo/otelSpan were read from, compared
     * by identity so a thread reads a span's ids once (Otel.Reader.read);
     * weak, so it pins no app class loader.
     */
    java.lang.ref.WeakReference<Object> otelContext;
    int ktid = UNKNOWN_KTID;
    /**
     * The name this thread's last THREAD record carried (the very String
     * getName returned then), null before one reached the ring: a
     * different String means the thread was renamed (Hooks.describeDue).
     */
    String described;
    /**
     * Hooks.generation when that THREAD record was written: an older one
     * means a resync (task 5j4) asked for it again.
     */
    int generation;
    /** After a dropped THREAD, no retry before this nanoTime. */
    long retryAt;
    /**
     * When that THREAD record was written (nanoTime): a virtual thread
     * describes itself again Hooks.redescribeNs later (task 2j4).
     */
    long describedAt;
    /**
     * The enter stamp of the bracket being written to the ring, or IDLE:
     * set before the exit is stamped and cleared after the record is
     * published, so a watermark never claims a time this bracket's
     * exit may precede. Set through Watermarks.closing, which also
     * enters the thread in the set the watermark scans; restored by the
     * caller's finally, which then calls Watermarks.leave.
     */
    volatile long closing = IDLE;

    /**
     * Makes slot d exist and marks it with mark (Hooks' SKIPPED) until
     * {@link #open} fills it. Growing allocates and so may throw; slot d
     * then stays missing ({@link #hook} gives NO_SLOT) and its exit is
     * dropped, the depth balanced all the same.
     */
    void reserve(int d, int mark) {
        long[] s = slots;
        if (s == null || d * SLOT >= s.length) {
            int n = s == null ? FIRST_SLOTS : s.length / SLOT;
            while (n <= d) {
                n *= 2;
            }
            s = s == null ? new long[n * SLOT] : java.util.Arrays.copyOf(s, n * SLOT);
            slots = s;
        }
        s[d * SLOT + HOOK] = mark;
    }

    /**
     * Fills reserved slot d: the stack, the carrier tid, the thread's
     * current OTEL span (otelHi/otelLo/otelSpan) and the enter stamp,
     * then the hook last, so a half-filled slot still reads as reserved.
     */
    void open(int d, int hook, long stack, int carrier, long enter) {
        long[] s = slots;
        int o = d * SLOT;
        s[o + STACK] = stack;
        s[o + KTID] = carrier;
        s[o + TRACE_HI] = otelHi;
        s[o + TRACE_LO] = otelLo;
        s[o + SPAN] = otelSpan;
        s[o + ENTER] = enter;
        s[o + HOOK] = hook;
    }

    /** Slot d's hook id (or mark), NO_SLOT if the slot was never allocated. */
    int hook(int d) {
        long[] s = slots;
        return s == null || d * SLOT >= s.length ? NO_SLOT : (int) s[d * SLOT + HOOK];
    }

    long enter(int d) {
        return slots[d * SLOT + ENTER];
    }

    long stack(int d) {
        return slots[d * SLOT + STACK];
    }

    long traceHi(int d) {
        return slots[d * SLOT + TRACE_HI];
    }

    long traceLo(int d) {
        return slots[d * SLOT + TRACE_LO];
    }

    long span(int d) {
        return slots[d * SLOT + SPAN];
    }

    /** Pushes HTTP stamp d (d < Hooks.MAX_HTTP_DEPTH), allocating the stamps at the first. */
    void httpPush(int d, Object self, int id, long start) {
        if (httpStarts == null) {
            long[] starts = new long[Hooks.MAX_HTTP_DEPTH];
            Object[] selves = new Object[Hooks.MAX_HTTP_DEPTH];
            int[] ids = new int[Hooks.MAX_HTTP_DEPTH];
            httpSelves = selves;
            httpIds = ids;
            httpStarts = starts;
        }
        httpSelves[d] = self;
        httpIds[d] = id;
        httpStarts[d] = start;
    }

    /** HTTP stamp d's start, 0 if the stamps were never allocated (a failed push). */
    long httpStart(int d) {
        return httpStarts == null ? 0 : httpStarts[d];
    }

    /** HTTP stamp d's this, null if the stamps were never allocated. */
    Object httpSelf(int d) {
        return httpStarts == null ? null : httpSelves[d];
    }

    /** Drops HTTP stamp d's this, so nothing of the target is held after its exit. */
    void httpClear(int d) {
        if (httpStarts != null) {
            httpSelves[d] = null;
        }
    }

    /** Whether a stamp of hook id is open below depth d. */
    boolean httpNested(int d, int id) {
        if (httpStarts == null) {
            return false;
        }
        for (int k = 0; k < d; k++) {
            if (httpIds[k] == id) {
                return true;
            }
        }
        return false;
    }

    /**
     * The kernel tid, looked up once per thread: /proc/thread-self
     * resolves to /proc/PID/task/TID. This readlink is one of the
     * agent's own syscalls (decision #27); the tracer tags the first one
     * per TID. 0 if the lookup failed — the thread's brackets then
     * cannot join.
     */
    int kernelTid() {
        if (ktid == UNKNOWN_KTID) {
            ktid = lookupKtid();
        }
        return ktid;
    }

    /**
     * The kernel tid a bracket of slot d can be joined on, read at its
     * close: the cached one for a platform thread; for a virtual thread
     * the carrier's, looked up again now and compared with the one at
     * enter. A virtual thread may park inside a bracket (a socket accept
     * or read waits in the poller and resumes on any carrier), and a
     * bracket that spans two carriers cannot be joined by tid: it gets 0
     * (unknown), and its syscalls stay attribution none. Equal tids do
     * not prove it stayed — it may have left the carrier and come back,
     * the carrier running other virtual threads meanwhile — so the
     * joiner never joins a virtual thread's bracket of a hook that can
     * park (internal/correlate canPark). These two
     * readlinks per bracket are the price of virtual threads, which v1
     * only flags (docs/milestones.md M3, "Platform threads only").
     */
    int carrierTid(int d) {
        if (!virtual) {
            return ktid;
        }
        int now = lookupKtid();
        return now == (int) slots[d * SLOT + KTID] ? now : 0;
    }

    static int lookupKtid() {
        try {
            return Integer.parseInt(Files.readSymbolicLink(Path.of("/proc/thread-self"))
                    .getFileName().toString());
        } catch (Exception e) {
            return 0;
        }
    }

    private static MethodHandle isVirtualHandle() {
        try {
            return MethodHandles.publicLookup().findVirtual(Thread.class, "isVirtual",
                    MethodType.methodType(boolean.class));
        } catch (ReflectiveOperationException e) {
            return null;
        }
    }

    private static boolean isVirtual(Thread t) {
        if (IS_VIRTUAL == null) {
            return false;
        }
        try {
            return (boolean) IS_VIRTUAL.invokeExact(t);
        } catch (Throwable e) {
            return false;
        }
    }
}
