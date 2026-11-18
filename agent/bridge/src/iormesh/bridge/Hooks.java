package iormesh.bridge;

import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicLongArray;

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
 * (FileInputStream.read(byte[]) can reach read(byte[],int,int)), and the
 * innermost bracket containing a syscall is the one the joiner keeps.
 * A bracket opened while the thread is already inside the bridge (the
 * stack walk loading a class, which reads a jar) is skipped, so the
 * agent never brackets its own work.
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

    private static volatile boolean walkStack = true;
    private static volatile AtomicLongArray counts = new AtomicLongArray(0);
    private static final AtomicLong SKIPS = new AtomicLong();
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
     * Called once by the agent before any JDK class is rewritten.
     * hookIds bounds the ids the table uses (ids are 1..hookIds-1);
     * stack=false records StackIds.NONE instead of walking (S1's cost
     * split, and an escape hatch while M3 engineers the walk down).
     */
    public static void configure(int hookIds, boolean stack) {
        counts = new AtomicLongArray(hookIds);
        walkStack = stack;
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
            StackIds.current();
        } finally {
            t.busy = false;
        }
    }

    public static void enter(int hook) {
        Brackets t = STATE.get();
        int d = t.depth++;
        if (d >= MAX_DEPTH) {
            return;           // too deep: exit sees d >= MAX_DEPTH and drops it
        }
        if (t.busy) {
            t.hooks[d] = SKIPPED;
            SKIPS.incrementAndGet();
            return;
        }
        t.busy = true;
        try {
            t.hooks[d] = hook;
            t.stacks[d] = walkStack ? StackIds.current() : StackIds.NONE;
            // Stamped last, so the walk is outside the bracket's time.
            t.enters[d] = System.nanoTime();
        } finally {
            t.busy = false;
        }
    }

    public static void exit(int hook) {
        Brackets t = STATE.get();
        if (t.depth == 0) {
            return;           // retransformed mid-call: no matching enter
        }
        int d = --t.depth;
        if (d >= MAX_DEPTH || t.hooks[d] != hook) {
            return;           // skipped or overflowed enter
        }
        AtomicLongArray c = counts;
        if (hook > 0 && hook < c.length()) {
            c.incrementAndGet(hook);
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
}
