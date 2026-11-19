package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import java.util.List;

/**
 * OtelTransformer on the real LazyStorage of the vendored OTEL (task
 * 4b4): the rewritten initializer verifies, runs, and arms the reader
 * when the app first uses OTEL's context — not before; a loader that
 * cannot see the bridge still gets a working OTEL; the transformer
 * leaves every other class alone.
 */
public final class OtelTransformerTest {
    private OtelTransformerTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("otel transformer/arms at first context use",
                        OtelTransformerTest::arms),
                new AgentTests.Case("otel transformer/no bridge in sight",
                        OtelTransformerTest::noBridge),
                new AgentTests.Case("otel transformer/other classes untouched",
                        OtelTransformerTest::others),
                new AgentTests.Case("otel transformer/warm-up", OtelTransformer::warmUp));
    }

    private static byte[] rewrite(byte[] b) {
        byte[] out = new OtelTransformer().rewrite(b);
        check(out != null, "LazyStorage has a static initializer to rewrite");
        return out;
    }

    static void arms() throws Exception {
        ClassLoader l = OtelFixture.loader(true, OtelTransformerTest.class.getClassLoader(),
                OtelTransformerTest::rewrite);
        int before = iormesh.bridge.Hooks.otelArmed();
        OtelFixture.lazyStorage(l);   // loaded, not initialized: nothing armed yet
        equal(before, iormesh.bridge.Hooks.otelArmed(), "armed before OTEL ran");
        // try/finally, not try-with-resources: -Xlint:try rejects an unread resource.
        AutoCloseable s = OtelFixture.makeCurrent(l, OtelFixture.SPAN);
        try {
            equal(before + 1, iormesh.bridge.Hooks.otelArmed(), "armed by the initializer");
        } finally {
            s.close();
        }
    }

    /**
     * The platform loader as parent: iormesh.bridge.Hooks is not visible,
     * the call fails to link, and the catch-all keeps OTEL working.
     */
    static void noBridge() throws Exception {
        ClassLoader l = OtelFixture.loader(true, ClassLoader.getPlatformClassLoader(),
                OtelTransformerTest::rewrite);
        int before = iormesh.bridge.Hooks.otelArmed();
        OtelFixture.makeCurrent(l, OtelFixture.SPAN).close();
        OtelFixture.initContext(l);
        equal(before, iormesh.bridge.Hooks.otelArmed(), "nothing armed");
    }

    static void others() throws Exception {
        OtelTransformer t = new OtelTransformer();
        byte[] any = {(byte) 0xca, (byte) 0xfe};
        check(t.transform(null, ClassLoader.getSystemClassLoader(),
                "io/opentelemetry/context/Context", null, null, any) == null, "other class");
        check(t.transform(null, null, OtelTransformer.LAZY_STORAGE, null, null, any) == null,
                "bootstrap loader");
        check(t.transform(null, ClassLoader.getSystemClassLoader(), OtelTransformer.LAZY_STORAGE,
                Object.class, null, any) == null, "retransformation");
    }
}
