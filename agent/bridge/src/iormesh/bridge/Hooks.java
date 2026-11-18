package iormesh.bridge;

import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicLongArray;
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
 * Until the ring transport (M3 step 4, decision #27) lands, a closed
 * bracket only bumps its hook's counter ({@link #counts}): the agent's
 * debug dump and the smoke test read it to show the hooks fire. The
 * bracket's enter time and stack id are already captured for the ring.
 */
public final class Hooks {
    /** Deeper nesting than this is not tracked (still balanced). */
    static final int MAX_DEPTH = 16;
    /** Marks a skipped slot, so its exit is dropped too. */
    private static final int SKIPPED = -1;
    private static final LongSupplier WALK = StackIds::current;
    private static final LongSupplier NO_WALK = () -> StackIds.NONE;

    /** Where enter gets its stack id; package-private so tests can fail it. */
    static volatile LongSupplier stacks = WALK;
    /** Closed brackets per hook id; package-private so tests can break it. */
    static volatile AtomicLongArray counts = new AtomicLongArray(0);
    private static final AtomicLong SKIPS = new AtomicLong();
    private static final AtomicLong FAILS = new AtomicLong();
    private static final AtomicBoolean CLAIMED = new AtomicBoolean();
    private static final ThreadLocal<Brackets> STATE = ThreadLocal.withInitial(Brackets::new);

    /** One thread's open brackets; preallocated, so no allocation per call. */
    private static final class Brackets {
        boolean busy;
        int depth;
        final int[] hooks = new int[MAX_DEPTH];
        final long[] enters = new long[MAX_DEPTH];
        final long[] stacks = new long[MAX_DEPTH];
    }

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
     * Called once by the agent before any JDK class is rewritten.
     * hookIds bounds the ids the table uses (ids are 1..hookIds-1);
     * stack=false records StackIds.NONE instead of walking (S1's cost
     * split, and an escape hatch while M3 engineers the walk down).
     */
    public static void configure(int hookIds, boolean stack) {
        counts = new AtomicLongArray(hookIds);
        stacks = stack ? WALK : NO_WALK;
    }

    /**
     * Initializes and links what the hot path touches (the thread state,
     * the stack walker's stream machinery) at install time, so none of
     * that class loading — and its jar reads — lands inside the target's
     * own I/O later. Records nothing.
     */
    public static void warmUp() {
        Brackets t = STATE.get();
        t.busy = true;
        try {
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
            t.stacks[d] = stacks.getAsLong();
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
            AtomicLongArray c = counts;
            if (hook > 0 && hook < c.length()) {
                c.incrementAndGet(hook);
            }
        } catch (Throwable e) {
            countSkip(FAILS);
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
