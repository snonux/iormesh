package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import iormesh.workload.UsageException;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CountDownLatch;

/**
 * S3's load scenario: the per-syscall worst case for the tracer — the
 * most events per unit of real work — so overhead, loss and volume are
 * measured where they are largest (docs/implementation.md, S3).
 *
 * <pre>
 *   syscall-burst [--ops N] [--threads T] [--dir D]
 * </pre>
 *
 * N ops (default 1,000,000) are split evenly over T platform threads
 * (default 1; the first N mod T threads do one more). Each thread owns
 * file "syscall-burst-&lt;i&gt;.dat" with two fds on it, and one op is
 * exactly two syscalls on the calling thread (strace, JDK 25):
 *
 * <pre>
 *   write(fdW, 1) = 1;  read(fdR, 1) = 1
 * </pre>
 *
 * Two fds rather than one O_RDWR fd: the reader's offset trails the
 * writer's by one byte, so every read finds the byte just written and
 * no lseek(2) is needed between them. The file grows by one byte per op
 * (1 MB per million). The bytes are a fixed pattern and are checked, so
 * a short or misdirected I/O fails the run instead of skewing a number.
 *
 * After the threads join, one stdout line reports the measurement:
 * {@code syscall-burst ops=N threads=T syscalls=2N elapsed_ns=E}, where
 * E spans only the loops (start latch to last join), not JVM startup,
 * file opens or class loading — scripts/perf-baseline.sh reads it.
 *
 * expect.json declares only the marker bracket: the event count scales
 * with --ops, and how T threads interleave is the scheduler's choice, so
 * no fixed golden can state the events; S3 asserts counts, not a golden.
 */
public final class SyscallBurst implements Scenario {
    static final String NAME = "syscall-burst";
    static final int DEFAULT_OPS = 1_000_000;
    // Enough for S3's 1/4/16-thread runs with headroom; a bound keeps a
    // typo like --threads 40000 from exhausting the host.
    static final int MAX_THREADS = 1024;
    // Small enough to finish in well under a second untraced.
    private static final int SELF_TEST_OPS = 1000;
    private static final int SELF_TEST_THREADS = 4;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of("ops", "threads", Workdir.OPTION));
        // Parse before any I/O, so a bad option is a usage error with an
        // empty bracket rather than a half-run burst.
        int ops = Options.intInRange(opts, "ops", DEFAULT_OPS, 1, Integer.MAX_VALUE);
        int threads = Options.intInRange(opts, "threads", 1, 1, MAX_THREADS);
        if (ops < threads) {
            throw new UsageException("--ops " + ops + " is less than --threads " + threads
                    + "; every thread needs at least one op");
        }
        try (Workdir wd = Workdir.open(opts)) {
            long elapsed = burst(wd, ops, threads);
            System.out.println(NAME + " ops=" + ops + " threads=" + threads
                    + " syscalls=" + 2L * ops + " elapsed_ns=" + elapsed);
            System.out.flush();
        }
    }

    @Override
    public void selfTest() throws Exception {
        run("solo", Map.of("ops", String.valueOf(SELF_TEST_OPS),
                "threads", String.valueOf(SELF_TEST_THREADS)));
    }

    /**
     * Runs the loops on threads platform threads and returns the loop
     * phase's wall time in nanoseconds. Every thread opens its files
     * before the start latch opens, so the opens stay out of the timing
     * and all loops contend at once — the load S3 wants to measure.
     */
    private static long burst(Workdir wd, int ops, int threads) throws Exception {
        CountDownLatch ready = new CountDownLatch(threads);
        CountDownLatch start = new CountDownLatch(1);
        List<Looper> loopers = new ArrayList<>();
        for (int i = 0; i < threads; i++) {
            int share = ops / threads + (i < ops % threads ? 1 : 0);
            loopers.add(new Looper(wd.file(NAME + "-" + i + ".dat"), share, ready, start));
        }
        List<Thread> started = new ArrayList<>();
        for (int i = 0; i < threads; i++) {
            started.add(Thread.ofPlatform().name(NAME + "-" + i).start(loopers.get(i)));
        }
        ready.await();
        long t0 = System.nanoTime();
        start.countDown();
        for (Thread t : started) {
            t.join();
        }
        long elapsed = System.nanoTime() - t0;
        for (Looper l : loopers) {
            l.rethrow();
        }
        return elapsed;
    }

    /** One thread's loop; a failure is kept and rethrown after the join. */
    private static final class Looper implements Runnable {
        private final Path file;
        private final int ops;
        private final CountDownLatch ready;
        private final CountDownLatch start;
        private volatile Exception failure;
        // Set once this thread counted ready down; only touched by it.
        private boolean signalled;

        Looper(Path file, int ops, CountDownLatch ready, CountDownLatch start) {
            this.file = file;
            this.ops = ops;
            this.ready = ready;
            this.start = start;
        }

        @Override
        public void run() {
            try {
                loop();
            } catch (Exception e) {
                failure = e;
            } finally {
                // A thread that failed before its countDown must not leave
                // the others (and main) waiting forever; one that counted
                // already must not count twice, or main could open the
                // start latch before another thread has opened its files.
                if (!signalled) {
                    ready.countDown();
                }
            }
        }

        private void loop() throws Exception {
            try (FileOutputStream out = new FileOutputStream(file.toFile());
                    FileInputStream in = new FileInputStream(file.toFile())) {
                signalled = true;
                ready.countDown();
                start.await();
                for (int i = 0; i < ops; i++) {
                    // write(int) and read() are one 1-byte syscall each:
                    // FileOutputStream/FileInputStream are unbuffered.
                    out.write(i);
                    int got = in.read();
                    if (got != (i & 0xff)) {
                        throw new IOException("op " + i + ": read " + got + ", wrote " + (i & 0xff)
                                + " in " + file);
                    }
                }
            } finally {
                Files.deleteIfExists(file);
            }
        }

        void rethrow() throws Exception {
            if (failure != null) {
                throw failure;
            }
        }
    }
}
