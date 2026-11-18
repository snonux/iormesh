package iormesh.bridge;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.ByteOrder;
import java.nio.channels.FileChannel;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.util.ArrayList;
import java.util.List;

/**
 * The ring writer, the THREAD/STACK/BRACKET records Hooks writes through
 * it, and the watermark promise. The records are decoded here only as
 * far as the tests need: the real reader is internal/agentspool, and
 * `mage agentTest` also runs its Java-driven Go tests (a concurrent
 * RingStress writer, the workload under the agent).
 */
public final class RingTest {
    /** One record seen in the file: type, length, offset of its word. */
    record Rec(int type, int len, int off) {}

    private RingTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("ring/header", RingTest::header),
                new AgentTests.Case("ring/bad size", RingTest::badSize),
                new AgentTests.Case("ring/no spool dir", RingTest::noDir),
                new AgentTests.Case("ring/full drops and counts", RingTest::fullDrops),
                new AgentTests.Case("ring/abandoned fill becomes PAD", RingTest::abandoned),
                new AgentTests.Case("ring/stale file replaced", RingTest::staleFile),
                new AgentTests.Case("ring/hooks write records", RingTest::hooksWrite),
                new AgentTests.Case("ring/watermark waits for closing", RingTest::watermark),
                new AgentTests.Case("ring/release discards", RingTest::release));
    }

    static void header() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        ByteBuffer b = map(r.path());
        equal(dir.resolve(ProcessHandle.current().pid() + ".ring"), r.path(), "path");
        equal(Ring.MAGIC, b.getLong(0), "magic");
        equal(Ring.VERSION, b.getInt(8), "version");
        equal(Ring.DATA, b.getInt(12), "header size");
        equal(Ring.MIN_CAPACITY, b.getLong(16), "capacity");
        equal(ProcessHandle.current().pid(), b.getLong(24), "pid");
        equal((long) Ring.DATA + Ring.MIN_CAPACITY, Files.size(r.path()), "file size");
        delete(dir);
    }

    static void badSize() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        try {
            for (long size : new long[] {0, 1000, Ring.MIN_CAPACITY / 2, Ring.MAX_CAPACITY * 2}) {
                rejectsSize(dir, size);
            }
        } finally {
            delete(dir);
        }
    }

    private static void rejectsSize(Path dir, long size) throws Exception {
        try {
            Ring.create(dir, size);
        } catch (IllegalArgumentException e) {
            return;
        }
        throw new AssertionError("ring size " + size + " accepted");
    }

    /** The spool directory is the tracer's: the agent never creates it. */
    static void noDir() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Files.delete(dir);
        try {
            Ring.create(dir, Ring.MIN_CAPACITY);
        } catch (IOException e) {
            check(!Files.exists(dir), "directory not created");
            return;
        }
        throw new AssertionError("ring created in a missing directory");
    }

    /** No reader: the ring fills, the rest is dropped and counted. */
    static void fullDrops() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        int attempts = 2000;
        int ok = 0;
        for (int i = 0; i < attempts; i++) {
            ok += r.bracket(1, 2, i, i + 1, 3, 4) ? 1 : 0;
        }
        List<Rec> recs = records(map(r.path()));
        long brackets = recs.stream().filter(x -> x.type() == Ring.BRACKET).count();
        equal((long) ok, brackets, "brackets in the ring");
        equal(Ring.MIN_CAPACITY / Ring.BRACKET_LEN, brackets, "ring filled");
        equal((long) attempts, brackets + r.dropped(), "delivered + dropped = written");
        equal(r.dropped(), map(r.path()).getLong(Ring.DROPPED + 8 * Ring.BRACKET),
                "dropped counted as BRACKET");
        delete(dir);
    }

    /**
     * A fill that threw publishes its reservation as a counted PAD, so
     * the reader skips it instead of waiting on a zero word for ever.
     */
    static void abandoned() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        int off = r.reserve(Ring.BRACKET_LEN, Ring.BRACKET);
        check(!r.abandon(off, Ring.BRACKET_LEN), "abandon reports no record");
        check(r.bracket(1, 2, 3, 4, 5, 6), "next record written");
        ByteBuffer b = map(r.path());
        equal(Ring.PAD, (int) (b.getLong(Ring.DATA) >>> 32), "abandoned slot is a PAD");
        equal(1L, b.getLong(Ring.ABANDONED), "abandoned counted");
        equal(List.of(Ring.BRACKET), records(b).stream().map(Rec::type).toList(),
                "the next record follows the PAD");
        delete(dir);
    }

    /**
     * A stale ring of a reused pid is replaced by a new file (new inode),
     * never truncated under a reader that may still map it.
     */
    static void staleFile() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring old = Ring.create(dir, Ring.MIN_CAPACITY);
        Object inode = Files.getAttribute(old.path(), "unix:ino");
        ByteBuffer oldMap = map(old.path());
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY * 2);
        check(!inode.equals(Files.getAttribute(r.path(), "unix:ino")), "new inode");
        equal(Ring.MAGIC, oldMap.getLong(0), "the old mapping stays readable");
        delete(dir);
    }

    /** A fresh thread's bracket writes its THREAD, the STACK, then the BRACKET. */
    static void hooksWrite() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(5, true, false);
        Hooks.ring = r;
        long[] tid = new long[1];
        try {
            Thread t = new Thread(() -> {
                tid[0] = Thread.currentThread().getId();
                Hooks.enter(3);
                Hooks.exit(3);
            }, "ring-test-thread");
            t.start();
            t.join();
        } finally {
            Hooks.ring = null;
        }
        ByteBuffer b = map(r.path());
        List<Rec> recs = records(b);
        equal(List.of(Ring.THREAD, Ring.STACK, Ring.BRACKET),
                recs.stream().map(Rec::type).toList(), "record order");
        Rec th = recs.get(0);
        equal(tid[0], b.getLong(th.off() + 8), "THREAD java id");
        check(b.getInt(th.off() + 16) > 0, "THREAD kernel tid looked up");
        checkBracket(b, recs.get(2), tid[0], b.getLong(recs.get(1).off() + 8));
        delete(dir);
    }

    private static void checkBracket(ByteBuffer b, Rec br, long tid, long stack) {
        int o = br.off();
        equal(Ring.BRACKET_LEN, br.len(), "BRACKET length");
        equal(3, b.getInt(o + 8), "hook");
        check(b.getInt(o + 12) > 0, "ktid");
        check(b.getLong(o + 16) <= b.getLong(o + 24), "enter <= exit");
        equal(stack, b.getLong(o + 32), "stack id is the STACK's");
        equal(tid, b.getLong(o + 40), "thread id");
    }

    /**
     * A thread between stamping its exit and publishing holds the
     * watermark back to its enter stamp; once idle it moves on.
     */
    static void watermark() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.ring = r;           // threads are tracked only with a ring
        Brackets b = new Brackets();
        Hooks.ring = null;
        try {
            Watermarks.emit(r);
            long enter = System.nanoTime();
            b.closing = enter;
            Watermarks.emit(r);   // held back to enter, still newer than the first
            b.closing = Brackets.IDLE;
            Watermarks.emit(r);
        } finally {
            b.closing = Brackets.IDLE;
        }
        ByteBuffer m = map(r.path());
        List<Long> marks = new ArrayList<>();
        for (Rec x : records(m)) {
            marks.add(m.getLong(x.off() + 8));
        }
        equal(3, marks.size(), "three watermarks");
        check(marks.get(0) < marks.get(1), "second is newer than the first");
        check(marks.get(1) < marks.get(2), "third passes the closing bracket");
        delete(dir);
    }

    static void release() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Hooks.claim();
        String path = Hooks.openRing(dir.toString(), Ring.MIN_CAPACITY);
        Hooks.release();
        check(!Files.exists(Path.of(path)), "ring file deleted");
        check(Hooks.ring == null, "no ring");
        check(Hooks.claim(), "claim is free again");
        delete(dir);
    }

    private static ByteBuffer map(Path p) throws IOException {
        try (FileChannel ch = FileChannel.open(p, StandardOpenOption.READ)) {
            return ch.map(FileChannel.MapMode.READ_ONLY, 0, ch.size())
                    .order(ByteOrder.LITTLE_ENDIAN);
        }
    }

    /** Every published record from 0 to head, PADs skipped (no wrap in these tests). */
    private static List<Rec> records(ByteBuffer b) {
        List<Rec> out = new ArrayList<>();
        long head = b.getLong(Ring.HEAD);
        for (int pos = 0; pos < head; ) {
            long word = b.getLong(Ring.DATA + pos);
            int len = (int) word;
            int type = (int) (word >>> 32);
            check(len >= 8 && len % 8 == 0, "framing at " + pos);
            if (type != Ring.PAD) {
                out.add(new Rec(type, len, Ring.DATA + pos));
            }
            pos += len;
        }
        return out;
    }

    private static void delete(Path dir) throws IOException {
        try (var files = Files.list(dir)) {
            for (Path f : files.toList()) {
                Files.delete(f);
            }
        }
        Files.delete(dir);
    }
}
