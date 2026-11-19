package iormesh.bridge;

import java.lang.invoke.VarHandle;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicLongArray;
import java.util.function.BiFunction;
import java.util.function.LongSupplier;

/**
 * The entry points the rewritten JDK methods call: {@code enter(id)} at
 * the start of a hooked method, {@code exit(id)} before each return and
 * on the exceptional path (BoundaryTransformer). One enter/exit pair is
 * a <em>bracket</em> around the method's native I/O call.
 *
 * Loaded by the bootstrap class loader (the agent appends the bridge jar
 * to its search path), because java.base code can only link classes
 * that loader sees; java.base is also made to read this jar's unnamed
 * module (Agent). So this jar holds only what the hot path needs: no
 * ASM, nothing of the agent core.
 *
 * Each thread keeps a small stack of open brackets: hooked methods nest
 * (RandomAccessFile.write(byte[]) reaches the hooked writeBytes), and
 * the innermost bracket containing a syscall is the one the joiner
 * keeps. A bracket opened while the thread is already inside the bridge
 * (the stack walk loading a class, which reads a jar) is skipped, so the
 * agent never brackets its own work.
 *
 * Neither entry point ever throws into the target, and both keep the
 * per-thread depth balanced whatever fails: a slot is marked SKIPPED
 * before anything that can throw runs, and only marked with its hook
 * once the bracket is fully open. An exit that threw would be worse
 * than a lost bracket — the pre-return exit calls lie inside the
 * method's catch-all range, so the handler would exit a second time and
 * close the caller's bracket.
 *
 * A closed bracket becomes a BRACKET record in the ring (decision #27,
 * {@link Ring}); a thread's first bracket also writes its THREAD record,
 * and a stack's first sighting its STACK record (StackIds). Neither is
 * guaranteed to precede the brackets naming it — a dropped one is
 * retried later, and another thread may publish a bracket on a stack
 * whose STACK is still being written — so the joiner resolves ids
 * against everything it has read. Without a
 * ring (no spool= option) brackets are only counted, and only when the
 * agent's debug dump asked for counts ({@link #counts}).
 *
 * HTTP hooks (decision #22, task 3b4) are a second kind of entry point,
 * {@link #http}: called once when a hooked HTTP method returns (or
 * throws), with what the method has at hand, it hands those objects to
 * the agent's probe for that hook and writes the request the probe
 * describes as one HTTP record. No bracket: an HTTP event is not a
 * syscall to join, it is an event of its own, stamped and stacked here.
 */
public final class Hooks {
    /** Deeper nesting than this is not tracked (still balanced). */
    static final int MAX_DEPTH = 16;
    /** How long a dropped THREAD or STACK waits before it is tried again. */
    static final long RETRY_NS = 1_000_000;
    /** Marks a skipped slot, so its exit is dropped too. */
    private static final int SKIPPED = -1;
    private static final LongSupplier WALK = StackIds::current;
    private static final LongSupplier NO_WALK = () -> StackIds.NONE;

    /** Where enter gets its stack id; package-private so tests can fail it. */
    static volatile LongSupplier stacks = WALK;
    /** Closed brackets per hook id; package-private so tests can break it. */
    static volatile AtomicLongArray counts = new AtomicLongArray(0);
    /** The transport, or null (no spool=): brackets then go nowhere. */
    static volatile Ring ring;
    private static final AtomicLong SKIPS = new AtomicLong();
    private static final AtomicLong FAILS = new AtomicLong();
    private static final AtomicBoolean CLAIMED = new AtomicBoolean();
    private static final ThreadLocal<Brackets> STATE = ThreadLocal.withInitial(Brackets::new);
    /**
     * The HTTP probes by HTTP hook id (iormesh.agent.http): given {return
     * value, this, argument} of a hooked method and an int[2] to fill with
     * {status, request-header count}, a probe returns the request's
     * strings — the Ring.HTTP_FIXED leading ones, then the headers,
     * redacted — or null when this call records nothing (a binding hook,
     * a request already recorded, no response). JDK types only: the agent
     * core, which implements them, is on another loader.
     */
    @SuppressWarnings("unchecked")
    static volatile BiFunction<Object[], int[], String[]>[] httpProbes =
            (BiFunction<Object[], int[], String[]>[]) new BiFunction<?, ?, ?>[0];
    /** Requests recorded per HTTP hook id, kept like counts (debug dump only). */
    static volatile AtomicLongArray httpCounts = new AtomicLongArray(0);
    private static volatile boolean counting;

