package iormesh.agent;

import java.io.IOException;
import java.io.PrintStream;
import java.lang.instrument.Instrumentation;
import java.util.Set;

/**
 * The iormesh Java agent (M3): brackets the JDK's I/O boundary methods
 * so the tracer can attribute each syscall to the Java stack and thread
 * that issued it (PLAN.md "M3 — Java agent", S1's design).
 *
 * premain ({@code -javaagent}, the primary path) and agentmain (live
 * attach, decision #2) run the same install: the JDK's I/O classes are
 * loaded before either runs, so both rewrite them by retransformation.
 *
 * The agent must never take the target down: any failure while
 * installing is reported on stderr and leaves the JVM as it was (or with
 * the hooks installed so far, which are harmless on their own).
 */
public final class Agent {
    private Agent() {}

    public static void premain(String args, Instrumentation inst) {
        start(args, inst);
    }

    public static void agentmain(String args, Instrumentation inst) {
        start(args, inst);
    }

    private static void start(String args, Instrumentation inst) {
        try {
            install(AgentArgs.parse(args), inst);
        } catch (Throwable t) {
            System.err.println("iormesh-agent: disabled: " + t);
        }
    }

    /**
     * Order matters: the bridge must be linked and warm, and ASM loaded,
     * before the first JDK class is rewritten, so that no agent class
     * loading happens inside a bracket of the target's I/O.
     */
    static void install(AgentArgs args, Instrumentation inst) throws Exception {
        int feature = Runtime.version().feature();
        String variant = args.table() != null ? args.table() : HookTable.variantFor(feature);
        if (variant == null) {
            // Decision #29: older JVMs are traced without Java attribution.
            System.err.println("iormesh-agent: JDK " + feature
                    + " is below 17, no Java attribution");
            return;
        }
        HookTable table = HookTable.load(variant);
        Bridge bridge = Bridge.install(inst, args.bridge());
        bridge.configure(table.maxId() + 1, args.walkStack());
        bridge.warmUp();
        BoundaryTransformer.warmUp();
        BoundaryTransformer transformer = new BoundaryTransformer(table);
        if (args.debug() != null) {
            Runtime.getRuntime().addShutdownHook(new Thread(
                    () -> writeDebug(args.debug(), table, transformer, bridge),
                    "iormesh-agent-debug"));
        }
        inst.addTransformer(transformer, true);
        Class<?>[] loaded = table.loadedTargets(inst);
        if (loaded.length > 0) {
            inst.retransformClasses(loaded);
        }
    }

    /**
     * Writes the debug dump (agent option debug=<path>): one line per
     * table row with its status and closed-bracket count. Status is
     * "applied" (method rewritten), "missing" (its class was seen but has
     * no such method — the table does not match this JDK) or "unloaded"
     * (its class never loaded, so nothing to rewrite yet).
     */
    private static void writeDebug(String path, HookTable table,
                                   BoundaryTransformer t, Bridge bridge) {
        try (PrintStream out = new PrintStream(path, "UTF-8")) {
            long[] counts = bridge.counts();
            Set<Integer> applied = t.applied();
            Set<String> seen = t.seen();
            out.printf("# iormesh-agent debug: variant=%s java=%d skips=%d stacks=%d%n",
                    table.variant(), Runtime.version().feature(), bridge.skips(),
                    bridge.stacks());
            for (HookTable.Hook h : table.hooks()) {
                String status = applied.contains(h.id()) ? "applied"
                        : seen.contains(h.owner()) ? "missing" : "unloaded";
                long n = h.id() < counts.length ? counts[h.id()] : 0;
                out.printf("%d\t%s\t%s\t%s\t%s\t%s\t%d%n", h.id(), h.kind(), h.owner(),
                        h.method(), h.descriptor(), status, n);
            }
        } catch (IOException | ReflectiveOperationException e) {
            System.err.println("iormesh-agent: debug dump failed: " + e);
        }
    }
}
