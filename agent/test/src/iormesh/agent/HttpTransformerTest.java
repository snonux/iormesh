package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

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
                new AgentTests.Case("http transformer/refuses static and primitive",
                        HttpTransformerTest::refuses),
                new AgentTests.Case("http transformer/other loader is a no-op",
                        HttpTransformerTest::otherLoader),
                new AgentTests.Case("http transformer/warm-up", HttpTransformer::warmUp));
    }

    /** What one probe call saw. */
    record Call(int id, Object ret, Object self, Object arg) {}

    private static List<Target> targets(BiFunction<Object[], int[], String[]> p) {
        return List.of(
                new Target(FIXTURE, "<init>", "(Ljava/lang/String;)V", 0, p),
                new Target(FIXTURE, "send", "(Ljava/lang/String;)Ljava/lang/String;",
                        Target.NO_ARG, p),
                new Target(FIXTURE, "fail", "(JLjava/lang/String;)Ljava/lang/String;",
                        Target.LAST_ARG, p),
                new Target(FIXTURE, "count", "(I)I", 0, p),
                new Target(FIXTURE, "util", "()Ljava/lang/String;", Target.NO_ARG, p));
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
                calls.add(new Call(id, a[0], a[1], a[2]));
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
            equal(List.of(new Call(101, null, f, "n"), new Call(102, "n:req", f, null),
                    new Call(103, "ok", f, "ok"), new Call(103, null, f, "boom")), calls,
                    "probe calls");
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
        equal(java.util.Set.of(1, 2, 3), t.applied(), "applied: ctor, send, fail only");
        equal(0, HttpTransformer.argSlot("(I)I", 0), "primitive argument refused");
        equal(3, HttpTransformer.argSlot("(JLjava/lang/String;)V", Target.LAST_ARG),
                "last argument after a long");
        equal(-1, HttpTransformer.argSlot("()V", Target.LAST_ARG), "no argument to pass");
    }

    static void otherLoader() throws Exception {
        HttpTransformer t = new HttpTransformer(HttpHooks.of(targets(null)));
        check(t.transform(null, HttpTransformerTest.class.getClassLoader(), FIXTURE, null, null,
                fixtureBytes()) == null, "an application class is never rewritten");
        check(t.rewrite("iormesh/agent/Other", fixtureBytes()) == null, "unlisted class");
    }
}
