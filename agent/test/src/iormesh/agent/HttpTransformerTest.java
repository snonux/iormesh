package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import io.netty.handler.codec.http.HttpObjectDecoder;
import io.netty.handler.codec.http.HttpObjectEncoder;
import iormesh.agent.http.Library;
import iormesh.agent.http.Netty;
import iormesh.agent.http.Target;
import iormesh.bridge.Hooks;
import java.io.InputStream;
import java.lang.reflect.InvocationTargetException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.function.BiFunction;

/**
 * HttpTransformer against HttpFixture: the listed methods hand their
 * return value, this and the named argument to Hooks.http on every way
 * out (a constructor only at its returns), still verify and behave, and
 * the methods it cannot hook are left alone.
 */
final class HttpTransformerTest {
    private static final String FIXTURE = "iormesh/agent/HttpFixture";

    private HttpTransformerTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("http transformer/hands over ret, this and arg",
                        HttpTransformerTest::handsOver),
                new AgentTests.Case("http transformer/refuses primitive, takes static",
                        HttpTransformerTest::refuses),
                new AgentTests.Case("http transformer/other loader is a no-op",
                        HttpTransformerTest::otherLoader),
                new AgentTests.Case("http transformer/warm-up", HttpTransformer::warmUp),
                new AgentTests.Case("http transformer/app loader needs the bridge",
                        HttpTransformerTest::appLoader),
                new AgentTests.Case("http transformer/argument pair, stores refused",
                        HttpTransformerTest::pairAndStores),
                new AgentTests.Case("http transformer/tells the library what it rewrote",
                        HttpTransformerTest::tellsLibrary));
    }

    /**
     * Each rewrite of a third-party class tells its library which methods
     * were rewritten in which loader (task 6j4), over Netty's own targets
     * and the stand-ins of its codecs: the decoder alone (older Netty
     * 4.1's encoder has no write to rewrite) reports no write; the JDK's
     * loaders and a class nothing of which is rewritten report nothing.
     */
    static void tellsLibrary() throws Exception {
        List<String> told = new ArrayList<>();
        Netty netty = new Netty();
        Library lib = new Library() {
            public String name() { return netty.name(); }
            public String module() { return null; }
            public List<String> opens() { return List.of(); }
            public List<Target> targets() { return netty.targets(); }
            @Override
            public boolean appLoaded() { return true; }
            @Override
            public void rewritten(ClassLoader loader, Target t) {
                told.add((loader == HttpTransformerTest.class.getClassLoader() ? "" : "?")
                        + t.method());
            }
        };
        HttpTransformer t = new HttpTransformer(HttpHooks.of(lib),
                new BridgeSight(Hooks.class, null));
        ClassLoader sees = HttpTransformerTest.class.getClassLoader();
        check(t.transform(null, sees, internal(HttpObjectDecoder.class), null, null,
                bytes(HttpObjectDecoder.class)) != null, "the decoder rewritten");
        equal(List.of("<init>", "decode"), told, "the decoder alone: no write");
        told.clear();
        check(t.transform(null, sees, internal(HttpObjectEncoder.class), null, null,
                bytes(HttpObjectEncoder.class)) != null, "the encoder rewritten");
        equal(List.of("<init>", "write"), told, "the encoder");
        told.clear();
        check(t.rewrite(internal(HttpObjectEncoder.class), bytes(HttpObjectEncoder.class))
                != null, "rewritten without a loader (the JDK's)");
        check(t.rewrite(FIXTURE, fixtureBytes()) == null, "an unlisted class");
        equal(List.of(), told, "nothing told without an application loader");
    }

    private static String internal(Class<?> c) {
        return c.getName().replace('.', '/');
    }

    private static byte[] bytes(Class<?> c) throws Exception {
        try (InputStream in = c.getResourceAsStream(c.getSimpleName() + ".class")) {
            return in.readAllBytes();
        }
    }

    /**
     * What one probe call saw; own: the open hook was the method itself,
     * with a start stamp (a stamped target's exit).
     */
    record Call(int id, Object ret, Object self, Object arg, boolean own) {}

    /** send and fail are stamped (fail's throw must pop its stamp too). */
    private static List<Target> targets(BiFunction<Object[], long[], String[]> p) {
        return List.of(
                new Target(FIXTURE, "<init>", "(Ljava/lang/String;)V", 0, p).stamped(),
                new Target(FIXTURE, "send", "(Ljava/lang/String;)Ljava/lang/String;",
                        Target.NO_ARG, p).stamped(),
                new Target(FIXTURE, "fail", "(JLjava/lang/String;)Ljava/lang/String;",
                        Target.LAST_ARG, p).stamped(),
                new Target(FIXTURE, "count", "(I)I", 0, p),
                new Target(FIXTURE, "lookup", "(Ljava/lang/String;)[Ljava/lang/String;", 0, p)
                        .lookup());
    }

    static byte[] fixtureBytes() throws Exception {
        try (InputStream in = HttpFixture.class.getResourceAsStream("HttpFixture.class")) {
            return in.readAllBytes();
        }
    }

    /**
     * Runs the rewritten fixture with a ring open (Hooks.http records
     * nothing without one) and probes that log their calls and return
     * null, so no record is written.
     */
    static void handsOver() throws Exception {
        List<Call> calls = new ArrayList<>();
        HttpHooks hooks = HttpHooks.of(targets(null));
        for (HttpHooks.Hook h : hooks.hooks()) {
            int id = 100 + h.id();
            Hooks.httpProbe(id, (a, meta) -> {
                // A throw hands over its throwable (task de4): compared by message.
                Object ret = a[0] instanceof Throwable t ? "threw " + t.getMessage() : a[0];
                calls.add(new Call(id, ret, a[1], a[2], a[3] == a[1] && meta[4] > 0));
                return null;
            });
        }
        HttpHooks shifted = HttpHooks.of(shift(targets(null)));
        byte[] after = new HttpTransformer(shifted).rewrite(FIXTURE, fixtureBytes());
        Class<?> c = new BoundaryTransformerTest.OneClassLoader("iormesh.agent.HttpFixture", after)
                .loadClass("iormesh.agent.HttpFixture");
        Path dir = Files.createTempDirectory("iormesh-http-test");
        Hooks.openRing(dir.toString(), 1 << 16);
        try {
            Object f = c.getConstructor(String.class).newInstance("n");
            equal("n:req", c.getMethod("send", String.class).invoke(f, "req"), "send");
            equal("ok", c.getMethod("fail", long.class, String.class).invoke(f, 0L, "ok"), "fail(0)");
            try {
                c.getMethod("fail", long.class, String.class).invoke(f, 1L, "boom");
                throw new AssertionError("fail(1) returned");
            } catch (InvocationTargetException e) {
                equal("boom", e.getCause().getMessage(), "fail(1) still throws its own");
            }
            // The constructor is never stamped, even when its row says so.
            equal(List.of(new Call(101, null, f, "n", false),
                    new Call(102, "n:req", f, null, true), new Call(103, "ok", f, "ok", true),
                    new Call(103, "threw boom", f, "boom", true)), calls, "probe calls");
            calls.clear();
            Object got = c.getMethod("lookup", String.class).invoke(null, "h");
            try {
                c.getMethod("lookup", String.class).invoke(null, "");
                throw new AssertionError("lookup(\"\") returned");
            } catch (InvocationTargetException e) {
                equal("empty", e.getCause().getMessage(), "lookup still throws its own");
            }
            equal(List.of(new Call(105, got, null, "h", false),
                    new Call(105, "threw empty", null, "", false)),
                    calls, "static lookup: null this, its argument, through Hooks.dns");
        } finally {
            Hooks.release();
            Files.deleteIfExists(dir);
        }
    }

    /** Ids 101.. instead of 1..: clear of other tests' probes. */
    private static List<Target> shift(List<Target> ts) {
        List<Target> out = new ArrayList<>();
        for (int i = 0; i < 100; i++) {
            out.add(new Target("x/Pad", "m" + i, "()V", Target.NO_ARG, null));
        }
        out.addAll(ts);
        return out;
    }

    static void refuses() throws Exception {
        HttpTransformer t = new HttpTransformer(HttpHooks.of(targets(null)));
        check(t.rewrite(FIXTURE, fixtureBytes()) != null, "fixture rewritten");
        equal(java.util.Set.of(1, 2, 3, 5), t.applied(), "applied: all but the primitive");
        equal(HttpTransformer.REFUSED, HttpTransformer.argSlot("(I)I", 0, false),
                "primitive argument refused");
        equal(3, HttpTransformer.argSlot("(JLjava/lang/String;)V", Target.LAST_ARG, false),
                "last argument after a long");
        equal(0, HttpTransformer.argSlot("(Ljava/lang/String;)V", 0, true),
                "a static method's first argument is slot 0");
        equal(HttpTransformer.NO_SLOT, HttpTransformer.argSlot("()V", Target.LAST_ARG, false),
                "no argument to pass");
    }

    /**
     * A third-party library's class (task de4) is rewritten in an
     * application loader that resolves the bridge's Hooks, never in the
     * JDK's loaders, nor in a loader that cannot see the bridge (its
     * parent is the platform loader), nor without a bridge at all.
     */
    static void appLoader() throws Exception {
        HttpHooks app = HttpHooks.of(targets(null), true);
        ClassLoader sees = HttpTransformerTest.class.getClassLoader();
        ClassLoader blind = new ClassLoader(ClassLoader.getPlatformClassLoader()) {};
        HttpTransformer t = new HttpTransformer(app, new BridgeSight(Hooks.class, null));
        check(t.transform(null, sees, FIXTURE, null, null, fixtureBytes()) != null,
                "an app loader that sees the bridge");
        check(t.transform(null, null, FIXTURE, null, null, fixtureBytes()) == null,
                "a third-party class in the bootstrap loader");
        check(t.transform(null, blind, FIXTURE, null, null, fixtureBytes()) == null,
                "a loader that does not see the bridge");
        check(new HttpTransformer(app).transform(null, sees, FIXTURE, null, null,
                fixtureBytes()) == null, "no bridge to link");
    }

    /**
     * A row naming two arguments hands them over as an Object[] pair, the
     * throw path too; a third-party library's listed method that stores
     * into a passed slot is left alone (its catch-all frame could fail
     * verification).
     */
    static void pairAndStores() throws Exception {
        List<Call> calls = new ArrayList<>();
        BiFunction<Object[], long[], String[]> p = (a, meta) -> {
            calls.add(new Call(0, a[0] instanceof Throwable ? "threw" : a[0], a[1],
                    List.of((Object[]) a[2]), false));
            return null;
        };
        HttpHooks hooks = HttpHooks.of(shift(List.of(
                new Target(FIXTURE, "pair", "(Ljava/lang/String;JLjava/lang/String;)Ljava/lang/String;",
                        0, p).withSecond(2),
                new Target(FIXTURE, "reassign", "(Ljava/lang/String;)Ljava/lang/String;", 0, p))),
                true);
        for (HttpHooks.Hook h : hooks.hooks()) {
            if (h.target().probe() != null) {
                Hooks.httpProbe(h.id(), h.target().probe());
            }
        }
        HttpTransformer t = new HttpTransformer(hooks);
        byte[] after = t.rewrite(FIXTURE, fixtureBytes());
        equal(java.util.Set.of(101), t.applied(), "the reassigning method is not hooked");
        Class<?> c = new BoundaryTransformerTest.OneClassLoader("iormesh.agent.HttpFixture", after)
                .loadClass("iormesh.agent.HttpFixture");
        Path dir = Files.createTempDirectory("iormesh-http-test");
        Hooks.openRing(dir.toString(), 1 << 16);
        try {
            Object f = c.getConstructor(String.class).newInstance("n");
            equal("a/b", c.getMethod("pair", String.class, long.class, String.class)
                    .invoke(f, "a", 0L, "b"), "pair");
            try {
                c.getMethod("pair", String.class, long.class, String.class).invoke(f, "a", 1L, "b");
                throw new AssertionError("pair(1) returned");
            } catch (InvocationTargetException e) {
                equal("a", e.getCause().getMessage(), "pair still throws its own");
            }
            equal(List.of(new Call(0, "a/b", f, List.of("a", "b"), false),
                    new Call(0, "threw", f, List.of("a", "b"), false)), calls, "pairs");
        } finally {
            Hooks.release();
            Files.deleteIfExists(dir);
        }
    }

    static void otherLoader() throws Exception {
        HttpTransformer t = new HttpTransformer(HttpHooks.of(targets(null)));
        check(t.transform(null, HttpTransformerTest.class.getClassLoader(), FIXTURE, null, null,
                fixtureBytes()) == null, "an application class is never rewritten");
        check(t.rewrite("iormesh/agent/Other", fixtureBytes()) == null, "unlisted class");
    }
}
