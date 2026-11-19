package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.Set;

/**
 * The live-attach scenario (decision #2, task ge4): one JVM, started
 * without {@code -javaagent}, does the same file I/O twice — once before
 * the agent is loaded into it, once after. Solo; options {@code --dir}
 * and {@code --attach-gate FILE}: between the two halves the scenario
 * prints {@value #AWAITING} on stdout and waits for FILE to exist,
 * which the harness creates once `iormesh attach` loaded the agent and
 * the tracer probed the JVM's natives.
 * Without the option it runs straight through (the self-test).
 *
 * <pre>
 *   main  Unattached#write  openat(before, CREAT|TRUNC), write(4096), close
 *         (the agent is loaded here)
 *   main  Attached#write    openat(after, CREAT|TRUNC), write(4096), close
 *   main  Sweeper#sweep     unlink(before), unlink(after)
 * </pre>
 *
 * The golden declares the first half attribution none — a live-attached
 * agent misses everything before the attach, and no tracer may guess it —
 * and the rest java, with its causing method.
 */
public final class LiveAttach implements Scenario {
    static final String NAME = "live-attach";
    static final String BEFORE = NAME + "-before.dat";
    static final String AFTER = NAME + "-after.dat";
    static final String GATE_OPTION = "attach-gate";
    static final String AWAITING = "awaiting attach";
    static final int SIZE = 4096;
    // Bounds the wait for the attach so a harness that never opens the
    // gate fails the run instead of hanging it; far above an attach's
    // fraction of a second.
    private static final long GATE_TIMEOUT_MS = 60_000;
    private static final long GATE_POLL_MS = 10;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION, GATE_OPTION));
        byte[] data = new byte[SIZE];
        try (Workdir wd = Workdir.open(opts)) {
            Path before = wd.file(BEFORE);
            Path after = wd.file(AFTER);
            try {
                Unattached.write(before, data);
                awaitAttach(opts.get(GATE_OPTION));
                Attached.write(after, data);
            } finally {
                Sweeper.sweep(before, after);
            }
        }
    }

    /**
     * Announces the first half done ({@value #AWAITING} on stdout, the
     * line a harness waits for before it attaches) and waits for the gate
     * file. Files.exists is access(2), which the tracer's default set
     * leaves out, and the sleeps are no I/O: the wait adds nothing to the
     * trace on either side of the attach.
     */
    private static void awaitAttach(String gate) throws Exception {
        if (gate == null) {
            return;
        }
        System.out.println(AWAITING);
        System.out.flush();
        Path path = Path.of(gate);
        long deadline = System.nanoTime() + GATE_TIMEOUT_MS * 1_000_000;
        while (!Files.exists(path)) {
            if (System.nanoTime() > deadline) {
                throw new IOException("timed out waiting for the attach gate " + path);
            }
            Thread.sleep(GATE_POLL_MS);
        }
    }

    /** The first half: I/O no agent sees. */
    static final class Unattached {
        private Unattached() {}

        static void write(Path file, byte[] data) throws IOException {
            try (FileOutputStream out = new FileOutputStream(file.toFile())) {
                out.write(data);
            }
        }
    }

    /** The second half: the same I/O, under the live-loaded agent. */
    static final class Attached {
        private Attached() {}

        static void write(Path file, byte[] data) throws IOException {
            try (FileOutputStream out = new FileOutputStream(file.toFile())) {
                out.write(data);
            }
        }
    }

    /** Removes both files, also after a failure, so a reused --dir stays clean. */
    static final class Sweeper {
        private Sweeper() {}

        static void sweep(Path before, Path after) throws IOException {
            Files.deleteIfExists(before);
            Files.deleteIfExists(after);
        }
    }
}
