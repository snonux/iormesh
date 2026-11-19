package iormesh.agent;

import iormesh.agent.http.JdkHttpClient;
import iormesh.agent.http.JdkHttpServer;
import iormesh.agent.http.JdkUrlConnection;
import iormesh.agent.http.Library;
import iormesh.agent.http.Target;
import java.lang.instrument.Instrumentation;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Set;
import java.util.function.Supplier;

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

    /** v1's libraries (decision #22): the JDK's own three first. */
    private static final List<Supplier<Library>> LIBRARIES = List.of(
            JdkHttpServer::new, JdkHttpClient::new, JdkUrlConnection::new);

    private final List<Hook> hooks = new ArrayList<>();
    private final Map<String, Hook> byKey = new HashMap<>();
    private final Set<String> classes = new HashSet<>();

    /**
     * Prepares every library of this JVM: its module reads the bridge
     * (rewritten methods call Hooks) and opens what its probes reflect
     * on to the agent, then its targets get ids and their probes are
     * registered with the bridge. A library whose module is missing (a
     * jlinked runtime) or whose internals moved is skipped with a note on
     * stderr; the others stand.
     */
    static HttpHooks prepare(Instrumentation inst, Bridge bridge) throws ReflectiveOperationException {
        HttpHooks h = new HttpHooks();
        for (Supplier<Library> s : LIBRARIES) {
            Library lib = s.get();
            Optional<Module> m = ModuleLayer.boot().findModule(lib.module());
            if (m.isEmpty()) {
                continue;
            }
            List<Target> targets;
            try {
                open(inst, m.get(), lib, bridge.module());
                targets = lib.targets();
            } catch (ReflectiveOperationException | RuntimeException | LinkageError e) {
                System.err.println("iormesh-agent: http hooks of " + lib.name() + " off: " + e);
                continue;
            }
            for (Target t : targets) {
                h.add(lib, t, bridge);
            }
        }
        return h;
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
        classes.add(t.owner());
        bridge.httpProbe(hook.id(), t.probe());
    }

    /** For tests: the given targets, ids 1.., no probes registered. */
    static HttpHooks of(List<Target> targets) {
        HttpHooks h = new HttpHooks();
        for (Target t : targets) {
            Hook hook = new Hook(h.hooks.size() + 1, null, t);
            h.hooks.add(hook);
            h.byKey.put(t.key(), hook);
            h.classes.add(t.owner());
        }
        return h;
    }

    List<Hook> hooks() {
        return hooks;
    }

    boolean hasClass(String internalName) {
        return classes.contains(internalName);
    }

    Hook lookup(String owner, String method, String descriptor) {
        return byKey.get(owner + "." + method + descriptor);
    }

    /**
     * The listed classes already loaded by the bootstrap or platform
     * loader (java.base, java.net.http and jdk.httpserver): those must be
     * retransformed.
     */
    List<Class<?>> loadedTargets(Instrumentation inst) {
        List<Class<?>> out = new ArrayList<>();
        ClassLoader platform = ClassLoader.getPlatformClassLoader();
        for (Class<?> c : inst.getAllLoadedClasses()) {
            ClassLoader l = c.getClassLoader();
            if ((l == null || l == platform) && hasClass(c.getName().replace('.', '/'))
                    && inst.isModifiableClass(c)) {
                out.add(c);
            }
        }
        return out;
    }
}
