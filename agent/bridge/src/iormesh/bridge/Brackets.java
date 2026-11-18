package iormesh.bridge;

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

    final Thread owner = Thread.currentThread();
    boolean busy;
    int depth;
    final int[] hooks = new int[Hooks.MAX_DEPTH];
    final long[] enters = new long[Hooks.MAX_DEPTH];
    final long[] stacks = new long[Hooks.MAX_DEPTH];
    int ktid = UNKNOWN_KTID;
    /** Whether this thread's THREAD record reached the ring. */
    boolean described;
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

    private static int lookupKtid() {
        try {
            return Integer.parseInt(Files.readSymbolicLink(Path.of("/proc/thread-self"))
                    .getFileName().toString());
        } catch (Exception e) {
            return 0;
        }
    }
}
