package iormesh.agent;

import java.io.IOException;
import java.io.PrintStream;
import java.lang.instrument.Instrumentation;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

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
 * completes — or a third-party library of the app's class path, such as
 * a servlet container (task de4) — into the same ring. With a ring, an app's OpenTelemetry is
 * read too (OtelTransformer, iormesh.bridge.Otel; decision #20): each
 * record carries the trace and span ids of the span current then.
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
            // What a re-attach wants is a tracer session's view of them,
            // and an earlier session consumed the dictionary records, so
            // the load asks for a resync (task 5j4) — a flag the watermark
            // thread acts on at its next tick; this thread never waits.
            // The first load's ring and options stay: a different spool=
            // in this load is not followed. Bridge.install found the
            // installed bridge and extracted nothing (task 8j4); only two
            // loads racing past that check can each have extracted a jar,
            // and the loser's goes first: an installed bridge older than
            // 5j4 has no requestResync, and that load must still leave
            // no file behind.
            bridge.deleteExtracted();
            try {
                bridge.requestResync();
                System.err.println("iormesh-agent: already installed, resyncing its ring");
            } catch (ReflectiveOperationException e) {
                System.err.println("iormesh-agent: already installed (an older agent, no"
                        + " resync), ignoring this load");
            }
            return;
        }
        BoundaryTransformer transformer = prepare(args, table, bridge);
        HttpTransformer http = prepareHttp(inst, bridge);
        // Ids of the rows reported as matching no method, so the exit
        // hook names only those whose class loaded after install.
        Set<Integer> reported = ConcurrentHashMap.newKeySet();
        Runtime.getRuntime().addShutdownHook(new Thread(
                () -> atExit(args, table, transformer, http, bridge, reported),
                "iormesh-agent-exit"));
        retransform(inst, table, transformer);
        // Most listed classes are loaded by now, so a table/JDK mismatch
        // shows at install. A class first loaded later (sun.nio.ch.Net
        // before the app's first socket) is reported at exit instead:
        // the transformer runs inside the target's class loading and
        // stays quiet there.
        reportUnapplied(table, transformer, reported, System.err);
        retransformHttp(inst, http);
        if (args.spool() != null) {
            installOtel(inst, bridge);
        }
    }

    /**
     * Span-context reading (decision #20), only with a ring — the ids
     * have nowhere else to go. The transformer arms each OTEL copy as
     * the app initializes it; copies a live attach finds initialized
     * already are armed here. A failure costs the OTEL ids only.
     */
    static void installOtel(Instrumentation inst, Bridge bridge) {
        try {
            OtelTransformer.warmUp();
            inst.addTransformer(new OtelTransformer(), false);
            for (Class<?> c : OtelTransformer.loaded(inst)) {
                bridge.otel(c);
            }
        } catch (Exception | LinkageError e) {
            System.err.println("iormesh-agent: otel ids off: " + e);
        }
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
            return new HttpTransformer(hooks, new BridgeSight(bridge.hooksClass(), inst));
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
     *
     * The table is complete once any other record follows it: prepare
     * calls warmUp right after this, whose THREAD and WATERMARK records
     * the tracer takes as that signal before it probes the natives the
     * table names (internal/correlate jvm.tableRead). It drains while
     * these rows are written, so a hook row must never come after
     * another record (task tf4). The bridge keeps the rows: a later
     * reader gets them again from the watermark thread (task 5j4,
     * Watermarks.resync), closed by a WATERMARK instead.
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
     * the ring, so the joiner need not time out — then the ring file is
     * unlinked (task 2j4: a tracer that reads it has it mapped; a dead
     * JVM's file would otherwise stay in the spool), then the rows that
     * matched no method in a class first loaded after install (task
     * bj4; install reported the others), then the debug dump.
     * A JVM killed without its shutdown hooks leaves the file to the
     * tracer, which removes rings whose process is gone.
     */
    private static void atExit(AgentArgs args, HookTable table, BoundaryTransformer t,
                               HttpTransformer http, Bridge bridge, Set<Integer> reported) {
        try {
            bridge.agentThread();  // the dump's own I/O is not the target's
            bridge.watermark();
        } catch (ReflectiveOperationException e) {
            System.err.println("iormesh-agent: last watermark failed: " + e);
        }
        try {
            bridge.removeRing();
        } catch (ReflectiveOperationException e) {
            System.err.println("iormesh-agent: removing the ring failed: " + e.getCause());
        }
        reportUnapplied(table, t, reported, System.err);
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
     * One stderr line per table row whose class the transformer has
     * been handed but whose method it did not rewrite (task bj4): the
     * table's exact descriptor matches no method of this JDK, so that
     * kind of I/O stays attribution none. Before this the drift showed
     * only in the debug= dump. Rows of a class never loaded print
     * nothing (it may load later, or not exist in this JDK at all), and
     * a table that matches prints nothing — the success path is silent.
     * A row already in reported is skipped and a printed one is added,
     * so install and exit name each row once.
     *
     * seen is read before applied: the transformer marks a class seen
     * only after its rewrite, so a class being rewritten concurrently
     * (a live attach while the app loads it) is not mistaken for one
     * whose methods are missing.
     */
    static void reportUnapplied(HookTable table, BoundaryTransformer t, Set<Integer> reported,
                                PrintStream err) {
        Set<String> seen = t.seen();
        Set<Integer> applied = t.applied();
        for (HookTable.Hook h : table.hooks()) {
            if (seen.contains(h.owner()) && !applied.contains(h.id()) && reported.add(h.id())) {
                err.println("iormesh-agent: hook " + h.id() + " (" + h.kind() + ") " + h.key()
                        + " matches no method in this JDK (java " + Runtime.version().feature()
                        + ", table " + table.variant() + "), no attribution for it");
            }
        }
    }

    /**
     * The HTTP hooks' rows of the debug dump, in the same columns: id
     * "h<id>", kind "http" ("dns" for a name lookup, task wd4), and the
     * requests (mappings) recorded. A row whose method
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
            out.printf("h%d\t%s\t%s\t%s\t%s\t%s\t%d%n", h.id(), t.dns() ? "dns" : "http",
                    t.owner(), t.method(), t.descriptor(), status, n);
        }
    }

    /**
     * Writes the debug dump (agent option debug=<path>): one line per
     * table row with its status and closed-bracket count. Status is
     * "applied" (method rewritten), "missing" (its class was seen but has
     * no such method — the table does not match this JDK; such a row was
     * also reported on stderr) or "unloaded"
     * (its class never loaded, so nothing to rewrite yet).
     */
    private static void writeDebug(String path, HookTable table, BoundaryTransformer t,
                                   HttpTransformer http, Bridge bridge) {
        try (PrintStream out = new PrintStream(path, "UTF-8")) {
            long[] counts = bridge.counts();
            Set<Integer> applied = t.applied();
            Set<String> seen = t.seen();
            out.printf("# iormesh-agent debug: variant=%s java=%d skips=%d fails=%d stacks=%d"
                    + " ring_dropped=%d otel=%d%n", table.variant(), Runtime.version().feature(),
                    bridge.skips(), bridge.fails(), bridge.stacks(), bridge.ringDropped(),
                    bridge.otelArmed());
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
