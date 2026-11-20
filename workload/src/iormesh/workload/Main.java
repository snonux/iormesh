package iormesh.workload;

import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeSet;

/**
 * Entry point of the traceable workload (PLAN.md, M0). Usage:
 *
 * <pre>
 *   java -jar workload.jar &lt;scenario&gt; [--role R] [--key value ...]
 *   java -jar workload.jar --self-test [scenario ...]
 *   java -jar workload.jar --expect &lt;scenario&gt;
 *   java -jar workload.jar --list
 * </pre>
 *
 * Exit codes: 0 ok, 1 the scenario failed, 2 usage error, 3 skipped
 * (the host lacks what the scenario needs, see {@link ScenarioSkipped}).
 *
 * Gating mirrors ior's cmd/ioworkload so the tracer is attached before
 * the first scenario syscall: the PID is the first stdout line, then the
 * process waits for $IORMESH_WORKLOAD_STARTUP_FILE (if set) and, after
 * the scenario, for $IORMESH_WORKLOAD_HOLD_FILE (if set).
 */
public final class Main {
    static final String STARTUP_FILE_ENV = "IORMESH_WORKLOAD_STARTUP_FILE";
    static final String HOLD_FILE_ENV = "IORMESH_WORKLOAD_HOLD_FILE";
    static final Duration STARTUP_TIMEOUT = Duration.ofSeconds(30);
    // Only a safety net so a forgotten hold never leaks the process; it
    // stays well above any -duration a test expects the target to outlive.
    static final Duration HOLD_TIMEOUT = Duration.ofSeconds(120);

    static final int EXIT_FAILURE = 1;
    static final int EXIT_USAGE = 2;
    // The host lacks what the scenario needs (ScenarioSkipped): neither
    // a pass nor a failure, so a harness turns it into a test skip.
    static final int EXIT_SKIP = 3;

    private Main() {}

    public static void main(String[] args) {
        Registry registry = Registry.builtin();
        int code;
        try {
            code = dispatch(registry, args);
        } catch (UsageException e) {
            System.err.println(e.getMessage());
            usage(registry);
            code = EXIT_USAGE;
        } catch (ScenarioSkipped e) {
            System.err.println("skipped: " + e.getMessage());
            code = EXIT_SKIP;
        } catch (Exception e) {
            System.err.println("workload failed: " + e);
            code = EXIT_FAILURE;
        }
        System.exit(code);
    }

    private static int dispatch(Registry registry, String[] args) throws Exception {
        if (args.length == 0) {
            throw new UsageException("missing scenario name");
        }
        switch (args[0]) {
            case "--self-test":
                return SelfTest.run(selfTestSet(registry, args));
            case "--list":
                requireArgCount(args, 1);
                registry.names().forEach(System.out::println);
                return 0;
            case "--expect":
                return printExpectation(registry, args);
            default:
                return runScenario(registry, args);
        }
    }

    /**
     * The scenarios --self-test runs: all of them, or only those named
     * after it — a separate module's harness (the OTEL flavor's jar)
     * self-tests its own scenario without running the core ones again.
     */
    private static List<Scenario> selfTestSet(Registry registry, String[] args)
            throws UsageException {
        if (args.length == 1) {
            return registry.all();
        }
        List<Scenario> picked = new ArrayList<>();
        for (int i = 1; i < args.length; i++) {
            picked.add(lookup(registry, args[i]));
        }
        return picked;
    }

    private static void requireArgCount(String[] args, int n) throws UsageException {
        if (args.length != n) {
            throw new UsageException("unexpected arguments after " + args[0]);
        }
    }

