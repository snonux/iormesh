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
 * Puts the bridge (iormesh.bridge.Hooks, StackIds, Ring) where rewritten JDK
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
    /** The temporary copy of the nested jar, or null for bridge=<path>. */
    private final Path extracted;

    private Bridge(Class<?> hooks, Path extracted) {
        this.hooks = hooks;
        this.extracted = extracted;
    }

    /**
     * A handle on an already loadable Hooks (tests: the bridge classes on
     * the class path), with nothing extracted.
     */
    static Bridge of(Class<?> hooks) {
        return new Bridge(hooks, null);
    }

    /** Appends the bridge (path, or the nested jar if null) and links it. */
    static Bridge install(Instrumentation inst, String path) throws Exception {
        Path extracted = path != null ? null : extractNested();
        Path jar = path != null ? Path.of(path) : extracted;
        inst.appendToBootstrapClassLoaderSearch(new JarFile(jar.toFile()));
        Class<?> hooks = Class.forName(HOOKS, true, null);
        inst.redefineModule(Object.class.getModule(), Set.of(hooks.getModule()),
                Map.of(), Map.of(), Set.of(), Map.of());
        return new Bridge(hooks, extracted);
    }

    /**
     * Copies the nested bridge jar to a temporary file: the bootstrap
     * search path takes files only. Install-time I/O, before any JDK
     * class is rewritten, so it is never bracketed. The agent deletes it
     * once the bridge is loaded and warm ({@link #deleteExtracted});
     * deleteOnExit is only the fallback if install fails before that.
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

    /**
     * Deletes the extracted jar, so a SIGKILLed JVM leaves no temp file
     * behind. The JVM opened the jar when it was appended and keeps that
     * descriptor, and on Linux an unlinked file stays readable through
     * it, so classes the bridge loads later still resolve.
     */
    void deleteExtracted() throws IOException {
        if (extracted != null) {
            Files.deleteIfExists(extracted);
        }
    }

    /** See Hooks.claim: false if an agent is installed in this JVM already. */
    boolean claim() throws ReflectiveOperationException {
        return (boolean) hooks.getMethod("claim").invoke(null);
    }

    /**
     * See Hooks.requestResync: a repeated load asks the installed agent's
     * watermark thread to resend its dictionary (task 5j4).
     */
    void requestResync() throws ReflectiveOperationException {
        hooks.getMethod("requestResync").invoke(null);
    }

    /** See Hooks.release: undoes claim after an install that rewrote nothing. */
    void release() throws ReflectiveOperationException {
        hooks.getMethod("release").invoke(null);
    }

    void configure(int hookIds, boolean walkStack, boolean count)
            throws ReflectiveOperationException {
        hooks.getMethod("configure", int.class, boolean.class, boolean.class)
                .invoke(null, hookIds, walkStack, count);
    }

    /** Creates the ring in the spool directory; returns its path. */
    String openRing(String dir, long capacity) throws Exception {
        try {
            return (String) hooks.getMethod("openRing", String.class, long.class)
                    .invoke(null, dir, capacity);
        } catch (java.lang.reflect.InvocationTargetException e) {
            // "disabled: <the IOException>" says what to fix; the wrapper does not.
            throw e.getCause() instanceof Exception c ? c : e;
        }
    }

    /** See Hooks.describeHook: one hook-table row into the ring. */
    boolean describeHook(int id, String kind, String cls, String method, String nativeName,
                         String syscalls) throws ReflectiveOperationException {
        return (boolean) hooks.getMethod("describeHook", int.class, String.class, String.class,
                String.class, String.class, String.class)
                .invoke(null, id, kind, cls, method, nativeName, syscalls);
    }

    /** The bridge's module (the bootstrap loader's unnamed module). */
    Module module() {
        return hooks.getModule();
    }

    /** The bridge's Hooks, which a rewritten app class must resolve (BridgeSight). */
    Class<?> hooksClass() {
        return hooks;
    }

    /** See Hooks.httpProbe: registers HTTP hook id's probe. */
    void httpProbe(int id, java.util.function.BiFunction<Object[], long[], String[]> probe)
            throws ReflectiveOperationException {
        hooks.getMethod("httpProbe", int.class, java.util.function.BiFunction.class)
                .invoke(null, id, probe);
    }

    /** See Hooks.httpCaller: the caller's identity for a request completing later. */
    @SuppressWarnings("unchecked")
    java.util.function.Supplier<long[]> httpCaller() throws ReflectiveOperationException {
        return (java.util.function.Supplier<long[]>) hooks.getMethod("httpCaller").invoke(null);
    }

    /** See Hooks.httpLate: writes a request that completed on another thread. */
    @SuppressWarnings("unchecked")
    java.util.function.BiConsumer<long[], java.util.function.Function<long[], String[]>> httpLate()
            throws ReflectiveOperationException {
        return (java.util.function.BiConsumer<long[], java.util.function.Function<long[], String[]>>)
                hooks.getMethod("httpLate").invoke(null);
    }

    /** See Hooks.httpCounts: requests recorded per HTTP hook id. */
    long[] httpCounts() throws ReflectiveOperationException {
        return (long[]) hooks.getMethod("httpCounts").invoke(null);
    }

    /** See Hooks.otel: arms span-context reading for an already loaded LazyStorage. */
    void otel(Class<?> lazyStorage) throws ReflectiveOperationException {
        hooks.getMethod("otel", Class.class).invoke(null, lazyStorage);
    }

    /** See Hooks.otelArmed: the OTEL copies being read. */
    int otelArmed() throws ReflectiveOperationException {
        return (int) hooks.getMethod("otelArmed").invoke(null);
    }

    void startWatermarks(long intervalMs) throws ReflectiveOperationException {
        hooks.getMethod("startWatermarks", long.class).invoke(null, intervalMs);
    }

    void agentThread() throws ReflectiveOperationException {
        hooks.getMethod("agentThread").invoke(null);
    }

    void watermark() throws ReflectiveOperationException {
        hooks.getMethod("watermark").invoke(null);
    }

    long ringDropped() throws ReflectiveOperationException {
        return (long) hooks.getMethod("ringDropped").invoke(null);
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

    long fails() throws ReflectiveOperationException {
        return (long) hooks.getMethod("fails").invoke(null);
    }

    int stacks() throws ReflectiveOperationException {
        Class<?> ids = Class.forName("iormesh.bridge.StackIds", true, null);
        return (int) ids.getMethod("size").invoke(null);
    }
}
