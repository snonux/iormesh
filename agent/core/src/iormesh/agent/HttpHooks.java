package iormesh.agent;

import iormesh.agent.http.Async;
import iormesh.agent.http.JdkDns;
import iormesh.agent.http.JdkHttpClient;
import iormesh.agent.http.JdkHttpServer;
import iormesh.agent.http.JdkUrlConnection;
import iormesh.agent.http.Library;
import iormesh.agent.http.Servlet;
import iormesh.agent.http.Target;
import java.lang.instrument.Instrumentation;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.function.Function;

/**
 * The HTTP hooks (PLAN.md M3 "HTTP/HTTPS request events", decision #22):
 * the libraries' targets with their ids, ready for HttpTransformer. Ids
 * are their own space, 1.., separate from the I/O hook table's: an HTTP
 * hook is no bracket and joins no syscall.
 *
 * Code, not a TSV like HookTable: each target comes with a probe that
 * reads the library's objects, and a JDK generation that changes a
 * signature gets a second row of the same method (Target), never a
 * table variant — the unmatched row is reported as "alternative" while
 * a sibling applied.
 */
final class HttpHooks {
    /** One target with its id and library. */
    record Hook(int id, Library library, Target target) {}

    /**
     * v1's libraries (decision #22): the JDK's own three first, then the
     * JDK's name lookups (decision #8, task wd4: DNS records, not
     * requests), then the third-party ones of the app's class path in
     * #22's order (task de4): Servlet, … Each is built with the bridge's
     * handles for requests completing on another thread (Async), which
     * only HttpClient's sendAsync needs.
     */
    private static final List<Function<Async, Library>> LIBRARIES = List.of(
            a -> new JdkHttpServer(), JdkHttpClient::new, a -> new JdkUrlConnection(),
            a -> new JdkDns(), a -> new Servlet());

    private final List<Hook> hooks = new ArrayList<>();
    private final Map<String, Hook> byKey = new HashMap<>();
    /** Classes of the JDK's libraries: rewritten only in the bootstrap or platform loader. */
    private final Set<String> classes = new HashSet<>();
    /** Classes of third-party libraries: rewritten only in an application loader. */
    private final Set<String> appClasses = new HashSet<>();

    /**
     * Prepares every library of this JVM: its module reads the bridge
     * (rewritten methods call Hooks) and opens what its probes reflect
     * on to the agent, then its targets get ids and their probes are
     * registered with the bridge. A library whose module is missing (a
     * jlinked runtime) or whose internals moved is skipped with a note on
     * stderr; the others stand. A third-party library (Library.appLoaded)
     * has no module to open: its targets are listed whether or not the app
     * has it, and HttpTransformer rewrites them in whichever application
     * loader defines them.
     */
    static HttpHooks prepare(Instrumentation inst, Bridge bridge) throws ReflectiveOperationException {
        HttpHooks h = new HttpHooks();
        Async async = new Async(bridge.httpCaller(), bridge.httpLate());
        for (Function<Async, Library> s : LIBRARIES) {
            Library lib = s.apply(async);
            List<Target> targets = targets(inst, lib, bridge.module());
            for (Target t : targets) {
                h.add(lib, t, bridge);
            }
        }
        return h;
    }

    /** lib's targets with its module prepared, or none (noted) when it cannot be. */
    private static List<Target> targets(Instrumentation inst, Library lib, Module bridge) {
        Optional<Module> m = lib.appLoaded() ? Optional.empty()
                : ModuleLayer.boot().findModule(lib.module());
        if (!lib.appLoaded() && m.isEmpty()) {
            return List.of();
        }
        try {
            if (m.isPresent()) {
                open(inst, m.get(), lib, bridge);
            }
            return lib.targets();
        } catch (ReflectiveOperationException | RuntimeException | LinkageError e) {
            System.err.println("iormesh-agent: http hooks of " + lib.name() + " off: " + e);
            return List.of();
        }
    }

    private static void open(Instrumentation inst, Module m, Library lib, Module bridge) {
        Map<String, Set<Module>> opens = new HashMap<>();
        for (String pkg : lib.opens()) {
            opens.put(pkg, Set.of(HttpHooks.class.getModule()));
        }
        inst.redefineModule(m, Set.of(bridge), Map.of(), opens, Set.of(), Map.of());
    }

    private void add(Library lib, Target t, Bridge bridge) throws ReflectiveOperationException {
        Hook hook = new Hook(hooks.size() + 1, lib, t);
        if (byKey.putIfAbsent(t.key(), hook) != null) {
            throw new IllegalArgumentException("duplicate http target " + t.key());
        }
        hooks.add(hook);
        (lib.appLoaded() ? appClasses : classes).add(t.owner());
        bridge.httpProbe(hook.id(), t.probe());
    }

    /** For tests: the given targets, ids 1.., no probes registered, all of the JDK. */
    static HttpHooks of(List<Target> targets) {
        return of(targets, false);
    }

    /** For tests: the given targets as a JDK's or (app) a third-party library's. */
    static HttpHooks of(List<Target> targets, boolean app) {
        HttpHooks h = new HttpHooks();
        for (Target t : targets) {
            Hook hook = new Hook(h.hooks.size() + 1, null, t);
            h.hooks.add(hook);
            h.byKey.put(t.key(), hook);
            (app ? h.appClasses : h.classes).add(t.owner());
        }
        return h;
    }

    List<Hook> hooks() {
        return hooks;
    }

    /** Whether a JDK library lists internalName (bootstrap or platform loader). */
    boolean hasClass(String internalName) {
        return classes.contains(internalName);
    }

    /** Whether a third-party library lists internalName (an application loader). */
    boolean hasAppClass(String internalName) {
        return appClasses.contains(internalName);
    }

    Hook lookup(String owner, String method, String descriptor) {
        return byKey.get(owner + "." + method + descriptor);
    }

    /**
     * The listed classes already loaded, which must be retransformed: the
     * JDK's by the bootstrap or platform loader (java.base, java.net.http
     * and jdk.httpserver), the third-party ones by any other loader (a live
     * attach to a running servlet container). HttpTransformer still checks
     * each third-party class's loader as it rewrites it.
     */
    List<Class<?>> loadedTargets(Instrumentation inst) {
        List<Class<?>> out = new ArrayList<>();
        ClassLoader platform = ClassLoader.getPlatformClassLoader();
        for (Class<?> c : inst.getAllLoadedClasses()) {
            ClassLoader l = c.getClassLoader();
            String name = c.getName().replace('.', '/');
            boolean jdk = l == null || l == platform;
            if ((jdk ? hasClass(name) : hasAppClass(name)) && inst.isModifiableClass(c)) {
                out.add(c);
            }
        }
        return out;
    }
}
