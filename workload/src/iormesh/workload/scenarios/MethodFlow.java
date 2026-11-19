package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.security.MessageDigest;
import java.util.Arrays;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.CompletableFuture;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.TimeUnit;

/**
 * M3's method-flow scenario (PLAN.md M3 acceptance: "every declared I/O op
 * of M3's method-flow scenarios carries its declared causing method").
 * Solo, {@code --dir} its only option. Each I/O op is issued by its own
 * named method, so the golden can declare the causing method per op —
 * the innermost workload frame of the event's Java stack:
 *
 * <pre>
 *   main                Pipeline#descend ×DEPTH, then
 *                       Journal#create   openat(journal, O_WRONLY|O_CREAT|O_TRUNC)
 *                       Journal#append   write(4096)
 *                       Journal#seal     close
 *   method-flow-worker  Reader#readBack  openat(journal, O_RDONLY), read(4096), close
 *   method-flow-sink    Recorder#record  openat(digest, …CREAT|TRUNC), write(32), close
 *   main                Sweeper#sweep    unlink(journal), unlink(digest)
 * </pre>
 *
 * Deep stacks: the journal is written {@value #DEPTH} frames down a
 * recursion, far past the agent's 10-frame stack cut (M3 step 2), so the
 * outer frames are cut while the causing method, innermost, survives.
 * Async I/O: the read-back is handed to a worker thread and its result
 * to a sink thread (CompletableFuture stages on two single-thread
 * executors), so the events of one logical flow land on three threads,
 * each with its own Java thread name. Platform threads only: virtual
 * threads are deferred (PLAN.md), and each hand-off is joined before the
 * next step, so the per-thread order is fixed.
 */
public final class MethodFlow implements Scenario {
    static final String NAME = "method-flow";
    static final String JOURNAL = NAME + "-journal.dat";
    static final String DIGEST = NAME + "-digest.dat";
    static final String WORKER = NAME + "-worker";
    static final String SINK = NAME + "-sink";
    static final int DEPTH = 24;
    static final int SIZE = 4096;
    // Bounds the hand-offs so a stuck stage fails the run instead of
    // hanging it; far above the milliseconds a healthy run takes.
    private static final long WAIT_SECONDS = 30;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            Path journal = wd.file(JOURNAL);
            Path digest = wd.file(DIGEST);
            try {
                flow(journal, digest);
            } finally {
                Sweeper.sweep(journal, digest);
            }
        }
    }

    private static void flow(Path journal, Path digest) throws Exception {
        byte[] data = pattern();
        Pipeline.descend(DEPTH, journal, data);
        ExecutorService worker = single(WORKER);
        ExecutorService sink = single(SINK);
        try {
            CompletableFuture
                    .supplyAsync(() -> Reader.readBack(journal), worker)
                    .thenAcceptAsync(back -> Recorder.record(digest, data, back), sink)
                    .get(WAIT_SECONDS, TimeUnit.SECONDS);
        } finally {
            stop(worker);
            stop(sink);
        }
    }

    /** A one-thread executor whose platform thread carries name. */
    private static ExecutorService single(String name) {
        return Executors.newSingleThreadExecutor(r -> Thread.ofPlatform().name(name).unstarted(r));
    }

    private static void stop(ExecutorService ex) throws InterruptedException {
        ex.shutdownNow();
        if (!ex.awaitTermination(WAIT_SECONDS, TimeUnit.SECONDS)) {
            throw new IllegalStateException("executor did not stop");
        }
    }

    /** A fixed byte pattern: the same content, run after run. */
    private static byte[] pattern() {
        byte[] b = new byte[SIZE];
        for (int i = 0; i < SIZE; i++) {
            b[i] = (byte) (i * 7);
        }
        return b;
    }

    /** The deep stack: recursion down to the journal write. */
    static final class Pipeline {
        private Pipeline() {}

        static void descend(int depth, Path journal, byte[] data) throws IOException {
            if (depth > 0) {
                descend(depth - 1, journal, data);
                return;
            }
            FileOutputStream out = Journal.create(journal);
            try {
                Journal.append(out, data);
            } finally {
                Journal.seal(out);
            }
        }
    }

    /** One method per syscall, so each op has its own causing method. */
    static final class Journal {
        private Journal() {}

        static FileOutputStream create(Path journal) throws IOException {
            return new FileOutputStream(journal.toFile());
        }

        // One write(2): a regular file takes the whole array at once.
        static void append(FileOutputStream out, byte[] data) throws IOException {
            out.write(data);
        }

        static void seal(FileOutputStream out) throws IOException {
            out.close();
        }
    }

    /** The worker's stage: open, read and close in one method. */
    static final class Reader {
        private Reader() {}

        static byte[] readBack(Path journal) {
            byte[] back = new byte[SIZE];
            // readNBytes into a sized buffer stops at SIZE: one read(2),
            // no trailing read for EOF.
            try (FileInputStream in = new FileInputStream(journal.toFile())) {
                if (in.readNBytes(back, 0, SIZE) != SIZE) {
                    throw new IOException("short read of " + journal);
                }
            } catch (IOException e) {
                throw new java.io.UncheckedIOException(e);
            }
            return back;
        }
    }

    /** The sink's stage: checks the read-back and records its digest. */
    static final class Recorder {
        private Recorder() {}

        static void record(Path digest, byte[] want, byte[] back) {
            try {
                if (!Arrays.equals(want, back)) {
                    throw new IOException("read back differs from what was written");
                }
                // Hash first: the provider lookup loads classes, which
                // stays outside the digest file's open/write/close.
                byte[] sum = MessageDigest.getInstance("SHA-256").digest(back);
                try (FileOutputStream out = new FileOutputStream(digest.toFile())) {
                    out.write(sum);
                }
            } catch (Exception e) {
                throw new IllegalStateException(e);
            }
        }
    }

    /** Removes both files, also after a failure, so a reused --dir stays clean. */
    static final class Sweeper {
        private Sweeper() {}

        static void sweep(Path journal, Path digest) throws IOException {
            Files.deleteIfExists(journal);
            Files.deleteIfExists(digest);
        }
    }
}
