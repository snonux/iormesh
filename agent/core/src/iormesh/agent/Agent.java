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
 * With {@code spool=<dir>} the brackets, the stacks and threads they
 * name, and watermarks go to a memory-mapped ring in that tracer-owned
 * directory (decision #27, iormesh.bridge.Ring).
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
     * Order matters: the bridge must be linked and warm, the ring mapped,
     * and ASM loaded, before the first JDK class is rewritten, so that no
     * agent class loading happens inside a bracket of the target's I/O.
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
        if (!bridge.claim()) {
            // A second -javaagent or attach: the first install's hooks
            // stand; installing again would bracket every call twice.
            System.err.println("iormesh-agent: already installed, ignoring this load");
            bridge.deleteExtracted();
            return;
        }
        BoundaryTransformer transformer = prepare(args, table, bridge);
        Runtime.getRuntime().addShutdownHook(new Thread(
                () -> atExit(args, table, transformer, bridge), "iormesh-agent-exit"));
        retransform(inst, table, transformer);
    }

    /**
     * Everything before the first rewrite: configure the bridge, map the
     * ring, warm up, start the watermarks. A failure here has rewritten
     * nothing, so the claim is released (and the ring discarded) — a
     * later live attach may then try again instead of finding an agent
     * "already installed" with nothing active.
     */
    static BoundaryTransformer prepare(AgentArgs args, HookTable table, Bridge bridge)
            throws Exception {
        try {
            bridge.configure(table.maxId() + 1, args.walkStack(), args.debug() != null);
            if (args.spool() != null) {
                bridge.openRing(args.spool(), args.ringSize());
                describeHooks(table, bridge);
            }
            bridge.warmUp();
            bridge.deleteExtracted();
            BoundaryTransformer.warmUp();
            BoundaryTransformer transformer = new BoundaryTransformer(table);
            bridge.startWatermarks(args.watermarkMs());
            return transformer;
        } catch (Exception | Error e) {
            try {
                bridge.release();
                bridge.deleteExtracted();
            } catch (Exception cleanup) {
                e.addSuppressed(cleanup);
            }
            throw e;
        }
    }

    /**
     * Writes the hook table into the fresh ring, one HOOK record per row,
     * ahead of every bracket: the joiner reads each hook's syscall
     * allowlist from the ring, so the tracer needs no copy of the tables
     * and follows a table= override too. The ring is empty here, so a
     * row cannot be dropped for lack of room; one that is anyway (a
     * pathological ring size) only leaves that hook's brackets
     * unjoinable — attribution none, never a guess.
     */
    private static void describeHooks(HookTable table, Bridge bridge)
            throws ReflectiveOperationException {
        for (HookTable.Hook h : table.hooks()) {
            bridge.describeHook(h.id(), h.kind(), h.owner(), h.method(), h.nativeRef(),
                    String.join(",", h.syscalls()));
        }
    }

    /**
     * At JVM exit: a last watermark — every bracket closed so far is in
     * the ring, so the joiner need not time out — then the debug dump.
     */
    private static void atExit(AgentArgs args, HookTable table, BoundaryTransformer t,
                               Bridge bridge) {
        try {
            bridge.agentThread();  // the dump's own I/O is not the target's
            bridge.watermark();
        } catch (ReflectiveOperationException e) {
            System.err.println("iormesh-agent: last watermark failed: " + e);
        }
        if (args.debug() != null) {
            writeDebug(args.debug(), table, t, bridge);
        }
    }

    /**
     * Registers the transformer and rewrites the already-loaded targets.
     * If that fails the transformer is removed again, so "disabled" on
     * stderr is true for classes loaded later too; classes rewritten
     * before the failure keep their (harmless) hooks.
     */
    private static void retransform(Instrumentation inst, HookTable table,
                                    BoundaryTransformer transformer) throws Exception {
        inst.addTransformer(transformer, true);
        try {
            Class<?>[] loaded = table.loadedTargets(inst);
            if (loaded.length > 0) {
                inst.retransformClasses(loaded);
            }
        } catch (Exception | Error e) {
            inst.removeTransformer(transformer);
            throw e;
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
            out.printf("# iormesh-agent debug: variant=%s java=%d skips=%d fails=%d stacks=%d"
                    + " ring_dropped=%d%n", table.variant(), Runtime.version().feature(),
                    bridge.skips(), bridge.fails(), bridge.stacks(), bridge.ringDropped());
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
