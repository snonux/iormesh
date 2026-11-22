package iormesh.bridge;

import static iormesh.agent.AgentTests.check;

import iormesh.agent.AgentTests;
import java.lang.management.ManagementFactory;
import java.lang.management.ThreadMXBean;
import java.lang.reflect.Method;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CountDownLatch;

/**
 * The agent's cost scales with the I/O rate, not with the number of live
 * threads (task dj4): many parked virtual threads that each closed one
 * bracket cost the watermark tick nothing and the heap little. Measured
 * as a ratio and a difference against the same JVM without the agent's
 * part, so a loaded machine slows both sides alike.
 */
public final class WatermarkScaleTest {
    /** Parked virtual threads: enough that a per-thread scan shows. */
    private static final int THREADS = 50_000;
    /** The tick with them may cost at most this many times the one without. */
    private static final double MAX_TICK_RATIO = 20;
    /**
     * The agent's bytes per thread that closed one bracket: its state
     * and the thread's ThreadLocal map (about 380, the JVM's stack chunks
     * making the measure noisy by a few hundred: up to about 920 seen
     * on a loaded machine); 1,165 to 1,920 before task dj4, when every
     * thread preallocated 16 slots and the watermark's set held it for
     * good. The tick ratio above is the main guarantee; this bound only
     * catches a return to the old per-thread arrays.
     */
    private static final long MAX_BYTES_PER_THREAD = 1000;

    private WatermarkScaleTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("watermarks/parked virtual threads cost little",
                WatermarkScaleTest::parkedVirtualThreads));
    }

    /**
     * THREADS virtual threads park, wake, make one call and park again,
     * twice over: once the call is a plain readlink (the control), once
     * a bracket, whose own call is that readlink. The bracket run's heap
     * growth less the control's is the agent's state, and the watermark
     * tick (Watermarks.emit) must not grow with the parked threads — it
     * scans closing threads only. Skipped on 17–20.
     */
    static void parkedVirtualThreads() throws Exception {
        Method ofVirtual;
        try {
            ofVirtual = Thread.class.getMethod("ofVirtual");
        } catch (NoSuchMethodException e) {
            AgentTests.skip("no virtual threads before JDK 21");
            return;
        }
        Path dir = Files.createTempDirectory("iormesh-scale-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(5, false, false);
        Hooks.ring = r;
        try {
            Object builder = ofVirtual.invoke(null);
            double tickIdle = tickCost(r);
            double[] tickBusy = new double[1];
            long control = growth(builder, false, r, tickBusy);
            long bracket = growth(builder, true, r, tickBusy);
            long perThread = (bracket - control) / THREADS;
            double ratio = tickBusy[0] / Math.max(tickIdle, 1000);
            check(ratio <= MAX_TICK_RATIO && perThread <= MAX_BYTES_PER_THREAD, String.format(
                    "watermark tick %.0f ns with %d parked threads vs %.0f ns without (%.1fx), "
                            + "%d bytes per thread after one bracket",
                    tickBusy[0], THREADS, tickIdle, ratio, perThread));
        } finally {
            Hooks.ring = null;
            Hooks.configure(5, true, false);
            delete(dir);
        }
    }

    /**
     * Runs THREADS threads through Phases and returns how much the heap
     * grew from their first park to their last; with bracket, also the
     * watermark tick's cost while they are parked (tick[0]).
     */
    private static long growth(Object builder, boolean bracket, Ring r, double[] tick)
            throws Exception {
        Method start = Class.forName("java.lang.Thread$Builder")
                .getMethod("start", Runnable.class);
        Phases p = new Phases(bracket);
        List<Thread> threads = new ArrayList<>(THREADS);
        try {
            for (int i = 0; i < THREADS; i++) {
                threads.add((Thread) start.invoke(builder, (Runnable) p::body));
            }
            p.parked.await();
            long before = usedHeap();
            p.wake.countDown();
            p.called.await();
            long grown = usedHeap() - before;
            if (bracket) {
                tick[0] = tickCost(r);
            }
            return grown;
        } finally {
            p.release();
            for (Thread t : threads) {
                t.join();
            }
        }
    }

    /**
     * The threads' steps: park on wake, then make the call — a bracket,
     * or for the control just the readlink a bracket makes — and park
     * on done, the same park as the first, so the heap between the two
     * differs by what the call left behind.
     */
    private static final class Phases {
        final boolean bracket;
        final CountDownLatch parked = new CountDownLatch(THREADS);
        final CountDownLatch wake = new CountDownLatch(1);
        final CountDownLatch called = new CountDownLatch(THREADS);
        final CountDownLatch done = new CountDownLatch(1);

        Phases(boolean bracket) {
            this.bracket = bracket;
        }

        void body() {
            parked.countDown();
            awaitQuietly(wake);
            if (bracket) {
                Hooks.enter(3);
                Hooks.exit(3);
            } else {
                Brackets.lookupKtid();
            }
            called.countDown();
            awaitQuietly(done);
        }

        /** Lets every thread run to its end, wherever the test stopped. */
        void release() {
            wake.countDown();
            done.countDown();
        }
    }

    /**
     * CPU nanoseconds per watermark tick on this thread: the least of a
     * few batches, so a preemption inflates none of them all.
     */
    private static double tickCost(Ring r) {
        ThreadMXBean mx = ManagementFactory.getThreadMXBean();
        boolean cpu = mx.isCurrentThreadCpuTimeSupported();
        int n = 200;
        double best = Double.MAX_VALUE;
        for (int batch = 0; batch < 5; batch++) {
            long t0 = cpu ? mx.getCurrentThreadCpuTime() : System.nanoTime();
            for (int i = 0; i < n; i++) {
                Watermarks.emit(r);
            }
            long t1 = cpu ? mx.getCurrentThreadCpuTime() : System.nanoTime();
            best = Math.min(best, (t1 - t0) / (double) n);
        }
        return best;
    }

    /** Heap in use after full collections. */
    private static long usedHeap() {
        Runtime rt = Runtime.getRuntime();
        long used = Long.MAX_VALUE;
        for (int i = 0; i < 3; i++) {
            System.gc();
            used = Math.min(used, rt.totalMemory() - rt.freeMemory());
        }
        return used;
    }

    private static void awaitQuietly(CountDownLatch l) {
        try {
            l.await();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private static void delete(Path dir) throws java.io.IOException {
        try (var files = Files.list(dir)) {
            for (Path f : files.toList()) {
                Files.delete(f);
            }
        }
        Files.delete(dir);
    }
}
