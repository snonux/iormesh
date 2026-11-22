package iormesh.bridge;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import iormesh.testprobe.ClassProbe;
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
                new AgentTests.Case("ring/hook table record", RingTest::hookRecord),
                new AgentTests.Case("ring/class records ahead of stack", RingTest::classRecords),
                new AgentTests.Case("ring/class labels", RingTest::classLabels),
                new AgentTests.Case("ring/classes under a SecurityManager", RingTest::securityManager),
                new AgentTests.Case("ring/renamed thread described again", RingTest::renamed),
                new AgentTests.Case("ring/virtual thread flagged", RingTest::virtualThread),
                new AgentTests.Case("ring/watermark waits for closing", RingTest::watermark),
                new AgentTests.Case("ring/release discards", RingTest::release),
                // Last: it moves the dictionary generation for the JVM.
                new AgentTests.Case("ring/later reader resyncs", RingTest::resync),
                new AgentTests.Case("ring/partial resend holds watermarks",
                        RingTest::partialResend),
                new AgentTests.Case("ring/old reader then new resyncs",
                        RingTest::oldReaderThenNew));
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
        equal("rw-------", java.nio.file.attribute.PosixFilePermissions.toString(
                Files.getPosixFilePermissions(r.path())), "mode 0600");
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
        // CLASS records come and go with what earlier cases described
        // (ids are per JVM, not per ring): classRecords checks them.
        recs = recs.stream().filter(x -> x.type() != Ring.CLASS).toList();
        equal(List.of(Ring.THREAD, Ring.STACK, Ring.BRACKET),
                recs.stream().map(Rec::type).toList(), "record order");
        Rec th = recs.get(0);
        equal(tid[0], b.getLong(th.off() + 8), "THREAD java id");
        check(b.getInt(th.off() + 16) > 0, "THREAD kernel tid looked up");
        equal(0, b.getInt(th.off() + 20), "THREAD flags of a platform thread");
        checkBracket(b, recs.get(2), tid[0], b.getLong(recs.get(1).off() + 8));
        delete(dir);
    }

    /** HOOK: id, count 5, flags 0, then kind, class, method, native, syscalls. */
    static void hookRecord() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        check(r.hook(7, "read", "java/io/FileInputStream", "read", "read0", "read,pread64"),
                "hook written");
        ByteBuffer b = map(r.path());
        List<Rec> recs = records(b);
        equal(1, recs.size(), "one record");
        int o = recs.get(0).off();
        equal(Ring.HOOK, recs.get(0).type(), "type");
        equal(7L, b.getLong(o + 8), "id");
        equal(5, b.getInt(o + 16), "string count");
        equal(0, b.getInt(o + 20), "flags");
        List<String> got = new ArrayList<>();
        for (int i = 0, p = o + 24; i < 5; i++) {
            byte[] s = new byte[b.getInt(p)];
            b.get(p + 4, s);
            got.add(new String(s, java.nio.charset.StandardCharsets.UTF_8));
            p += 4 + s.length;
        }
        equal(List.of("read", "java/io/FileInputStream", "read", "read0", "read,pread64"), got,
                "strings");
        delete(dir);
    }

    /**
     * A new stack's classes are described in CLASS records ahead of its
     * STACK (task fe4), and the STACK names each frame's class id: the
     * probe's frame its CLASS (binary name, the app loader, the test
     * classes' path), the JDK's a non-zero id too.
     */
    static void classRecords() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(5, true, false);
        Hooks.ring = r;
        try {
            Thread t = new Thread(() -> ClassProbe.run(() -> {
                Hooks.enter(3);
                Hooks.exit(3);
            }), "ring-class-thread");
            t.start();
            t.join();
        } finally {
            Hooks.ring = null;
        }
        ByteBuffer b = map(r.path());
        java.util.Map<Long, List<String>> classes = new java.util.HashMap<>();
        Rec stack = null;
        for (Rec x : records(b)) {
            if (x.type() == Ring.CLASS) {
                check(stack == null, "CLASS ahead of the STACK");
                equal(3, b.getInt(x.off() + 16), "CLASS string count");
                classes.put(b.getLong(x.off() + 8), strings(b, x.off() + 24, 3));
            } else if (x.type() == Ring.STACK) {
                stack = x;
            }
        }
        check(stack != null, "a STACK");
        int n = b.getInt(stack.off() + 16);
        List<String> frames = strings(b, stack.off() + 24 + 8 * n, n);
        String probe = ClassProbe.class.getName();
        check(frames.get(0).startsWith(probe + "#run("), "innermost frame is the probe: " + frames);
        for (int i = 0; i < n; i++) {
            check(b.getLong(stack.off() + 24 + 8 * i) != ClassIds.NONE, "class id of frame " + i);
        }
        List<String> c = classes.get(b.getLong(stack.off() + 24));
        String where = Path.of(ClassProbe.class.getProtectionDomain().getCodeSource()
                .getLocation().toURI()).toString();
        equal(List.of(probe, "app", where), c, "the probe's CLASS");
        delete(dir);
    }

    /**
     * Under a SecurityManager whose policy grants the app neither
     * getStackWalkerWithClassReference nor getProtectionDomain (review of
     * fe4), the bridge still walks stacks and describes classes: it asks
     * for both in doPrivileged. A child JVM runs SecurityProbe with the
     * bridge on the boot class path; this reads the ring it leaves. JDK
     * 24 and later cannot enable a SecurityManager: skipped there.
     */
    static void securityManager() throws Exception {
        if (Runtime.version().feature() >= 24) {
            AgentTests.skip("JDK " + Runtime.version().feature() + " has no SecurityManager");
        }
        Path dir = Files.createTempDirectory("iormesh-ring-sm");
        Path tests = Path.of(ClassProbe.class.getProtectionDomain().getCodeSource().getLocation().toURI());
        Path bridge = Path.of(Hooks.class.getProtectionDomain().getCodeSource().getLocation().toURI());
        Path policy = dir.resolve("probe.policy");
        Files.writeString(policy, "grant codeBase \"" + tests.toUri() + "-\" {\n"
                + "  permission java.io.FilePermission \"" + dir + "/-\", \"read,write,delete\";\n"
                + "  permission java.io.FilePermission \"/proc/-\", \"read\";\n"
                + "  permission java.lang.RuntimePermission \"manageProcess\";\n};\n");
        String javaBin = ProcessHandle.current().info().command().orElse("java");
        Process p = new ProcessBuilder(javaBin, "-Xbootclasspath/a:" + bridge,
                "-Djava.security.manager", "-Djava.security.policy==" + policy,
                "-cp", tests.toString(), "iormesh.testprobe.SecurityProbe", dir.toString())
                .redirectErrorStream(true).start();
        String out = new String(p.getInputStream().readAllBytes(),
                java.nio.charset.StandardCharsets.UTF_8);
        equal(0, p.waitFor(), "probe exit: " + out);
        check(out.contains("probe fails 0"), "no failed bracket: " + out);
        Files.delete(policy);
        Path ring;
        try (var files = Files.list(dir)) {
            ring = files.findFirst().orElseThrow();
        }
        checkProbeClass(map(ring), tests.toString());
        delete(dir);
    }

    /**
     * The ring's STACK starts at the probe's lambda (SecurityProbe's own
     * method, not hidden), whose CLASS names where.
     */
    private static void checkProbeClass(ByteBuffer b, String where) {
        java.util.Map<Long, List<String>> classes = new java.util.HashMap<>();
        Rec stack = null;
        for (Rec x : records(b)) {
            if (x.type() == Ring.CLASS) {
                classes.put(b.getLong(x.off() + 8), strings(b, x.off() + 24, 3));
            } else if (x.type() == Ring.STACK) {
                stack = x;
            }
        }
        check(stack != null, "a STACK");
        List<String> c = classes.get(b.getLong(stack.off() + 24));
        equal(List.of("iormesh.testprobe.SecurityProbe", "app", where), c, "the probe's CLASS");
    }

    /** Loader labels and code sources: boot, platform, app; credentials stripped. */
    static void classLabels() throws Exception {
        equal("bootstrap", ClassIds.loader(null), "boot loader");
        equal("app", ClassIds.loader(RingTest.class.getClassLoader()), "app loader");
        equal("java.net.URLClassLoader", ClassIds.loader(
                new java.net.URLClassLoader(new java.net.URL[0])), "an unnamed loader");
        equal("jrt:/java.base", ClassIds.codeSource(String.class), "a java.base class");
        equal("jrt:/java.sql", ClassIds.codeSource(java.sql.Connection.class),
                "a platform loader class");
        equal("/opt/app/lib/x.jar", ClassIds.location(
                new java.net.URL("file:/opt/app/lib/x.jar")), "a file URL is its path");
        equal("https://repo.example:8443/lib/x.jar", ClassIds.location(
                new java.net.URL("https://user:secret@repo.example:8443/lib/x.jar?token=t#f")),
                "userinfo, query and fragment stripped");
        equal("jar:https://repo.example/x.jar!/", ClassIds.location(
                new java.net.URL("jar:https://u:p@repo.example/x.jar!/")),
                "a nested authority's userinfo stripped");
        equal("/srv/a@b/x?.jar", ClassIds.clean("/srv/a@b/x?.jar"), "a path is kept");
        equal("jar:file:/app.jar!/BOOT-INF/lib/x.jar!/", ClassIds.location(
                new java.net.URL("jar:file:/app.jar!/BOOT-INF/lib/x.jar!/")), "a nested jar");
        Class<?> proxy = java.lang.reflect.Proxy.newProxyInstance(RingTest.class.getClassLoader(),
                new Class<?>[] {Runnable.class}, (p, m, a) -> null).getClass();
        equal("", ClassIds.codeSource(proxy), "a generated class has none");
        Class<?> bootProxy = java.lang.reflect.Proxy.newProxyInstance(null,
                new Class<?>[] {Runnable.class}, (p, m, a) -> null).getClass();
        check(!ClassIds.codeSource(bootProxy).startsWith("jrt:"),
                "a dynamic module is not the runtime image");
    }

    /**
     * A later reader gets the dictionary again (task 5j4): the first
     * reader's epoch (1) changes nothing; the second's (2) makes the
     * watermark thread's resync write the hook table and a WATERMARK
     * closing it, and the same thread's next bracket on the same stack
     * writes its THREAD, the stack's CLASS records and its STACK again.
     * A repeated agent load (requestResync) resyncs without a new reader.
     */
    static void resync() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Hooks.configure(5, true, false);
        Hooks.openRing(dir.toString(), Ring.MIN_CAPACITY);   // a fresh table too
        Ring r = Hooks.ring;
        Hooks.describeHook(3, "write", "java/io/FileOutputStream", "write", "writeBytes", "write");
        ByteBuffer b = map(r.path());
        java.util.concurrent.CountDownLatch resynced = new java.util.concurrent.CountDownLatch(1);
        Watermarks.Resync s = new Watermarks.Resync();
        s.gapNs = 0;   // two resyncs back to back below
        int before;
        try {
            Thread t = new Thread(() -> {
                ClassProbe.run(RingTest::bracket);
                awaitQuietly(resynced);
                ClassProbe.run(RingTest::bracket);
            }, "ring-resync-thread");
            t.start();
            while (count(records(b), Ring.BRACKET) == 0) {
                Thread.sleep(1);
            }
            setEpoch(r.path(), 1);
            Watermarks.resync(r, s);
            before = records(b).size();
            setEpoch(r.path(), 2);
            Watermarks.resync(r, s);
            resynced.countDown();
            t.join();
        } finally {
            Hooks.ring = null;
        }
        List<Rec> recs = records(b);
        checkResent(recs.subList(before, recs.size()));
        Hooks.requestResync();
        Hooks.ring = r;
        try {
            Watermarks.resync(r, s);
        } finally {
            Hooks.ring = null;
        }
        equal(Ring.HOOK, records(b).get(recs.size()).type(), "a repeated load resends too");
        delete(dir);
    }

    /**
     * The records after the second reader's epoch: the hook table, the
     * resyncing thread's THREAD and the closing WATERMARK, then the app
     * thread's next bracket with its THREAD, CLASS records and STACK.
     */
    private static void checkResent(List<Rec> after) {
        equal(List.of(Ring.HOOK, Ring.THREAD, Ring.WATERMARK),
                after.subList(0, 3).stream().map(Rec::type).toList(), "resent table, closed");
        List<Integer> rest = after.subList(3, after.size()).stream().map(Rec::type).toList();
        equal(Ring.THREAD, rest.get(0), "the thread described again: " + rest);
        check(rest.contains(Ring.CLASS) && rest.indexOf(Ring.CLASS) < rest.indexOf(Ring.STACK),
                "CLASS records again, ahead of the STACK: " + rest);
        equal(Ring.BRACKET, rest.get(rest.size() - 1), "then the bracket: " + rest);
    }

    private static void bracket() {
        Hooks.enter(3);
        Hooks.exit(3);
    }

    private static void awaitQuietly(java.util.concurrent.CountDownLatch l) {
        try {
            l.await();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    private static long count(List<Rec> recs, int type) {
        return recs.stream().filter(x -> x.type() == type).count();
    }

    /**
     * A thread renamed after its first bracket writes another THREAD at
     * its next one (task fe4), and only then: an unchanged name is not
     * described again.
     */
    static void renamed() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(5, false, false);
        Hooks.ring = r;
        try {
            Thread t = new Thread(() -> {
                Hooks.enter(3);
                Hooks.exit(3);
                Thread.currentThread().setName("ring-renamed");
                Hooks.enter(3);
                Hooks.exit(3);
                Hooks.enter(3);
                Hooks.exit(3);
            }, "ring-before");
            t.start();
            t.join();
        } finally {
            Hooks.ring = null;
            Hooks.configure(5, true, false);
        }
        ByteBuffer b = map(r.path());
        List<Rec> recs = records(b);
        equal(List.of(Ring.THREAD, Ring.BRACKET, Ring.THREAD, Ring.BRACKET, Ring.BRACKET),
                recs.stream().map(Rec::type).toList(), "record order");
        equal(List.of("ring-before"), strings(b, recs.get(0).off() + 24, 1), "first name");
        equal(List.of("ring-renamed"), strings(b, recs.get(2).off() + 24, 1), "new name");
        equal(b.getLong(recs.get(0).off() + 8), b.getLong(recs.get(2).off() + 8), "same thread");
        delete(dir);
    }

    /** n strings (u32 length + UTF-8) from p. */
    private static List<String> strings(ByteBuffer b, int p, int n) {
        List<String> out = new ArrayList<>();
        for (int i = 0; i < n; i++) {
            byte[] s = new byte[b.getInt(p)];
            b.get(p + 4, s);
            out.add(new String(s, java.nio.charset.StandardCharsets.UTF_8));
            p += 4 + s.length;
        }
        return out;
    }

    /**
     * A virtual thread's THREAD carries VIRTUAL, and a bracket that
     * stayed on one carrier carries that carrier's tid. Skipped on 17–20,
     * which have no virtual threads.
     */
    static void virtualThread() throws Exception {
        java.lang.reflect.Method ofVirtual;
        try {
            ofVirtual = Thread.class.getMethod("ofVirtual");
        } catch (NoSuchMethodException e) {
            return;
        }
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Ring r = Ring.create(dir, Ring.MIN_CAPACITY);
        Hooks.configure(5, false, false);
        Hooks.ring = r;
        try {
            Object builder = ofVirtual.invoke(null);
            Runnable body = () -> {
                Hooks.enter(3);
                Hooks.exit(3);
            };
            // Through the public interface: the builder class is internal.
            Thread t = (Thread) Class.forName("java.lang.Thread$Builder")
                    .getMethod("start", Runnable.class).invoke(builder, body);
            t.join();
        } finally {
            Hooks.ring = null;
            Hooks.configure(5, true, false);
        }
        ByteBuffer b = map(r.path());
        List<Rec> recs = records(b);
        equal(List.of(Ring.THREAD, Ring.BRACKET), recs.stream().map(Rec::type).toList(),
                "record order");
        equal(Ring.VIRTUAL, b.getInt(recs.get(0).off() + 20), "THREAD flags");
        check(b.getInt(recs.get(1).off() + 12) > 0, "carrier tid of an unparked bracket");
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

    /**
     * A resend whose rows the full ring drops writes no watermark at all
     * (review of 5j4): the tracer takes the first WATERMARK after resent
     * rows as the table's end, so the tick's own watermark would close a
     * partial table and the tracer would probe only its natives. Once the
     * reader frees room, the next tick resends it whole and closes it.
     */
    static void partialResend() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Hooks.openRing(dir.toString(), Ring.MIN_CAPACITY);
        Ring r = Hooks.ring;
        try {
            Hooks.describeHook(3, "write", "java/io/FileOutputStream", "write", "writeBytes",
                    "write");
            ByteBuffer b = map(r.path());
            fillLeaving(r, b);   // room for a WATERMARK, not for a HOOK
            long head = b.getLong(Ring.HEAD);
            setEpoch(r.path(), 2);
            Watermarks.Resync s = new Watermarks.Resync();
            s.gapNs = 0;
            Watermarks.tick(r, s);
            check(s.due, "the dropped resend is still due");
            equal(head, b.getLong(Ring.HEAD), "nothing written, no watermark either");
            setHeader(r.path(), Ring.TAIL, head);   // the reader consumed it all
            Watermarks.tick(r, s);
            check(!s.due, "resent once there is room");
            check(b.getLong(Ring.HEAD) > head, "the table and its watermark written");
        } finally {
            Hooks.ring = null;
        }
        delete(dir);
    }

    /**
     * A reader too old to bump the epoch (a tracer before 5j4) still
     * consumed the dictionary: the first epoch bump after its drain moved
     * the tail resyncs, though the epoch only reached 1. Resyncs closer
     * than the gap wait for it instead of piling up.
     */
    static void oldReaderThenNew() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-ring-test");
        Hooks.openRing(dir.toString(), Ring.MIN_CAPACITY);
        Ring r = Hooks.ring;
        try {
            Watermarks.Resync s = new Watermarks.Resync();
            int g = Hooks.generation;
            r.watermark(0);
            setHeader(r.path(), Ring.TAIL, Ring.WATERMARK_LEN);   // the old reader's drain
            Watermarks.resync(r, s);
            equal(g, Hooks.generation, "no new reader yet");
            setEpoch(r.path(), 1);
            Watermarks.resync(r, s);
            equal(g + 1, Hooks.generation, "the new reader resynced");
            setEpoch(r.path(), 2);
            Watermarks.resync(r, s);
            equal(g + 1, Hooks.generation, "the next one waits for the gap");
            check(s.pending, "and stays pending");
            s.gapNs = 0;
            Watermarks.resync(r, s);
            equal(g + 2, Hooks.generation, "then resyncs");
        } finally {
            Hooks.ring = null;
        }
        delete(dir);
    }

    /**
     * Fills r with BRACKETs, then WATERMARKs, until 16 or 24 bytes are
     * left: a WATERMARK (16) fits, a HOOK or THREAD (over 24) does not.
     */
    private static void fillLeaving(Ring r, ByteBuffer b) {
        long cap = Ring.MIN_CAPACITY;
        while (cap - b.getLong(Ring.HEAD) > Ring.BRACKET_LEN + 24) {
            r.bracket(1, 1, 0, 0, 0, 0);
        }
        while (cap - b.getLong(Ring.HEAD) > 24) {
            r.watermark(0);
        }
    }

    /** Sets the ring's reader epoch, as internal/agentspool's Open bumps it. */
    private static void setEpoch(Path p, long epoch) throws IOException {
        setHeader(p, Ring.EPOCH, epoch);
    }

    /** Sets one u64 header word, as the reader would. */
    private static void setHeader(Path p, int off, long v) throws IOException {
        try (FileChannel ch = FileChannel.open(p, StandardOpenOption.READ,
                StandardOpenOption.WRITE)) {
            ch.map(FileChannel.MapMode.READ_WRITE, 0, Ring.DATA).order(ByteOrder.LITTLE_ENDIAN)
                    .putLong(off, v);
        }
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
