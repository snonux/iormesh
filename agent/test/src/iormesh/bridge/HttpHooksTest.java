package iormesh.bridge;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.channels.FileChannel;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.util.ArrayList;
import java.util.List;

/**
 * Hooks.http and the HTTP record: a probe's request lands in the ring
 * with its thread and stack, a probe that records nothing or throws
 * leaves no trace in the target, and an oversized record keeps the
 * request without its headers.
 */
public final class HttpHooksTest {
    private HttpHooksTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("http hooks/request written", HttpHooksTest::written),
                new AgentTests.Case("http hooks/null and throwing probes", HttpHooksTest::quiet),
                new AgentTests.Case("http hooks/oversized headers dropped", HttpHooksTest::oversized),
                new AgentTests.Case("http hooks/entry stamps", HttpHooksTest::stamps),
                new AgentTests.Case("http hooks/dns record", HttpHooksTest::dns));
    }

    private static final String[] REQUEST = {"GET", "http", "h", "/p?q=***", "HTTP/1.1",
        "127.0.0.1:1", "127.0.0.1:2", "authorization: ***", "date: x"};

    static void written() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-http-hooks");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(2, true, true);
        Hooks.httpProbe(1, (a, meta) -> {
            meta[Ring.M_STATUS] = 404;
            meta[Ring.M_NREQ] = 1;
            meta[Ring.M_FLAGS] = Ring.HTTP_SERVER;
            meta[Ring.M_EXCHANGES] = 3;
            meta[Ring.M_START] = 5;
            meta[Ring.M_REQ_BYTES] = 7;
            return REQUEST;
        });
        Hooks.ring = r;
        try {
            Hooks.http(null, "self", null, 1);
        } finally {
            Hooks.ring = null;
        }
        ByteBuffer b = map(r.path());
        List<int[]> recs = records(b);
        // This test's frames are the bridge package's and dropped from the
        // stack, so its id may be one an earlier test of the package wrote.
        check(recs.get(0)[0] == Ring.THREAD, "THREAD first");
        int o = recs.get(recs.size() - 1)[1];
        equal(Ring.HTTP, recs.get(recs.size() - 1)[0], "HTTP last");
        check(b.getLong(o + 8) > 0, "stamp");
        check(StackIds.frames(b.getLong(o + 16)) != null, "stack id known");
        equal(Thread.currentThread().getId(), b.getLong(o + 24), "thread id");
        check(b.getInt(o + 32) > 0, "kernel tid");
        equal(404, b.getInt(o + 36), "status");
        equal(9, b.getInt(o + 40), "strings");
        equal(1, b.getInt(o + 44), "request headers");
        equal(Ring.HTTP_SERVER, b.getInt(o + 48), "flags");
        equal(3, b.getInt(o + 52), "exchanges");
        equal(5L, b.getLong(o + 80), "start");
        equal(7L, b.getLong(o + 88), "request bytes");
        equal(-1L, b.getLong(o + 96), "response bytes unknown");
        equal(List.of(REQUEST), strings(b, o + Ring.HTTP_HEAD, 9), "strings");
        check(Hooks.httpCounts()[1] == 1, "counted");
        delete(dir, r);
    }

    static void quiet() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-http-hooks");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.httpProbe(1, (a, meta) -> null);
        Hooks.httpProbe(2, (a, meta) -> {
            throw new IllegalStateException("probe");
        });
        long fails = Hooks.fails();
        Hooks.ring = r;
        try {
            Hooks.http(null, null, null, 1);
            Hooks.http(null, null, null, 2);   // must not throw
            Hooks.http(null, null, null, 99);  // no probe
        } finally {
            Hooks.ring = null;
        }
        ByteBuffer b = map(r.path());
        equal(0, records(b).size(), "no record");
        equal(1L, Hooks.fails() - fails, "the throw counted");
        // The failed request counts as a dropped HTTP record, so the
        // tracer stops numbering instead of shifting the ordinals.
        equal(1L, b.getLong(Ring.DROPPED + 8 * Ring.HTTP), "HTTP drop counted");
        delete(dir, r);
    }

    static void oversized() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-http-hooks");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        List<String> s = new ArrayList<>(List.of(REQUEST).subList(0, 7));
        for (int i = 0; i < 64; i++) {
            s.add("x-h" + i + ": " + "v".repeat(1000));
        }
        long[] meta = {200, 64, 0, 1, 0, -1, -1};
        check(r.http(1, 2, 3, 4, s.toArray(new String[0]), meta, 0, 0, 0), "written");
        ByteBuffer b = map(r.path());
        int o = records(b).get(0)[1];
        equal(7, b.getInt(o + 40), "only the fixed strings");
        equal(0, b.getInt(o + 44), "no request headers");
        delete(dir, r);
    }

    /**
     * httpEnter/httpExit: the exit's probe sees its own this as the open
     * hook with the entry's stamp; a plain hook inside it sees the same
     * open hook; after the exit nothing is open, and nesting past
     * MAX_HTTP_DEPTH stays balanced.
     */
    static void stamps() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-http-hooks");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        List<Object[]> seen = new ArrayList<>();
        Hooks.httpProbe(1, (a, meta) -> {
            seen.add(new Object[] {a[1], a[3], meta[Ring.M_START]});
            return null;
        });
        Hooks.ring = r;
        try {
            long before = System.nanoTime();
            Hooks.httpEnter("outer", 1);
            Hooks.http(null, "inner", null, 1);
            Hooks.httpExit(null, "outer", null, 1);
            Hooks.http(null, "after", null, 1);
            for (int i = 0; i < Hooks.MAX_HTTP_DEPTH + 2; i++) {
                Hooks.httpEnter("deep" + i, 1);
            }
            for (int i = Hooks.MAX_HTTP_DEPTH + 1; i >= 0; i--) {
                Hooks.httpExit(null, "deep" + i, null, 1);
            }
            equal(0, Hooks.state().httpDepth, "balanced");
            equal("outer", seen.get(0)[1], "inner sees the open hook");
            check((long) seen.get(0)[2] >= before, "with its stamp");
            equal("outer", seen.get(1)[1], "the exit sees itself");
            equal(seen.get(0)[2], seen.get(1)[2], "same stamp");
            equal(null, seen.get(2)[1], "nothing open after");
            equal(0L, seen.get(2)[2], "no stamp after");
            equal(null, seen.get(3)[1], "past the depth: no open hook");
            equal("deep" + (Hooks.MAX_HTTP_DEPTH - 1), seen.get(5)[1], "kept again inside it");
        } finally {
            Hooks.ring = null;
            Hooks.httpProbe(1, null);
        }
        delete(dir, r);
    }

    /**
     * Hooks.dns: the probe's mapping becomes a DNS record with the
     * thread's identity; a null mapping and a lookup on a busy thread
     * (the agent's own) record nothing and count no HTTP loss.
     */
    static void dns() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-http-hooks");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.httpProbe(3, (a, meta) -> "h".equals(a[2]) ? new String[] {"h", "10.0.0.1"} : null);
        Hooks.ring = r;
        try {
            Hooks.dns(null, null, "h", 3);
            Hooks.dns(null, null, "other", 3);
            Hooks.state().busy = true;
            try {
                Hooks.dns(null, null, "h", 3);
            } finally {
                Hooks.state().busy = false;
            }
        } finally {
            Hooks.ring = null;
            Hooks.httpProbe(3, null);
        }
        ByteBuffer b = map(r.path());
        List<int[]> recs = records(b);
        equal(Ring.DNS, recs.get(recs.size() - 1)[0], "DNS last");
        equal(1L, recs.stream().filter(x -> x[0] == Ring.DNS).count(), "one DNS record");
        int o = recs.get(recs.size() - 1)[1];
        equal(Thread.currentThread().getId(), b.getLong(o + 24), "thread id");
        equal(2, b.getInt(o + 36), "strings");
        equal(List.of("h", "10.0.0.1"), strings(b, o + Ring.DNS_HEAD, 2), "host and address");
        equal(0L, b.getLong(Ring.DROPPED + 8 * Ring.HTTP), "no HTTP loss");
        delete(dir, r);
    }

    private static List<String> strings(ByteBuffer b, int p, int n) {
        List<String> out = new ArrayList<>();
        for (int i = 0; i < n; i++) {
            byte[] s = new byte[b.getInt(p)];
            b.get(p + 4, s);
            out.add(new String(s, StandardCharsets.UTF_8));
            p += 4 + s.length;
        }
        return out;
    }

    private static ByteBuffer map(Path p) throws java.io.IOException {
        try (FileChannel ch = FileChannel.open(p, StandardOpenOption.READ)) {
            return ch.map(FileChannel.MapMode.READ_ONLY, 0, ch.size()).order(ByteOrder.LITTLE_ENDIAN);
        }
    }

    /** {type, offset} of each published record but PADs, up to head. */
    private static List<int[]> records(ByteBuffer b) {
        List<int[]> out = new ArrayList<>();
        long head = b.getLong(Ring.HEAD);
        for (int pos = 0; pos < head; ) {
            long word = b.getLong(Ring.DATA + pos);
            if ((int) (word >>> 32) != Ring.PAD) {
                out.add(new int[] {(int) (word >>> 32), Ring.DATA + pos});
            }
            pos += (int) word;
        }
        return out;
    }

    private static void delete(Path dir, Ring r) throws java.io.IOException {
        Files.deleteIfExists(r.path());
        Files.delete(dir);
    }
}
