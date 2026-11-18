package iormesh.bridge;

import java.util.List;
import java.util.concurrent.ConcurrentHashMap;

/**
 * The Java stack at an I/O boundary, as a 64-bit id (decision #6: the
 * agent walks the stack with StackWalker at the hook, nothing else).
 *
 * The id hashes the caller frames, so a call site that does I/O in a
 * loop costs one walk and one hash per call but stores its frames once:
 * the first time an id is seen its frames are kept for {@link #frames}
 * and written to the ring as a STACK record (decision #27). The id is
 * visible to other threads before its STACK is published, so a STACK
 * may follow brackets that name it; the joiner resolves stacks by id. A
 * STACK the ring dropped is forgotten again, so a sighting after a 1 ms
 * back-off (Hooks.RETRY_NS) retries it;
 * past MAX_KNOWN distinct stacks no more are kept or written, and each
 * unkept sighting counts as a dropped STACK — the bracket keeps its id,
 * the trace's loss accounting says why the frames are missing.
 *
 * Lives in the bridge (bootstrap path) because {@link Hooks#enter} calls
 * it from rewritten java.base code, which cannot see the agent's own
 * class loader. The walk is the agent's dominant cost (S1: ≈8 µs of a
 * ≈8.3 µs bracket on a loaded host), which is why the depth is capped.
 */
public final class StackIds {
    /** S1's depth: enough to reach the application frame above the JDK's. */
    static final int MAX_FRAMES = 10;
    /** Bounds the frame table if an app has unboundedly many call sites. */
    static final int MAX_KNOWN = 1 << 16;
    /** 0 means "no stack" (stack walking off, or a skipped bracket). */
    public static final long NONE = 0;
    private static final long FNV_PRIME = 0x100000001b3L;

    private static final StackWalker WALKER = StackWalker.getInstance();
    private static final String OWN_PACKAGE = "iormesh.bridge.";
    private static final ConcurrentHashMap<Long, String[]> KNOWN = new ConcurrentHashMap<>();
    /** After a dropped STACK, no new stack is written before this nanoTime. */
    private static volatile long retryAt;

    private StackIds() {}

    /**
     * Returns the id of the current stack below the bridge: the first
     * frame is the hooked JDK method, then its callers. Frames of this
     * package are dropped by name rather than by a fixed skip count, so
     * the id does not depend on how the bridge calls this.
     */
    public static long current() {
        List<StackWalker.StackFrame> frames = WALKER.walk(s -> s
                .dropWhile(f -> f.getClassName().startsWith(OWN_PACKAGE))
                .limit(MAX_FRAMES)
                .toList());
        long id = hash(frames);
        if (!KNOWN.containsKey(id)) {
            remember(id, frames);
        }
        return id;
    }

    /**
     * Returns the frames recorded for id, innermost first, as
     * {@code class#method(file:line)}, or null if the id is unknown.
     */
    public static String[] frames(long id) {
        String[] f = KNOWN.get(id);
        return f == null ? null : f.clone();
    }

    /** The number of distinct stacks seen so far. */
    public static int size() {
        return KNOWN.size();
    }

    /**
     * 64-bit FNV-1a over each frame's class and method name (char by
     * char, not String.hashCode, which would cap each name at 32 bits)
     * and bytecode index; the bci tells two calls in one method apart.
     * A collision is not detected: two stacks would share an id (and
     * the first one's frames). With n distinct stacks the odds are about
     * n²/2⁶⁵ — negligible for the call-site counts the agent sees.
     */
    private static long hash(List<StackWalker.StackFrame> frames) {
        long h = 0xcbf29ce484222325L;
        for (StackWalker.StackFrame f : frames) {
            h = mix(h, f.getClassName());
            h = mix(h, f.getMethodName());
            h = (h ^ f.getByteCodeIndex()) * FNV_PRIME;
        }
        return h == NONE ? 1 : h;
    }

    private static long mix(long h, String s) {
        for (int i = 0; i < s.length(); i++) {
            h = (h ^ s.charAt(i)) * FNV_PRIME;
        }
        // A separator, so "ab"+"c" and "a"+"bc" differ.
        return (h ^ 0xffff) * FNV_PRIME;
    }

    /**
     * Keeps a new stack's frames and writes its STACK record; only the
     * thread whose putIfAbsent won writes it, so a stack is written once.
     */
    private static void remember(long id, List<StackWalker.StackFrame> frames) {
        Ring r = Hooks.ring;
        if (KNOWN.size() >= MAX_KNOWN) {
            if (r != null) {
                r.lost(Ring.STACK);
            }
            return;
        }
        if (r != null && System.nanoTime() < retryAt) {
            return;  // backing off after a drop: no allocation per sighting
        }
        String[] described = describe(frames);
        if (KNOWN.putIfAbsent(id, described) == null && r != null && !r.stack(id, described)) {
            KNOWN.remove(id);
            retryAt = System.nanoTime() + Hooks.RETRY_NS;
        }
    }

    private static String[] describe(List<StackWalker.StackFrame> frames) {
        String[] out = new String[frames.size()];
        for (int i = 0; i < out.length; i++) {
            StackWalker.StackFrame f = frames.get(i);
            String file = f.getFileName() == null ? "Unknown Source" : f.getFileName();
            out[i] = f.getClassName() + "#" + f.getMethodName()
                    + "(" + file + ":" + f.getLineNumber() + ")";
        }
        return out;
    }
}
