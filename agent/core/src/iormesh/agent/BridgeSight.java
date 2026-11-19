package iormesh.agent;

import java.lang.instrument.Instrumentation;
import java.util.Collections;
import java.util.Map;
import java.util.Set;
import java.util.WeakHashMap;

/**
 * Whether a class an application loader defines can call the bridge
 * (task de4): HttpTransformer rewrites a third-party library's method to
 * call iormesh.bridge.Hooks, and a class that cannot link it would fail
 * at the rewritten method's first return — in the app. Two conditions:
 *
 * <ul>
 *   <li>the loader resolves iormesh.bridge.Hooks to the bridge's own
 *       class. Every loader that delegates to its parent for unknown
 *       packages does (the bridge is on the bootstrap path); an OSGi
 *       framework without boot delegation for iormesh.* does not, and
 *       its classes are then left alone.
 *   <li>a class of a named module reads the bridge's module (the
 *       bootstrap loader's unnamed one); the read is added through
 *       Instrumentation, or the class is skipped.
 * </ul>
 *
 * The loader's answer is kept weakly (no undeployed web app is pinned)
 * and computed with no lock of ours held: Class.forName may load through
 * the app's loader, and holding a lock across that could deadlock with
 * an app thread that holds the loader's lock and waits for ours (the
 * live-attach lesson of task 4b4). Two threads may both compute it.
 */
final class BridgeSight {
    /** The bridge's Hooks; null: nothing links it (tests, warm-up). */
    private final Class<?> hooks;
    /** For a named module's read of the bridge; null: such classes are refused. */
    private final Instrumentation inst;
    private final Map<ClassLoader, Boolean> loaders = Collections.synchronizedMap(new WeakHashMap<>());

    BridgeSight(Class<?> hooks, Instrumentation inst) {
        this.hooks = hooks;
        this.inst = inst;
    }

    /** Whether a class of loader and module, named className, may call the bridge. */
    boolean linkable(ClassLoader loader, Module module, String className) {
        if (hooks == null || loader == null) {
            return false;
        }
        Boolean known = loaders.get(loader);
        if (known == null) {
            known = sees(loader);
            loaders.put(loader, known);
            if (!known) {
                System.err.println("iormesh-agent: http hooks off in " + loader
                        + ": it does not see the bridge (" + className + ")");
            }
        }
        return known && reads(module);
    }

    private boolean sees(ClassLoader loader) {
        try {
            return Class.forName(hooks.getName(), false, loader) == hooks;
        } catch (ClassNotFoundException | LinkageError e) {
            return false;
        }
    }

    /** Whether module reads the bridge's module, after adding the read if it can. */
    private boolean reads(Module module) {
        Module bridge = hooks.getModule();
        if (module == null || !module.isNamed() || module.canRead(bridge)) {
            return true;
        }
        if (inst == null || !inst.isModifiableModule(module)) {
            return false;
        }
        inst.redefineModule(module, Set.of(bridge), Map.of(), Map.of(), Set.of(), Map.of());
        return module.canRead(bridge);
    }
}
