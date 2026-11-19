package iormesh.bridge;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import iormesh.agent.OtelFixture;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.channels.FileChannel;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.util.List;

/**
 * Otel, the span-context read (decision #20, task 4b4): an armed OTEL
 * copy's current span lands in the thread state as numbers, outside a
 * span nothing does, an OTEL without its API jar arms nothing and throws
 * nothing into the caller, and brackets and HTTP records carry the ids.
 */
public final class OtelTest {
    private static final long HI = 0xa3ce929d0e0e4736L;
    private static final long LO = 0x4bf92f3577b34d01L;
    private static final long SPAN = 0xc11e0000000000a1L;

    private OtelTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("otel/hex ids", OtelTest::hex),
                new AgentTests.Case("otel/armed read", OtelTest::armedRead),
                new AgentTests.Case("otel/no api jar arms nothing", OtelTest::noApi),
                new AgentTests.Case("otel/bracket and http carry ids", OtelTest::records),
                new AgentTests.Case("otel/throwing reader is no span", OtelTest::throwing),
                // Last: it fills the reader list, then resets it.
                new AgentTests.Case("otel/copies past the cap refused", OtelTest::cap));
    }

    static void hex() {
        equal(HI, Otel.hex16(OtelFixture.TRACE, 0), "upper half");
        equal(LO, Otel.hex16(OtelFixture.TRACE, 16), "lower half");
        equal(-1L, Otel.hex16("ffffffffffffffff", 0), "all ones is a valid half");
        check(Otel.isHex("0123456789abcdefABCDEF"), "hex digits");
        check(!Otel.isHex("0123g"), "non-hex digit");
    }

    /** One loader's OTEL, initialized by the "app", armed once. */
    private static ClassLoader armed;

    private static ClassLoader armedLoader() throws Exception {
        if (armed == null) {
            ClassLoader l = OtelFixture.loader(true, OtelTest.class.getClassLoader(), b -> b);
            OtelFixture.initContext(l);
            check(Otel.arm(OtelFixture.lazyStorage(l), Hooks.state()), "armed");
            armed = l;
        }
        return armed;
    }

    static void armedRead() throws Exception {
        ClassLoader l = armedLoader();
        Brackets t = Hooks.state();
        check(!Otel.arm(OtelFixture.lazyStorage(l), t), "a copy is armed once");
        Otel.read(t);
        equal(0L, t.otelSpan, "no span current");
        // try/finally, not try-with-resources: -Xlint:try rejects an unread resource.
        AutoCloseable s = OtelFixture.makeCurrent(l, OtelFixture.SPAN);
        try {
            Otel.read(t);
            equal(HI, t.otelHi, "trace id upper half");
            equal(LO, t.otelLo, "trace id lower half");
            equal(SPAN, t.otelSpan, "span id");
            AutoCloseable inner = OtelFixture.makeCurrent(l, "00000000000000ff");
            try {
                Otel.read(t);
                equal(0xffL, t.otelSpan, "the inner span");
            } finally {
                inner.close();
            }
            Otel.read(t);
            equal(SPAN, t.otelSpan, "the outer span again");
        } finally {
            s.close();
        }
        Otel.read(t);
        check(t.otelHi == 0 && t.otelLo == 0 && t.otelSpan == 0, "span closed: no ids");
    }

    static void noApi() throws Exception {
        ClassLoader l = OtelFixture.loader(false, OtelTest.class.getClassLoader(), b -> b);
        OtelFixture.initContext(l);
        int before = Otel.armed();
        long fails = Hooks.fails();
        Hooks.otel(OtelFixture.lazyStorage(l));   // must not throw
        equal(before, Otel.armed(), "nothing armed");
        equal(1L, Hooks.fails() - fails, "the failed arm counted");
    }

    static void records() throws Exception {
        ClassLoader l = armedLoader();
        Path dir = Files.createTempDirectory("iormesh-otel-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(5, true, false);
        Hooks.httpProbe(1, (a, meta) -> new String[] {"GET", "http", "h", "/", "HTTP/1.1",
            "", ""});
        Hooks.ring = r;
        AutoCloseable s = OtelFixture.makeCurrent(l, OtelFixture.SPAN);
        try {
            Hooks.enter(3);
            Hooks.exit(3);
            Hooks.http(null, null, null, 1);
        } finally {
            Hooks.ring = null;
            s.close();
        }
        Hooks.enter(3);   // outside the span, no ring: nothing written
        Hooks.exit(3);
        ByteBuffer b = map(r.path());
        int[] br = find(b, Ring.BRACKET);
        equal(Ring.BRACKET_OTEL_LEN, br[1], "BRACKET with ids");
        checkIds(b, br[0] + 48, "BRACKET");
        checkIds(b, find(b, Ring.HTTP)[0] + 56, "HTTP");
        Files.deleteIfExists(r.path());
        Files.delete(dir);
    }

    /**
     * A reader whose read throws counts as no span and forgets the
     * thread's cached context, so a stale cache cannot answer later.
     */
    static void throwing() throws Exception {
        java.lang.invoke.MethodHandle boom = java.lang.invoke.MethodHandles.throwException(
                Object.class, IllegalStateException.class).bindTo(new IllegalStateException("x"));
        java.lang.invoke.MethodHandle str = java.lang.invoke.MethodHandles.dropArguments(
                java.lang.invoke.MethodHandles.constant(String.class, ""), 0, Object.class);
        ClassLoader loader = new ClassLoader(null) {};
        Otel.Reader r = new Otel.Reader(loader, boom, str, str);
        Brackets t = Hooks.state();
        t.otelContext = new java.lang.ref.WeakReference<>(new Object());
        t.otelSpan = 7;
        Otel.publish(r);
        Otel.read(t);   // must not throw
        equal(0L, t.otelSpan, "no span");
        check(t.otelContext == null, "cache forgotten");
        // The list holds readers weakly: keep this one alive until here.
        java.lang.ref.Reference.reachabilityFence(r);
        Otel.reset();
        armed = null;
    }

    /** MAX_READERS copies are read; the next one is refused and counted. */
    static void cap() throws Exception {
        List<ClassLoader> keep = new java.util.ArrayList<>();
        try {
            while (Otel.armed() < Otel.MAX_READERS) {
                ClassLoader l = OtelFixture.loader(true, OtelTest.class.getClassLoader(), b -> b);
                OtelFixture.initContext(l);
                check(Otel.arm(OtelFixture.lazyStorage(l), Hooks.state()), "armed below the cap");
                keep.add(l);
            }
            ClassLoader extra = OtelFixture.loader(true, OtelTest.class.getClassLoader(), b -> b);
            OtelFixture.initContext(extra);
            long fails = Hooks.fails();
            Hooks.otel(OtelFixture.lazyStorage(extra));   // must not throw
            equal(Otel.MAX_READERS, Otel.armed(), "still the cap");
            equal(1L, Hooks.fails() - fails, "the refused copy counted");
        } finally {
            Otel.reset();
            armed = null;
            keep.clear();
        }
    }

    private static void checkIds(ByteBuffer b, int o, String what) {
        equal(HI, b.getLong(o), what + " trace id upper half");
        equal(LO, b.getLong(o + 8), what + " trace id lower half");
        equal(SPAN, b.getLong(o + 16), what + " span id");
    }

    private static ByteBuffer map(Path p) throws java.io.IOException {
        try (FileChannel ch = FileChannel.open(p, StandardOpenOption.READ)) {
            return ch.map(FileChannel.MapMode.READ_ONLY, 0, ch.size())
                    .order(ByteOrder.LITTLE_ENDIAN);
        }
    }

    /** {offset, length} of the first record of type. */
    private static int[] find(ByteBuffer b, int type) {
        long head = b.getLong(Ring.HEAD);
        for (int pos = 0; pos < head; ) {
            long word = b.getLong(Ring.DATA + pos);
            if ((int) (word >>> 32) == type) {
                return new int[] {Ring.DATA + pos, (int) word};
            }
            pos += (int) word;
        }
        throw new AssertionError("no record of type " + type);
    }
}