    private Hooks() {}

    /**
     * Claims the bridge for one agent install and returns whether this
     * caller got it. A second -javaagent or a repeated live attach finds
     * this (bootstrap-loaded, so shared) Hooks already claimed and must
     * not install again: it would rewrite the JDK twice — two brackets
     * per call — and reset the counters.
     */
    public static boolean claim() {
        return CLAIMED.compareAndSet(false, true);
    }

    /**
     * Undoes {@link #claim} after an install that failed before any JDK
     * class was rewritten, so a later live attach can still install;
     * also discards the ring, so the tracer does not wait on it.
     */
    public static void release() {
        Ring r = ring;
        ring = null;
        if (r != null) {
            try {
                r.discard();
            } catch (Exception e) {
                // The tracer then finds a ring that stays empty.
            }
        }
        CLAIMED.set(false);
    }

    /**
     * Called once by the agent before any JDK class is rewritten.
     * hookIds bounds the ids the table uses (ids are 1..hookIds-1);
     * stack=false records StackIds.NONE instead of walking (S1's cost
     * split, and an escape hatch while M3 engineers the walk down);
     * count=true keeps per-hook counts for the debug dump (an atomic
     * increment per bracket, so off unless asked for).
     */
    public static void configure(int hookIds, boolean stack, boolean count) {
        counts = new AtomicLongArray(count ? hookIds : 0);
        counting = count;
        stacks = stack ? WALK : NO_WALK;
    }

    /**
     * Creates and maps the ring in the tracer's spool directory and
     * returns its path; install time, before warmUp, so the warm-up
     * exercises the ring's paths too.
     */
    public static String openRing(String dir, long capacity) throws java.io.IOException {
        Ring r = Ring.create(java.nio.file.Path.of(dir), capacity);
        ring = r;
        return r.path().toString();
    }

    /**
     * Writes one HOOK record (a row of the agent's hook table) into the
     * ring; the agent calls it for every row at install, before any
     * bracket, so the joiner knows each hook's syscall allowlist. False
     * without a ring or when the record was dropped.
     */
    public static boolean describeHook(int id, String kind, String cls, String method,
                                       String nativeName, String syscalls) {
        Ring r = ring;
        return r != null && r.hook(id, kind, cls, method, nativeName, syscalls);
    }

    /**
     * Registers the probe of HTTP hook id (1..); install time, before the
     * first HTTP class is rewritten. A later registration of the same id
     * replaces the earlier one.
     */
    @SuppressWarnings("unchecked")
    public static synchronized void httpProbe(int id, BiFunction<Object[], int[], String[]> probe) {
        BiFunction<Object[], int[], String[]>[] old = httpProbes;
        BiFunction<Object[], int[], String[]>[] p = java.util.Arrays.copyOf(old,
                Math.max(old.length, id + 1));
        p[id] = probe;
        httpProbes = p;
        if (counting && httpCounts.length() < p.length) {
            httpCounts = new AtomicLongArray(p.length);
        }
    }

    /** Requests recorded so far per HTTP hook id (index = id). */
    public static long[] httpCounts() {
        AtomicLongArray c = httpCounts;
        long[] out = new long[c.length()];
        for (int i = 0; i < out.length; i++) {
            out[i] = c.get(i);
        }
        return out;
    }

    /** Starts the watermark thread (Watermarks); needs the ring. */
    public static void startWatermarks(long intervalMs) {
        Ring r = ring;
        if (r != null && intervalMs > 0) {
            Watermarks.start(r, intervalMs);
        }
    }

