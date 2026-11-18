package iormesh.bridge;

import java.lang.invoke.VarHandle;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * WATERMARK records: the promise "every bracket that exited before this
 * time is in the ring ahead of this record, or counted as dropped".
 * Brackets are written at exit, so the joiner holds a syscall until a
 * watermark has passed it (docs/implementation.md M3 step 4); without
 * watermarks it would have to guess how long to wait.
 *
 * Why the promise holds: a closing bracket sets its thread's
 * {@code closing} to its enter stamp before stamping its exit, and
 * clears it after publishing. A watermark reads the clock first, then
 * scans every thread: a thread seen idle either published already (so
 * its record precedes the watermark's reservation) or will stamp its
 * exit after the scan, hence after the clock read; a thread seen
 * closing lowers the watermark to its enter stamp, which no exit
 * precedes. Watermarks never go backwards. Both sides fence between
 * their volatile access and their clock read (Dekker): the JMM does not
 * order System.nanoTime() against volatiles by itself.
 */
final class Watermarks {
    /** The thread's name: the tracer recognises its syscalls as the agent's. */
    static final String THREAD_NAME = "iormesh-agent-watermark";
    /** Every so many registrations, dead threads are pruned (see register). */
    private static final int PRUNE_EVERY = 256;
    private static final Set<Brackets> LIVE = ConcurrentHashMap.newKeySet();
    private static final AtomicInteger REGISTERED = new AtomicInteger();
    private static long last = Long.MIN_VALUE;

    private Watermarks() {}

    /**
     * Tracks a thread's state for the scan; without a ring there are no
     * watermarks and nothing to track. Dead threads are pruned by every
     * scan, and here too, so an app that churns threads cannot grow the
     * set when the timer is off (watermark=0).
     */
    static void register(Brackets b) {
        if (Hooks.ring == null) {
            return;
        }
        LIVE.add(b);
        if (REGISTERED.incrementAndGet() % PRUNE_EVERY == 0) {
            LIVE.removeIf(x -> x.closing == Brackets.IDLE && !x.owner.isAlive());
        }
    }

    /**
     * Writes a watermark now if it is newer than the last one; the
     * timer thread and the agent's shutdown hook both call this.
     */
    static synchronized void emit(Ring ring) {
        long now = System.nanoTime();
        // Orders the clock read before the scan's volatile loads, which
        // the JMM alone does not; pairs with the fence in Hooks.close.
        VarHandle.fullFence();
        long w = now;
        for (Brackets b : LIVE) {
            long c = b.closing;
            if (c < w) {
                w = c;
            } else if (c == Brackets.IDLE && !b.owner.isAlive()) {
                LIVE.remove(b);  // dead thread: it can write nothing more
            }
        }
        if (w > last && ring.watermark(w)) {
            last = w;
        }
    }

    /**
     * Starts the daemon thread that emits a watermark every intervalMs.
     * Its sleeps are the agent's syscalls on the agent's own thread,
     * which it names in a THREAD record (by its name) so the tracer can
     * keep them out of the target's story; it never opens a bracket.
     */
    static void start(Ring ring, long intervalMs) {
        Thread t = new Thread(() -> run(ring, intervalMs), THREAD_NAME);
        t.setDaemon(true);
        t.start();
    }

    private static void run(Ring ring, long intervalMs) {
        Brackets self = Hooks.state();
        self.busy = true;  // whatever it calls is never bracketed
        while (!self.described) {
            self.described = ring.thread(Thread.currentThread().getId(), self.kernelTid(),
                    THREAD_NAME);
            if (!self.described && !pause(intervalMs)) {
                return;
            }
        }
        while (pause(intervalMs)) {
            emit(ring);
        }
    }

    /** Sleeps; false once interrupted, which ends the thread. */
    private static boolean pause(long ms) {
        try {
            Thread.sleep(ms);
            return true;
        } catch (InterruptedException e) {
            return false;
        }
    }
}
