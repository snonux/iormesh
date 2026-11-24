package iormesh.workload;

import iormesh.workload.scenarios.ArgvSecret;
import iormesh.workload.scenarios.FdLifecycle;
import iormesh.workload.scenarios.FileAttrs;
import iormesh.workload.scenarios.FileBasic;
import iormesh.workload.scenarios.FileLifecycle;
import iormesh.workload.scenarios.FileLinks;
import iormesh.workload.scenarios.HttpScenario;
import iormesh.workload.scenarios.Ingress;
import iormesh.workload.scenarios.LiveAttach;
import iormesh.workload.scenarios.MethodFlow;
import iormesh.workload.scenarios.Microservices;
import iormesh.workload.scenarios.NonJavaPeer;
import iormesh.workload.scenarios.Noop;
import iormesh.workload.scenarios.PipeIpc;
import iormesh.workload.scenarios.Restart;
import iormesh.workload.scenarios.Signals;
import iormesh.workload.scenarios.SyscallBurst;
import iormesh.workload.scenarios.TcpFamily;
import iormesh.workload.scenarios.TcpHosts;
import iormesh.workload.scenarios.TcpLoopback;
import iormesh.workload.scenarios.UdpLoopback;
import iormesh.workload.scenarios.UnixSocket;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.ServiceLoader;
import java.util.TreeMap;

/** Maps scenario names to scenarios; the single place scenarios register. */
final class Registry {
    // TreeMap keeps names sorted, so usage output and self-test order are
    // deterministic.
    private final Map<String, Scenario> byName = new TreeMap<>();

    /**
     * The registry with every built-in scenario, plus any a separate
     * module on the class path provides (META-INF/services/
     * iormesh.workload.Scenario). That is how the OTEL flavor
     * (workload/otel, docs/milestones.md M0) joins without the core jar
     * depending on OpenTelemetry (decision #5): workload.jar alone finds no
     * providers.
     */
    static Registry builtin() {
        Registry r = new Registry();
        r.add(new ArgvSecret());
        r.add(new FdLifecycle());
        r.add(new FileAttrs());
        r.add(new FileBasic());
        r.add(new FileLifecycle());
        r.add(new FileLinks());
        r.add(HttpScenario.async());
        r.add(HttpScenario.plain());
        r.add(HttpScenario.redaction());
        r.add(HttpScenario.tls());
        r.add(new Ingress());
        r.add(new LiveAttach());
        r.add(new MethodFlow());
        r.add(new Microservices());
        r.add(new NonJavaPeer());
        r.add(new Noop());
        r.add(new PipeIpc());
        r.add(new Restart());
        r.add(new Signals());
        r.add(new SyscallBurst());
        r.add(TcpFamily.ipv4());
        r.add(TcpFamily.ipv6());
        r.add(TcpHosts.nat());
        r.add(TcpHosts.twoHost());
        r.add(new TcpLoopback());
        r.add(new UdpLoopback());
        r.add(new UnixSocket());
        for (Scenario s : ServiceLoader.load(Scenario.class)) {
            r.add(s);
        }
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
