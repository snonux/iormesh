package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;
import java.util.Set;

/**
 * M2's restart: an app exits mid-session and comes back as a new process
 * with the same command line (PLAN.md M0 "Restart scenario", M2 "Target
 * spec", decision #23). Solo, {@code --dir} its only option.
 *
 * This process is only the launcher: it starts {@link RestartApp} as a
 * child JVM, waits for it to exit (and reaps it), then starts it again
 * with the identical argv — {@code java <flags> -cp <cp>
 * iormesh.workload.scenarios.RestartApp <dir>} — so nothing on the
 * command line tells the two incarnations apart; only the state file
 * the first one leaves behind does. Syscalls of each incarnation
 * (strace, JDK 25, 26 and 27-ea):
 *
 * <pre>
 *   incarnation 1: openat(state, O_RDONLY) = -ENOENT;
 *                  openat(state, O_WRONLY|O_CREAT|O_TRUNC) = F;
 *                  write(F, "incarn-1"); close(F)
 *   incarnation 2: openat(state, O_RDONLY) = F; read(F, 8); close(F);
 *                  openat(state, O_WRONLY|O_CREAT|O_TRUNC) = G;
 *                  write(G, "incarn-2"); close(G)
 * </pre>
 *
 * Each incarnation brackets itself with this scenario's markers. That is
 * what lets a pattern-bound session trace it: the session matches the
 * glob {@code *iormesh.workload.scenarios.RestartApp*} on exec, so it
 * never sees the launcher (whose argv is {@code -jar workload.jar
 * restart}) and the launcher's bracket; and it keeps both incarnations
 * through the normalizer's per-process cut without relying on
 * fork-following. The launcher's own events (spawn, wait4, the final
 * check and cleanup) are not declared. The golden declares both
 * incarnations as distinct processes with their own file work, linked by
 * {@code incarnation} edges from one app node.
 */
public final class Restart implements Scenario {
    static final String NAME = "restart";
    static final int INCARNATIONS = 2;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            Path state = wd.file(RestartApp.STATE_FILE);
            // A leftover state file would make the first incarnation a
            // "second" one and the declared ENOENT open would never come.
            if (Files.exists(state)) {
                throw new IOException("restart state exists already: " + state);
            }
            try {
                for (int i = 1; i <= INCARNATIONS; i++) {
                    incarnation(state.getParent());
                }
                verify(state);
            } finally {
                Files.deleteIfExists(state);
            }
        }
    }

    /**
     * Runs one incarnation to completion. inheritIO means no pipes: the
     * child prints nothing on stdout (markers go to stderr), so the
     * harness's stdout protocol stays the launcher's.
     */
    private static void incarnation(Path dir) throws Exception {
        Process p = new ProcessBuilder(ChildJvm.command(RestartApp.class,
                dir.toAbsolutePath().toString())).inheritIO().start();
        int code = p.waitFor();
        if (code != 0) {
            throw new IOException("restart incarnation exited " + code);
        }
    }

    /** The last incarnation's checkpoint proves each one saw the last. */
    private static void verify(Path state) throws IOException {
        String got = new String(Files.readAllBytes(state), StandardCharsets.US_ASCII);
        String want = RestartApp.checkpoint(INCARNATIONS);
        if (!got.equals(want)) {
            throw new IOException("restart state " + got + ", want " + want);
        }
    }
}
