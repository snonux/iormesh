package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.io.InputStream;
import java.util.Map;
import java.util.Set;

/**
 * M2's signals: this process signals two child JVMs — SIGTERM via
 * Process.destroy(), then SIGKILL via Process.destroyForcibly() — the
 * supervisor's "who touched the process" case. Solo, no options.
 * Syscalls (strace, JDK 25, 26 and 27-ea):
 *
 * <pre>
 *   per child: spawn SignalChild (stdin and stdout pipes); read its
 *              1-byte ready message; close the stdout pipe; close the
 *              stdin pipe; read /proc/&lt;child&gt;/stat (the JDK checks
 *              the PID still names its child — PID reuse — before
 *              signalling); kill(child, SIGTERM | SIGKILL); the reaper
 *              thread's wait4 reaps the child
 * </pre>
 *
 * Both pipes are closed on this thread before the signal, on purpose:
 * left open, destroy() would close stdin after kill(2), but the JDK's
 * reaper thread closes it too when it sees the child die — whichever
 * runs first issues the close(2), so its thread would vary run to run.
 *
 * The child exits 143 (SIGTERM: the JVM's own handler runs shutdown
 * and exits 128+15) or is killed by SIGKILL (exit value 137); anything
 * else fails the run. The ready byte makes the signal land on a JVM
 * that is up, its SIGTERM handler installed, not one still starting —
 * otherwise the exit value would depend on how far the child got.
 *
 * Limits of a JDK-only workload: Java can send only SIGTERM and SIGKILL
 * (destroy, destroyForcibly), only to its own children, and cannot
 * signal itself (ProcessHandle.current().destroy() is refused); it has
 * no public API to install a handler (sun.misc.Signal is internal and
 * fails -Werror). So the declared sender side is kill(2) from this
 * process; delivery to the child (signal_generate/signal_deliver, with
 * the sender's PID) is what the tracer adds, and its event shape is
 * for M2 to define — the golden declares only the kill(2) events.
 */
public final class Signals implements Scenario {
    static final String NAME = "signals";
    // Exit values a Java Process reports for a child killed by, or
    // exiting on, a signal: 128 + the signal number.
    static final int SIGTERM_EXIT = 128 + 15;
    static final int SIGKILL_EXIT = 128 + 9;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of());
        signal(false, SIGTERM_EXIT);
        signal(true, SIGKILL_EXIT);
    }

    /** Starts a SignalChild, waits until it is ready, then signals it. */
    private static void signal(boolean force, int want) throws Exception {
        Process p = new ProcessBuilder(ChildJvm.command(SignalChild.class))
                .redirectError(ProcessBuilder.Redirect.INHERIT)
                .start();
        try {
            awaitReady(p);
            // Before the signal: see the class comment. The child takes
            // the EOF as no cue to exit (SignalChild), so the signal
            // still decides its exit value.
            p.getOutputStream().close();
            if (force) {
                p.destroyForcibly();
            } else {
                p.destroy();
            }
            int code = ChildJvm.await(p, NAME);
            if (code != want) {
                throw new IOException("signals child exited " + code + ", want " + want);
            }
        } finally {
            // On failure, no child may outlive the scenario. On success
            // the child is already reaped and the JDK skips kill(2) and
            // the (already done) pipe closes, so this adds no syscall.
            ChildJvm.kill(p);
        }
    }

    /**
     * Reads the ready byte, then closes the stdout pipe at once: the
     * child writes nothing more, and a closed read end leaves the JDK's
     * reaper nothing to drain on its own thread when the child dies.
     */
    private static void awaitReady(Process p) throws IOException {
        try (InputStream in = p.getInputStream()) {
            int b = in.read();
            if (b != SignalChild.READY) {
                throw new IOException("signals child not ready: " + b);
            }
        }
    }
}
