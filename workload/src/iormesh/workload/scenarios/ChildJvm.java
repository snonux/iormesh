package iormesh.workload.scenarios;

import java.io.File;
import java.util.ArrayList;
import java.util.List;

/**
 * The command line that starts a child JVM running one of the
 * workload's helper main classes (PipeChild, SignalChild, RestartApp):
 * this JVM's own java and class path, so a scenario stays JDK-only and needs
 * nothing on PATH. Serial GC and C1-only keep the short-lived child's
 * thread count and start time down, and no perf data means no
 * hsperfdata file in /tmp; none of them changes a declared syscall.
 * (FdLifecycle builds the same line inline; it predates this helper.)
 */
final class ChildJvm {
    private ChildJvm() {}

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
