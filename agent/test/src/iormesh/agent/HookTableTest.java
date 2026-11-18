package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;
import static iormesh.agent.AgentTests.rejects;

import iormesh.agent.shaded.asm.ClassReader;
import iormesh.agent.shaded.asm.ClassVisitor;
import iormesh.agent.shaded.asm.MethodVisitor;
import iormesh.agent.shaded.asm.Opcodes;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Set;

/** HookTable: parsing, variant choice, and the tables against the JDK. */
final class HookTableTest {
    private HookTableTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("hooktable/variantFor", HookTableTest::variantFor),
                new AgentTests.Case("hooktable/bundled tables load", HookTableTest::bundled),
                new AgentTests.Case("hooktable/rejects bad rows", HookTableTest::badRows),
                new AgentTests.Case("hooktable/matches the running JDK",
                        HookTableTest::matchesRunningJdk),
                new AgentTests.Case("hooktable/JDK check catches wrong rows",
                        HookTableTest::wrongRows));
    }

    static void variantFor() {
        equal(null, HookTable.variantFor(11), "11");
        equal("jdk17", HookTable.variantFor(17), "17");
        equal("jdk17", HookTable.variantFor(20), "20");
        equal("jdk21", HookTable.variantFor(21), "21");
        equal("jdk21", HookTable.variantFor(24), "24");
        equal("jdk25", HookTable.variantFor(25), "25");
        equal("jdk25", HookTable.variantFor(27), "27");
    }

    static void bundled() throws Exception {
        for (String v : List.of("jdk17", "jdk21", "jdk25")) {
            HookTable t = HookTable.load(v);
            check(t.hooks().size() > 30, v + " has " + t.hooks().size() + " rows");
            check(t.hasClass("java/io/FileOutputStream"), v + " lists FileOutputStream");
            check(t.lookup("java/io/FileOutputStream", "write", "([BII)V") != null,
                    v + " lists FileOutputStream.write([BII)V");
        }
        // The variants differ where S1 found the JDKs differ (#29).
        check(HookTable.load("jdk21").hasClass("sun/nio/ch/UnixDispatcher"), "21 UnixDispatcher");
        check(!HookTable.load("jdk17").hasClass("sun/nio/ch/UnixDispatcher"), "17 no UnixDispatcher");
        // 21–24 keep Net.serverSocket's stream flag; 25 dropped it.
        String net = "sun/nio/ch/Net";
        String pf = "(Ljava/net/ProtocolFamily;";
        String fd = ")Ljava/io/FileDescriptor;";
        check(HookTable.load("jdk21").lookup(net, "serverSocket", pf + "Z" + fd) != null,
                "21 serverSocket(ProtocolFamily, boolean)");
        check(HookTable.load("jdk25").lookup(net, "serverSocket", pf + fd) != null,
                "25 serverSocket(ProtocolFamily)");
        check(HookTable.load("jdk25").lookup(net, "serverSocket", pf + "Z" + fd) == null,
                "25 no serverSocket(ProtocolFamily, boolean)");
    }

    static void badRows() throws Exception {
        String ok = "1\tread\ta/B\tm\t()V\tm0\tread";
        rejects("columns", () -> HookTable.parse("t", List.of("1\tread\ta/B")));
        rejects("duplicate id", () -> HookTable.parse("t", List.of(ok,
                "1\tread\ta/B\tn\t()V\tn0\tread")));
        rejects("duplicate method", () -> HookTable.parse("t", List.of(ok,
                "2\tread\ta/B\tm\t()V\tm0\tread")));
        rejects("id out of range", () -> HookTable.parse("t", List.of(
                "0\tread\ta/B\tm\t()V\tm0\tread")));
        rejects("bad id", () -> HookTable.parse("t", List.of(
                "x\tread\ta/B\tm\t()V\tm0\tread")));
        rejects("empty column", () -> HookTable.parse("t", List.of(
                "1\t\ta/B\tm\t()V\tm0\tread")));
        rejects("unknown kind", () -> HookTable.parse("t", List.of(
                "1\treed\ta/B\tm\t()V\tm0\tread")));
        rejects("bad descriptor", () -> HookTable.parse("t", List.of(
                "1\tread\ta/B\tm\tV\tm0\tread")));
        HookTable t = HookTable.parse("t", List.of("# comment", "", ok));
        equal(1, t.hooks().size(), "comments skipped");
        equal(List.of("read"), t.hooks().get(0).syscalls(), "syscalls");
    }

    /**
     * Every row of the running JDK's variant names a method that exists,
     * has a body, and reaches the row's native, which must be a native
     * method of its class (by name) — the check that a table
     * still fits a JDK (decision #29). Run it under each JDK of
     * IORMESH_AGENT_JDKS to check the other variants.
     *
     * "Reaches" allows a short detour through methods of the same class:
     * JDK 24 and later route RandomAccessFile.write through implWrite
     * (a JFR FileWriteEvent split) where 21 calls write0 directly, and
     * hooking write keeps one row valid for both — jdk21 covers 21–24 —
     * while the bracket still holds only the native's I/O.
     */
    static void matchesRunningJdk() throws Exception {
        HookTable t = HookTable.load(HookTable.variantFor(Runtime.version().feature()));
        List<String> bad = new ArrayList<>();
        for (HookTable.Hook h : t.hooks()) {
            String problem = checkRow(h);
            if (problem != null) {
                bad.add(h.id() + " " + h.key() + ": " + problem);
            }
        }
        check(bad.isEmpty(), t.variant() + " vs java " + Runtime.version() + ":\n  "
                + String.join("\n  ", bad));
    }

    /** Same-class call hops allowed between a hooked method and its native. */
    private static final int MAX_HOPS = 3;

    private static String checkRow(HookTable.Hook h) throws Exception {
        CallGraph g = CallGraph.of(h.owner());
        if (g == null) {
            return "no such class";
        }
        String self = h.method() + h.descriptor();
        if (!g.calls.containsKey(self)) {
            return g.noBody.contains(self) ? "no body (native or abstract)" : "no such method";
        }
        int dot = h.nativeRef().lastIndexOf('.');
        String owner = dot < 0 ? h.owner() : h.nativeRef().substring(0, dot);
        String name = h.nativeRef().substring(dot + 1);
        CallGraph target = owner.equals(h.owner()) ? g : CallGraph.of(owner);
        if (target == null || !target.natives.contains(name)) {
            return owner + "." + name + " is not a native method";
        }
        return g.reaches(self, owner + "." + name, MAX_HOPS) ? null
                : "does not reach " + owner + "." + name;
    }

    /**
     * Rows that must fail the JDK check: a "native" that is plain Java,
     * a native the method never calls, and a method that is not there.
     */
    static void wrongRows() throws Exception {
        String fis = "java/io/FileInputStream";
        List<String> rows = List.of(
                "1\tread\t" + fis + "\tread\t([BII)I\tclose\tread",
                "2\tread\t" + fis + "\tread\t([BII)I\topen0\tread",
                "3\tread\t" + fis + "\tread\t(J)I\treadBytes\tread");
        // One table per row: a table may not list a method twice.
        for (String row : rows) {
            HookTable.Hook h = HookTable.parse("wrong", List.of(row)).hooks().get(0);
            check(checkRow(h) != null, "row " + h.id() + " passed the JDK check");
        }
    }

    /**
     * One class's methods with bodies, each with the methods it calls
     * ("owner.name"), plus the bodiless ones.
     */
    private static final class CallGraph extends ClassVisitor {
        final Map<String, Set<String>> calls = new HashMap<>();
        final Set<String> noBody = new HashSet<>();
        final Set<String> natives = new HashSet<>();
        private final String owner;

        private CallGraph(String owner) {
            super(Opcodes.ASM9);
            this.owner = owner;
        }

        static CallGraph of(String owner) throws Exception {
            try (InputStream in = ClassLoader.getSystemResourceAsStream(owner + ".class")) {
                if (in == null) {
                    return null;
                }
                CallGraph g = new CallGraph(owner);
                new ClassReader(in.readAllBytes()).accept(g,
                        ClassReader.SKIP_DEBUG | ClassReader.SKIP_FRAMES);
                return g;
            }
        }

        @Override
        public MethodVisitor visitMethod(int access, String n, String desc, String sig,
                                         String[] exc) {
            if ((access & Opcodes.ACC_NATIVE) != 0) {
                natives.add(n);
            }
            if ((access & (Opcodes.ACC_NATIVE | Opcodes.ACC_ABSTRACT)) != 0) {
                noBody.add(n + desc);
                return null;
            }
            Set<String> out = new HashSet<>();
            calls.put(n + desc, out);
            return new MethodVisitor(Opcodes.ASM9) {
                @Override
                public void visitMethodInsn(int op, String o, String m, String d, boolean itf) {
                    out.add(o + "." + m + (o.equals(owner) ? d : ""));
                }
            };
        }

        /**
         * Whether method (name+desc) calls target ("owner.name") within
         * hops levels of same-class calls.
         */
        boolean reaches(String method, String target, int hops) {
            Set<String> callees = calls.getOrDefault(method, Set.of());
            for (String c : callees) {
                String plain = c.indexOf('(') < 0 ? c : c.substring(0, c.indexOf('('));
                if (plain.equals(target)) {
                    return true;
                }
            }
            if (hops == 0) {
                return false;
            }
            for (String c : callees) {
                if (c.startsWith(owner + ".") && c.indexOf('(') > 0
                        && reaches(c.substring(owner.length() + 1), target, hops - 1)) {
                    return true;
                }
            }
            return false;
        }
    }
}
