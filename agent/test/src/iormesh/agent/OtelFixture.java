package iormesh.agent;

import java.io.IOException;
import java.io.InputStream;
import java.net.URL;
import java.net.URLClassLoader;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.function.UnaryOperator;

/**
 * OpenTelemetry for the agent's tests, without making it a dependency of
 * the agent or of the test build: the workload's vendored API jars
 * (workload/otel/third_party, task za4) are loaded at run time in class
 * loaders of their own, and spans are made current reflectively — the
 * same calls OtelScenario makes, {@code Span.wrap(SpanContext.create(…))
 * .makeCurrent()}. `mage agentTest` runs from the repository root.
 */
public final class OtelFixture {
    static final Path JARS = Path.of("workload", "otel", "third_party", "opentelemetry");
    static final String VERSION = "1.67.0";
    public static final String TRACE = "a3ce929d0e0e47364bf92f3577b34d01";
    public static final String SPAN = "c11e0000000000a1";
    public static final String LAZY_STORAGE = "io.opentelemetry.context.LazyStorage";

    private OtelFixture() {}

    /** The vendored jars: context and common always, api when withApi. */
    static URL[] jars(boolean withApi) throws IOException {
        String[] names = withApi ? new String[] {"api", "context", "common"}
                : new String[] {"context", "common"};
        URL[] out = new URL[names.length];
        for (int i = 0; i < names.length; i++) {
            Path p = JARS.resolve("opentelemetry-" + names[i] + "-" + VERSION + ".jar");
            if (!Files.isRegularFile(p)) {
                throw new IOException(p + " not found (run from the repository root)");
            }
            out[i] = p.toUri().toURL();
        }
        return out;
    }

    /**
     * A loader of its own OTEL copy, child-first for io.opentelemetry
     * (the test's class path has none, so plain parent-first would do;
     * findClass is where the rewrite hooks in). Each LazyStorage it
     * defines first goes through rewrite (identity: unchanged).
     */
    public static ClassLoader loader(boolean withApi, ClassLoader parent,
                                     UnaryOperator<byte[]> rewrite) throws IOException {
        return new URLClassLoader(jars(withApi), parent) {
            @Override
            protected Class<?> findClass(String name) throws ClassNotFoundException {
                if (!name.equals(LAZY_STORAGE)) {
                    return super.findClass(name);
                }
                try (InputStream in = getResourceAsStream(name.replace('.', '/') + ".class")) {
                    if (in == null) {
                        throw new ClassNotFoundException(name);
                    }
                    byte[] b = rewrite.apply(in.readAllBytes());
                    return defineClass(name, b, 0, b.length);
                } catch (IOException e) {
                    throw new ClassNotFoundException(name, e);
                }
            }
        };
    }

    /** Initializes l's OTEL context storage as an app does: Context.current(). */
    public static void initContext(ClassLoader l) throws ReflectiveOperationException {
        Class.forName("io.opentelemetry.context.Context", true, l).getMethod("current")
                .invoke(null);
    }

    /** l's LazyStorage, loaded (initialized only if OTEL's context was used). */
    public static Class<?> lazyStorage(ClassLoader l) throws ClassNotFoundException {
        return Class.forName(LAZY_STORAGE, false, l);
    }

    /**
     * Makes the span (TRACE, span) current on this thread in l's OTEL and
     * returns its Scope, an AutoCloseable.
     */
    public static AutoCloseable makeCurrent(ClassLoader l, String span)
            throws ReflectiveOperationException {
        Class<?> ctx = Class.forName("io.opentelemetry.api.trace.SpanContext", true, l);
        Class<?> flags = Class.forName("io.opentelemetry.api.trace.TraceFlags", true, l);
        Class<?> state = Class.forName("io.opentelemetry.api.trace.TraceState", true, l);
        Object sc = ctx.getMethod("create", String.class, String.class, flags, state)
                .invoke(null, TRACE, span, flags.getMethod("getSampled").invoke(null),
                        state.getMethod("getDefault").invoke(null));
        Class<?> spanType = Class.forName("io.opentelemetry.api.trace.Span", true, l);
        Object s = spanType.getMethod("wrap", ctx).invoke(null, sc);
        return (AutoCloseable) Class.forName("io.opentelemetry.context.ImplicitContextKeyed",
                true, l).getMethod("makeCurrent").invoke(s);
    }
}