    private static int runScenario(Registry registry, String[] args) throws Exception {
        Scenario scenario = lookup(registry, args[0]);
        Map<String, String> opts = parseOpts(args);
        String role = opts.getOrDefault("role", "solo");
        // The scenario names its roles (Scenario.roles): client, server
        // and solo for most, a service role each for microservices.
        if (!scenario.roles().contains(role)) {
            throw new UsageException("unknown role: " + role + "; " + scenario.name()
                    + " knows " + new TreeSet<>(scenario.roles()));
        }
        // The harness reads the PID from the first stdout line and attaches
        // the tracer to it before releasing the startup gate. Note: Java's
        // main runs on a JVM-created thread, so scenario syscalls carry a
        // TID different from the PID (unlike ior's ioworkload, which pins
        // TID == PID); the tracer must follow every thread of the PID.
        System.out.println(ProcessHandle.current().pid());
        System.out.flush();
        waitForEnvFile(STARTUP_FILE_ENV, STARTUP_TIMEOUT);
        bracketed(scenario, role, opts);
        waitForEnvFile(HOLD_FILE_ENV, HOLD_TIMEOUT);
        return 0;
    }

    /** Runs one scenario inside its begin/end marker bracket. */
    static void bracketed(Scenario scenario, String role, Map<String, String> opts)
            throws Exception {
        bracketed(scenario.name(), () -> scenario.run(role, opts));
    }

    /**
     * Brackets body with the scenario's markers. A body that throws gets
     * no end marker on purpose: a failed scenario has no complete bracket,
     * so a golden check can never pass on half a run.
     */
    static void bracketed(String name, Body body) throws Exception {
        Marker.begin(name);
        body.run();
        Marker.end(name);
    }

    /** The scenario work between the markers. */
    @FunctionalInterface
    interface Body {
        void run() throws Exception;
    }

    private static Scenario lookup(Registry registry, String name) throws UsageException {
        return registry.find(name)
                .orElseThrow(() -> new UsageException("unknown scenario: " + name));
    }

    /** Parses {@code --key value} pairs after the scenario name. */
    static Map<String, String> parseOpts(String[] args) throws UsageException {
        Map<String, String> opts = new HashMap<>();
        for (int i = 1; i < args.length; i += 2) {
            String key = args[i];
            if (!key.startsWith("--") || key.length() == 2) {
                throw new UsageException("expected --key, got: " + key);
            }
            if (i + 1 >= args.length) {
                throw new UsageException("missing value for " + key);
            }
            if (opts.put(key.substring(2), args[i + 1]) != null) {
                throw new UsageException("duplicate option: " + key);
            }
        }
        return opts;
    }

    /** Prints a scenario's declared expectations, bundled in the jar. */
    private static int printExpectation(Registry registry, String[] args) throws Exception {
        if (args.length != 2) {
            throw new UsageException("usage: --expect <scenario>");
        }
        Scenario scenario = lookup(registry, args[1]);
        String resource = "/scenarios/" + scenario.name() + "/expect.json";
        try (InputStream in = Main.class.getResourceAsStream(resource)) {
            if (in == null) {
                throw new IOException("no expectation bundled: " + resource);
            }
            System.out.print(new String(in.readAllBytes(), StandardCharsets.UTF_8));
        }
        return 0;
    }

    /**
     * Waits for the file named by env var; returns at once if it is unset.
     * Unlike ior's ioworkload there is no fallback startup delay: an unset
     * gate means "no tracer to wait for" (standalone runs, self-test), and
     * the harness always sets it, so nothing races silently on a timer.
     */
    static void waitForEnvFile(String env, Duration timeout) throws Exception {
        String path = System.getenv(env);
        if (path == null || path.isEmpty()) {
            return;
        }
        waitForFile(Path.of(path), timeout);
    }

    static void waitForFile(Path path, Duration timeout) throws Exception {
        long deadline = System.nanoTime() + timeout.toNanos();
        while (!Files.exists(path)) {
            if (System.nanoTime() > deadline) {
                throw new IOException("timed out after " + timeout + " waiting for " + path);
            }
            Thread.sleep(10);
        }
    }

    private static void usage(Registry registry) {
        System.err.println("usage: workload.jar <scenario> [--role R]"
                + " [--key value ...] | --self-test [scenario ...] | --list"
                + " | --expect <scenario>");
        System.err.println("scenarios: " + String.join(", ", registry.names()));
    }
}