    /**
     * Marks the calling thread as the agent's own for good: its hooked
     * I/O is never bracketed. The agent calls it on its shutdown-hook
     * thread, iormesh-agent-exit, whose debug dump writes a file through
     * hooked JDK methods; that I/O is the agent's, not the target's.
     */
    public static void agentThread() {
        STATE.get().busy = true;
    }

    /** Writes a watermark now (the agent's shutdown hook). */
    public static void watermark() {
        Ring r = ring;
        if (r != null) {
            Watermarks.emit(r);
        }
    }

    /** Records dropped by the ring so far, or 0 without one. */
    public static long ringDropped() {
        Ring r = ring;
        return r == null ? 0 : r.dropped();
    }

    /**
     * Initializes and links what the hot path touches (the thread state,
     * the kernel tid lookup, the stack walker's stream machinery, the
     * ring's record paths) at install time, so none of that class
     * loading — and its jar reads — lands inside the target's own I/O
     * later. With a ring this writes real records: the installing
     * thread's THREAD, the warm-up stack's STACK and a first WATERMARK.
     */
    public static void warmUp() {
        Brackets t = STATE.get();
        t.busy = true;
        try {
            Ring r = ring;
            t.kernelTid();
            if (r != null) {
                describe(t, r);
                Watermarks.emit(r);
                r.dropped();
            }
            stacks.getAsLong();
        } finally {
            t.busy = false;
        }
    }

    public static void enter(int hook) {
        Brackets t;
        try {
            t = STATE.get();
        } catch (Throwable e) {
            return;           // no state, no slot: exit finds none either
        }
        int d = t.depth++;
        if (d >= MAX_DEPTH) {
            return;           // too deep: exit sees d >= MAX_DEPTH and drops it
        }
        // Skipped until fully open, so a throw below leaves a balanced,
        // dropped slot instead of a half-written bracket.
        t.hooks[d] = SKIPPED;
        if (t.busy) {
            countSkip(SKIPS);
            return;
        }
        t.busy = true;
        try {
            Ring r = ring;
            if (r != null && !t.described && System.nanoTime() >= t.retryAt) {
                describe(t, r);
            }
            t.stacks[d] = stacks.getAsLong();
            if (t.virtual) {
                // The carrier now; close compares it with the one then.
                t.ktids[d] = Brackets.lookupKtid();
            }
            // Stamped after the walk, so the walk is outside the bracket.
            t.enters[d] = System.nanoTime();
            t.hooks[d] = hook;
        } catch (Throwable e) {
            countSkip(FAILS);  // e.g. StackOverflowError in a deep recursion
        } finally {
            t.busy = false;
        }
    }

