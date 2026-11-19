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
 * directory (decision #27, iormesh.bridge.Ring). The HTTP hooks
 * (HttpHooks, HttpTransformer; decision #22) write one HTTP record per
 * request the JDK's HttpServer, HttpClient or HttpURLConnection
 * completes, into the same ring.
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
        HttpTransformer http = prepareHttp(inst, bridge);
        Runtime.getRuntime().addShutdownHook(new Thread(
                () -> atExit(args, table, transformer, http, bridge), "iormesh-agent-exit"));
        retransform(inst, table, transformer);
        retransformHttp(inst, http);
    }

    /**
     * The HTTP hooks (decision #22): modules opened, probes registered,
     * the transformer warm. A failure here costs the HTTP events only —
     * the I/O hooks stand — so it is reported and the install goes on.
     */
    static HttpTransformer prepareHttp(Instrumentation inst, Bridge bridge) {
        try {
            HttpHooks hooks = HttpHooks.prepare(inst, bridge);
            HttpTransformer.warmUp();
            return new HttpTransformer(hooks);
        } catch (Exception | LinkageError e) {
            System.err.println("iormesh-agent: http hooks off: " + e);
            return null;
        }
    }

    /**
     * Registers the HTTP transformer and rewrites its already-loaded
     * targets (a live attach finds HttpClient loaded, say). A failure
     * removes it again, as retransform does, and leaves the I/O hooks.
     */
    private static void retransformHttp(Instrumentation inst, HttpTransformer http) {
        if (http == null) {
            return;
        }
        inst.addTransformer(http, true);
        try {
            java.util.List<Class<?>> loaded = http.hooks().loadedTargets(inst);
            if (!loaded.isEmpty()) {
                inst.retransformClasses(loaded.toArray(new Class<?>[0]));
            }
        } catch (Exception | Error e) {
            inst.removeTransformer(http);
            System.err.println("iormesh-agent: http hooks off: " + e);
        }
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
                               HttpTransformer http, Bridge bridge) {
        try {
            bridge.agentThread();  // the dump's own I/O is not the target's
            bridge.watermark();
        } catch (ReflectiveOperationException e) {
            System.err.println("iormesh-agent: last watermark failed: " + e);
        }
        if (args.debug() != null) {
            writeDebug(args.debug(), table, t, http, bridge);
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
     * The HTTP hooks' rows of the debug dump, in the same columns: id
     * "h<id>", kind "http", and the requests recorded. A row whose method
     * another row of the same name matched in this JDK (another JDK
     * generation's signature) is "alternative", not "missing".
     */
    private static void writeHttpDebug(PrintStream out, HttpTransformer http, long[] counts) {
        Set<Integer> applied = http.applied();
        Set<String> seen = http.seen();
        Set<String> appliedMethods = new java.util.HashSet<>();
        for (HttpHooks.Hook h : http.hooks().hooks()) {
            if (applied.contains(h.id())) {
                appliedMethods.add(h.target().owner() + "." + h.target().method());
            }
        }
        for (HttpHooks.Hook h : http.hooks().hooks()) {
            var t = h.target();
            String status = applied.contains(h.id()) ? "applied"
                    : appliedMethods.contains(t.owner() + "." + t.method()) ? "alternative"
                    : seen.contains(t.owner()) ? "missing" : "unloaded";
            long n = h.id() < counts.length ? counts[h.id()] : 0;
            out.printf("h%d\thttp\t%s\t%s\t%s\t%s\t%d%n", h.id(), t.owner(), t.method(),
                    t.descriptor(), status, n);
        }
    }

    /**
     * Writes the debug dump (agent option debug=<path>): one line per
     * table row with its status and closed-bracket count. Status is
     * "applied" (method rewritten), "missing" (its class was seen but has
     * no such method — the table does not match this JDK) or "unloaded"
     * (its class never loaded, so nothing to rewrite yet).
     */
    private static void writeDebug(String path, HookTable table, BoundaryTransformer t,
                                   HttpTransformer http, Bridge bridge) {
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
            if (http != null) {
                writeHttpDebug(out, http, bridge.httpCounts());
            }
        } catch (IOException | ReflectiveOperationException e) {
            System.err.println("iormesh-agent: debug dump failed: " + e);
        }
    }
}
