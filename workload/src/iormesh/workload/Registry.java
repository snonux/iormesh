package iormesh.workload;

import iormesh.workload.scenarios.FdLifecycle;
import iormesh.workload.scenarios.FileAttrs;
import iormesh.workload.scenarios.FileBasic;
import iormesh.workload.scenarios.FileLifecycle;
import iormesh.workload.scenarios.FileLinks;
import iormesh.workload.scenarios.Noop;
import iormesh.workload.scenarios.SyscallBurst;
import iormesh.workload.scenarios.TcpLoopback;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.TreeMap;

/** Maps scenario names to scenarios; the single place scenarios register. */
final class Registry {
    // TreeMap keeps names sorted, so usage output and self-test order are
    // deterministic.
    private final Map<String, Scenario> byName = new TreeMap<>();

    /** The registry with every built-in scenario. */
    static Registry builtin() {
        Registry r = new Registry();
        r.add(new FdLifecycle());
        r.add(new FileAttrs());
        r.add(new FileBasic());
        r.add(new FileLifecycle());
        r.add(new FileLinks());
        r.add(new Noop());
        r.add(new SyscallBurst());
        r.add(new TcpLoopback());
        return r;
    }

    void add(Scenario s) {
        if (byName.putIfAbsent(s.name(), s) != null) {
            throw new IllegalArgumentException("duplicate scenario: " + s.name());
        }
    }

    Optional<Scenario> find(String name) {
        return Optional.ofNullable(byName.get(name));
    }

    List<String> names() {
        return List.copyOf(byName.keySet());
    }

    List<Scenario> all() {
        return List.copyOf(byName.values());
    }
}
