package iormesh.workload;

import java.util.Map;
import java.util.Set;

/**
 * One deterministic, declared behavior. Its syscalls between the begin
 * and end markers are what the scenario's expect.json declares; nothing
 * the JVM does outside that bracket is ever asserted on (docs/milestones.md,
 * M0).
 */
public interface Scenario {
    /** Registry key and marker name, e.g. "file-basic". */
    String name();

    /**
     * Runs the scenario once.
     *
     * @param role one of {@link #roles()}: "client", "server" or "solo"
     *             for most scenarios (socket scenarios run as two
     *             processes, everything else as "solo"), a service role
     *             for the microservices scenario
     * @param opts scenario options from the command line ({@code --key
     *             value}), e.g. a port
     */
    void run(String role, Map<String, String> opts) throws Exception;

    /**
     * The roles {@code --role} may name. Most scenarios are solo or a
     * client/server pair; the microservices scenario plays one service
     * role per process (gateway, orders, …), so it names its own. Main
     * rejects any other role with the usage code before the bracket; the
     * scenario still narrows the set further where it needs to.
     */
    default Set<String> roles() {
        return Set.of("client", "server", "solo");
    }

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
