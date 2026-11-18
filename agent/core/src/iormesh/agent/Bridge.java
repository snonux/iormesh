package iormesh.agent;

import java.io.IOException;
import java.io.InputStream;
import java.lang.instrument.Instrumentation;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;
import java.util.Map;
import java.util.Set;
import java.util.jar.JarFile;

/**
 * Puts the bridge (iormesh.bridge.Hooks, StackIds) where rewritten JDK
 * code can call it, and gives the agent a handle on it.
 *
 * Two steps, both found necessary by S1: append the bridge jar to the
 * bootstrap class loader's search path (java.base classes can only link
 * classes that loader sees), then make java.base read the bridge's
 * module — the bootstrap loader's unnamed module — or the rewritten
 * methods fail to link Hooks. Appending prints a CDS note on stderr
 * ("Sharing is only supported for boot loader classes…"); stdout, the
 * workload's contract, is untouched.
 *
 * The agent core never names Hooks statically: it lives on the
 * bootstrap path, not in the agent jar, and resolving it before the
 * append would fail for good. Hence the reflective calls.
 */
final class Bridge {
    static final String HOOKS = "iormesh.bridge.Hooks";
    /** Where agentBuild nests the bridge jar inside the agent jar. */
    static final String NESTED = "iormesh-bridge.jar";

    private final Class<?> hooks;

    private Bridge(Class<?> hooks) {
        this.hooks = hooks;
    }

    /** Appends the bridge (path, or the nested jar if null) and links it. */
    static Bridge install(Instrumentation inst, String path) throws Exception {
        Path jar = path != null ? Path.of(path) : extractNested();
        inst.appendToBootstrapClassLoaderSearch(new JarFile(jar.toFile()));
        Class<?> hooks = Class.forName(HOOKS, true, null);
        inst.redefineModule(Object.class.getModule(), Set.of(hooks.getModule()),
                Map.of(), Map.of(), Set.of(), Map.of());
        return new Bridge(hooks);
    }

    /**
     * Copies the nested bridge jar to a temporary file: the bootstrap
     * search path takes files only. Install-time I/O, before any JDK
     * class is rewritten, so it is never bracketed. The file is deleted
     * at JVM exit; the JVM holds it open until then.
     */
    private static Path extractNested() throws IOException {
        Path tmp = Files.createTempFile("iormesh-bridge-", ".jar");
        tmp.toFile().deleteOnExit();
        try (InputStream in = Bridge.class.getResourceAsStream(NESTED)) {
            if (in == null) {
                throw new IOException("agent jar has no nested " + NESTED);
            }
            Files.copy(in, tmp, StandardCopyOption.REPLACE_EXISTING);
        }
        return tmp;
    }

    void configure(int hookIds, boolean walkStack) throws ReflectiveOperationException {
        hooks.getMethod("configure", int.class, boolean.class).invoke(null, hookIds, walkStack);
    }

    void warmUp() throws ReflectiveOperationException {
        hooks.getMethod("warmUp").invoke(null);
    }

    long[] counts() throws ReflectiveOperationException {
        return (long[]) hooks.getMethod("counts").invoke(null);
    }

    long skips() throws ReflectiveOperationException {
        return (long) hooks.getMethod("skips").invoke(null);
    }

    int stacks() throws ReflectiveOperationException {
        Class<?> ids = Class.forName("iormesh.bridge.StackIds", true, null);
        return (int) ids.getMethod("size").invoke(null);
    }
}
