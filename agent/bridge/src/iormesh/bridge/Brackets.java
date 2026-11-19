package iormesh.bridge;

import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.nio.file.Files;
import java.nio.file.Path;

/**
 * One thread's open brackets and its identity for the ring, kept in a
 * ThreadLocal by Hooks. Preallocated, so a bracket allocates nothing.
 *
 * {@link #closing} is the only field another thread reads: the
 * watermark thread (Watermarks) must know which threads are between
 * stamping a bracket's exit and publishing it.
 */
final class Brackets {
    /** {@link #closing} when no bracket of this thread is being written. */
    static final long IDLE = Long.MAX_VALUE;
    /** Kernel tid not looked up yet; 0 means the lookup failed. */
    static final int UNKNOWN_KTID = -1;

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
    final int[] hooks = new int[Hooks.MAX_DEPTH];
    final long[] enters = new long[Hooks.MAX_DEPTH];
    final long[] stacks = new long[Hooks.MAX_DEPTH];
    /** A virtual thread's carrier tid at each slot's enter (unused otherwise). */
    final int[] ktids = new int[Hooks.MAX_DEPTH];
    /**
     * The OTEL span current at each slot's enter (Otel): trace id halves
     * and span id, all zero for none.
     */
    final long[] traceHis = new long[Hooks.MAX_DEPTH];
    final long[] traceLos = new long[Hooks.MAX_DEPTH];
    final long[] spans = new long[Hooks.MAX_DEPTH];
    /**
     * The HTTP hooks whose method is running on this thread and stamped
     * its entry (Hooks.httpEnter, task wd4): each one's start stamp and
     * its this, innermost last, so a request's latency starts where its
     * library began it. Deeper nesting is counted but not kept; selves are
     * cleared at the exit, so nothing of the target is held after it.
     */
    final long[] httpStarts = new long[Hooks.MAX_HTTP_DEPTH];
    final Object[] httpSelves = new Object[Hooks.MAX_HTTP_DEPTH];
    /** Each open stamp's hook id: an exit can tell it runs nested in its own hook (task de4). */
    final int[] httpIds = new int[Hooks.MAX_HTTP_DEPTH];
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
    /** After a dropped THREAD, no retry before this nanoTime. */
    long retryAt;
    /**
     * The enter stamp of the bracket being written to the ring, or IDLE:
     * set before the exit is stamped and cleared after the record is
     * published, so a watermark never claims a time this bracket's
     * exit may precede.
     */
    volatile long closing = IDLE;

    Brackets() {
        Watermarks.register(this);
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
     * only flags (PLAN.md M3, "Platform threads only").
     */
    int carrierTid(int d) {
        if (!virtual) {
            return ktid;
        }
        int now = lookupKtid();
        return now == ktids[d] ? now : 0;
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
