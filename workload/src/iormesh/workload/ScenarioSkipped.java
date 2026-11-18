package iormesh.workload;

/**
 * The host cannot run this scenario at all — e.g. tcp-ipv6 on a host
 * without an IPv6 loopback. It exits with its own code (3), so a harness
 * reports a skip with this reason instead of a failure (exit 1), and a
 * golden check never runs on a bracket the scenario never filled.
 */
public final class ScenarioSkipped extends Exception {
    private static final long serialVersionUID = 1L;

    public ScenarioSkipped(String reason, Throwable cause) {
        super(reason, cause);
    }
}
