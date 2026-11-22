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
 *
 * The same thread serves later readers (task 5j4): each tick it polls
 * the ring's reader epoch and, when a reader came after the first one,
 * resends the dictionary records that reader missed ({@link #resync}).
 * With watermark=0 there is no thread and so no resync either.
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
        emit(ring, false);
    }

    /**
     * emit, or with force a watermark even when no newer one is due: the
     * newer of now's and the last one, so watermarks still never go
     * backwards. A resync forces one to close its hook table (resend).
     */
    private static synchronized void emit(Ring ring, boolean force) {
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
        long v = force ? Math.max(w, last) : w;
        if ((force || w > last) && ring.watermark(v)) {
            last = v;
        }
    }

    /** The least time between two resyncs (Resync.gapNs's default). */
    static final long RESYNC_GAP_NS = 1_000_000_000L;

    /**
     * The watermark thread's resync state (task 5j4), touched by that
     * thread only: the reader epoch it last acted on and the ring's tail
     * read just before (what earlier readers consumed), whether a resync
     * was asked for but waits out gapNs since the last one (pending), and
     * whether the hook table still has to be resent (due: a row or the
     * THREAD was dropped last time).
     */
    static final class Resync {
        long seen;
        long seenTail;
        boolean pending;
        boolean due;
        long gapNs = RESYNC_GAP_NS;
        long lastNs = System.nanoTime() - RESYNC_GAP_NS;
    }

    /**
     * Resends the dictionary when a reader came after an earlier one or
     * a repeated agent load asked for it (Hooks.requestResync): an earlier
     * reader consumed the HOOK, THREAD, STACK and CLASS records written so
     * far. The first reader needs none — everything it did not consume is
     * still in the ring. An earlier reader is one whose open moved the
     * epoch past 1, or one that consumed records without moving it (a
     * tracer older than 5j4: the tail had moved before the epoch did).
     * The tail is read before the epoch, so a new reader's own drains are
     * never taken for an earlier one's. A resync bumps the generation,
     * which makes each thread's THREAD and each class's CLASS due again
     * at their next bracket or stack, forgets the known stacks so each is
     * written again at its next sighting, and writes the hook table again
     * (resend). App threads pay only what they paid at their first
     * bracket; nothing here waits on them. Bounded: at most one resync per
     * gapNs (a second), however fast readers come; one asked for sooner
     * waits for the gap, never dropped.
     */
    static void resync(Ring ring, Resync s) {
        long tail = ring.consumed();
        long epoch = ring.readerEpoch();
        s.pending |= Hooks.resyncRequested();
        if (epoch != s.seen) {
            s.pending |= epoch > 1 || s.seenTail > 0;
            s.seen = epoch;
        }
        s.seenTail = tail;
        long now = System.nanoTime();
        if (s.pending && now - s.lastNs >= s.gapNs) {
            s.pending = false;
            s.lastNs = now;
            Hooks.generation = (Hooks.generation + 1) & Integer.MAX_VALUE;
            StackIds.forget();
            s.due = true;
        }
        if (s.due) {
            s.due = !resend(ring);
        }
    }

    /**
     * Writes the hook table, this thread's THREAD and then a watermark,
     * under emit's lock, so no other watermark lands among the rows: the
     * tracer takes the first WATERMARK after a resent HOOK row as the end
     * of the table (internal/correlate, jvm.tableRead) before it probes
     * the natives the rows name. App threads' records may interleave with
     * the rows; a watermark never does. False if a row or the THREAD was
     * dropped: the next tick tries again (rows repeated are harmless).
     */
    private static synchronized boolean resend(Ring ring) {
        boolean ok = Hooks.writeHooks(ring) & describeSelf(ring);
        if (ok) {
            emit(ring, true);
        }
        return ok;
    }

    /** Writes the watermark thread's THREAD record; false if dropped. */
    private static boolean describeSelf(Ring ring) {
        return ring.thread(Thread.currentThread().getId(), Hooks.state().kernelTid(), 0,
                THREAD_NAME);
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
        while (self.described == null) {
            if (describeSelf(ring)) {
                self.described = THREAD_NAME;
            } else if (!pause(intervalMs)) {
                return;
            }
        }
        Resync s = new Resync();
        while (pause(intervalMs)) {
            tick(ring, s);
        }
    }

    /**
     * One tick: a resync if a new reader asked for one, then the
     * watermark — but none while a resend is still due (review of 5j4).
     * The tracer takes the first WATERMARK after resent HOOK rows as the
     * table's end and probes the natives of the rows it has: a watermark
     * after a resend whose rows the full ring partly dropped would close
     * a partial table, and the full one resent later would come too late.
     * So watermarks wait until the resend lands whole (resend closes it
     * itself); meanwhile the tracer holds the JVM's syscalls up to its
     * join timeout, as it would with watermarks dropped by the same full
     * ring. A resync that throws (it must not) costs that resync, never
     * the watermarks.
     */
    static void tick(Ring ring, Resync s) {
        try {
            resync(ring, s);
        } catch (Throwable e) {
            s.due = false;
        }
        if (!s.due) {
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
