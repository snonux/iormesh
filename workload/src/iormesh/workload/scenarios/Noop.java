package iormesh.workload.scenarios;

import iormesh.workload.Scenario;
import java.util.Map;

/**
 * Does nothing between its markers. It proves the framework itself — PID
 * announcement, gating, the marker bracket — and gives the harness an
 * empty bracket to check the normalizer against.
 */
public final class Noop implements Scenario {
    @Override
    public String name() {
        return "noop";
    }

    @Override
    public void run(String role, Map<String, String> opts) {
        // Intentionally empty: the bracket alone is the behavior.
    }
}
