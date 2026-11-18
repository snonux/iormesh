package iormesh.workload.scenarios;

import java.io.File;
import java.io.IOException;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.TimeUnit;

/**
 * The command line that starts a child JVM running one of the
 * workload's helper main classes (FdChild, PipeChild, SignalChild,
 * RestartApp): this JVM's own java and class path, so a scenario stays
 * JDK-only and needs nothing on PATH. Serial GC and C1-only keep the
 * short-lived child's thread count and start time down, and no perf
 * data means no hsperfdata file in /tmp; none of them changes a declared
 * syscall. Also the bounded wait for such a child to exit.
 */
final class ChildJvm {
    // Generous: a child JVM starts in well under a second, but a loaded
    // host can stretch that a lot; the bound only has to stop a hang.
    static final long EXIT_TIMEOUT_SECONDS = 60;

    private ChildJvm() {}

    /**
     * Waits for p to exit and returns its exit value. A child that hangs
     * would hang the scenario (and the harness bracket) with it, so past
     * the timeout it is killed and reaped, and the run fails.
     */
    static int await(Process p, String what) throws IOException, InterruptedException {
        if (!p.waitFor(EXIT_TIMEOUT_SECONDS, TimeUnit.SECONDS)) {
            kill(p);
            throw new IOException(what + " child did not exit within "
                    + EXIT_TIMEOUT_SECONDS + "s");
        }
        return p.exitValue();
    }

    /** SIGKILLs p unless it is gone already, and reaps it. */
    static void kill(Process p) throws InterruptedException {
        p.destroyForcibly();
        p.waitFor();
    }

    /** The command line for main with args appended after the class name. */
    static List<String> command(Class<?> main, String... args) {
        String java = System.getProperty("java.home") + File.separator + "bin"
                + File.separator + "java";
        List<String> cmd = new ArrayList<>(List.of(java, "-XX:+UseSerialGC",
                "-XX:TieredStopAtLevel=1", "-XX:-UsePerfData",
                "-cp", System.getProperty("java.class.path"), main.getName()));
        cmd.addAll(List.of(args));
        return List.copyOf(cmd);
    }
}
