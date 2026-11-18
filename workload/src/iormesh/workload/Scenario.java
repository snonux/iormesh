package iormesh.workload;

import java.util.Map;

/**
 * One deterministic, declared behavior. Its syscalls between the begin
 * and end markers are what the scenario's expect.json declares; nothing
 * the JVM does outside that bracket is ever asserted on (PLAN.md, M0).
 */
public interface Scenario {
    /** Registry key and marker name, e.g. "file-basic". */
    String name();

    /**
     * Runs the scenario once.
     *
     * @param role "client", "server" or "solo"; socket scenarios run as
     *             two processes, everything else as "solo"
     * @param opts scenario options from the command line ({@code --key
     *             value}), e.g. a port
     */
    void run(String role, Map<String, String> opts) throws Exception;

    /**
     * Runs the scenario without a tracer for {@code --self-test}. Solo
     * scenarios just run; multi-role scenarios override this to drive
     * every role inside one process, so the self-test needs no second
     * process and no fixed port.
     */
    default void selfTest() throws Exception {
        run("solo", Map.of());
    }
}