    public static void exit(int hook) {
        try {
            Brackets t = STATE.get();
            if (t.depth == 0) {
                return;       // retransformed mid-call: no matching enter
            }
            int d = --t.depth;
            if (d >= MAX_DEPTH || t.hooks[d] != hook) {
                return;       // skipped, failed or overflowed enter
            }
            Ring r = ring;
            if (r != null) {
                close(t, d, r);
            }
            AtomicLongArray c = counts;
            if (hook > 0 && hook < c.length()) {
                c.incrementAndGet(hook);
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
    }

    /**
     * The entry point of HTTP hook id, called by the rewritten method
     * before each return (ret = the value returned, or null) and on its
     * exceptional exit (ret null); self is the method's this, arg the
     * argument its row names (or null). Never throws into the target. The
     * thread is busy throughout, so the probe's own work — reflection,
     * never I/O — is not bracketed, and an HTTP hook reached from inside
     * another one's probe records nothing.
     */
    public static void http(Object ret, Object self, Object arg, int id) {
        try {
            Ring r = ring;
            BiFunction<Object[], int[], String[]>[] p = httpProbes;
            if (r == null || id <= 0 || id >= p.length || p[id] == null) {
                return;
            }
            Brackets t = STATE.get();
            if (t.busy) {
                countSkip(SKIPS);
                return;
            }
            t.busy = true;
            try {
                if (request(t, r, p[id], new Object[] {ret, self, arg})
                        && id < httpCounts.length()) {
                    httpCounts.incrementAndGet(id);
                }
            } finally {
                t.busy = false;
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
    }

    /**
     * Asks the probe and writes its request as an HTTP record. The record
     * keeps the watermark promise like a bracket (close): the thread is
     * marked closing before its stamp is taken and until the record is
     * published, so no watermark passes the stamp while the probe and the
     * stack walk run. The walk happens only for a call that records;
     * true when the probe described a request (written or dropped).
     */
    private static boolean request(Brackets t, Ring r,
                                   BiFunction<Object[], int[], String[]> probe, Object[] args) {
        t.closing = System.nanoTime();
        VarHandle.fullFence();   // as in close: the stamp after the mark
        try {
            long mono = System.nanoTime();
            int[] meta = new int[2];
            String[] s = probe.apply(args, meta);
            if (s == null) {
                return false;
            }
            if (!t.described && System.nanoTime() >= t.retryAt) {
                describe(t, r);
            }
            long stack = stacks.getAsLong();
            int ktid = t.virtual ? Brackets.lookupKtid() : t.kernelTid();
            r.http(mono, stack, t.owner.getId(), ktid, meta[0], s, meta[1]);
            return true;
        } finally {
            t.closing = Brackets.IDLE;
        }
    }

    /** The calling thread's state (the watermark thread's own, too). */
    static Brackets state() {
        return STATE.get();
    }

    /**
     * Writes slot d's BRACKET. The thread is marked closing (Watermarks)
     * before the exit is stamped and until the record is published, and
     * busy, so nothing the write might do is bracketed itself. A dropped
     * record is counted in the ring header, not here.
     */
    private static void close(Brackets t, int d, Ring r) {
        t.closing = t.enters[d];
        // The JMM does not order System.nanoTime() against volatiles:
        // without a full fence the clock read below may take effect
        // before the closing store is visible (AArch64's stlr has no
        // trailing barrier), and a watermark that read the clock later
        // could still see this thread idle and claim a time before our
        // exit. Paired with the fence in Watermarks.emit (Dekker).
        VarHandle.fullFence();
        boolean wasBusy = t.busy;
        t.busy = true;
        try {
            int ktid = t.carrierTid(d);
            long exit = System.nanoTime();
            r.bracket(t.hooks[d], ktid, t.enters[d], exit, t.stacks[d], t.owner.getId());
        } finally {
            t.busy = wasBusy;
            t.closing = Brackets.IDLE;
        }
    }

    /**
     * Writes the thread's THREAD record (its first bracket, or the
     * warm-up); a dropped one is retried at the next bracket, and the
     * kernel tid lookup — a syscall — happens only once regardless.
     * After a drop the retry waits RETRY_NS: under back-pressure every
     * bracket would otherwise encode the name again (an allocation) only
     * to be dropped again.
     */
    private static void describe(Brackets t, Ring r) {
        t.described = r.thread(t.owner.getId(), t.kernelTid(),
                t.virtual ? Ring.VIRTUAL : 0, t.owner.getName());
        if (!t.described) {
            t.retryAt = System.nanoTime() + RETRY_NS;
        }
    }

    /** Counts a dropped bracket; must not throw either. */
    private static void countSkip(AtomicLong n) {
        try {
            n.incrementAndGet();
        } catch (Throwable ignored) {
            // Counting is best effort; the target's call goes on.
        }
    }

    /** Closed brackets per hook id so far (index = hook id). */
    public static long[] counts() {
        AtomicLongArray c = counts;
        long[] out = new long[c.length()];
        for (int i = 0; i < out.length; i++) {
            out[i] = c.get(i);
        }
        return out;
    }

    /** Brackets skipped because the thread was inside the bridge already. */
    public static long skips() {
        return SKIPS.get();
    }

    /** Brackets dropped because opening or closing them threw. */
    public static long fails() {
        return FAILS.get();
    }

    /** The calling thread's open-bracket depth (tests). */
    static int depth() {
        return STATE.get().depth;
    }
}
