package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.shaded.asm.ClassReader;
import iormesh.agent.shaded.asm.ClassVisitor;
import iormesh.agent.shaded.asm.Label;
import iormesh.agent.shaded.asm.MethodVisitor;
import iormesh.agent.shaded.asm.Opcodes;
import iormesh.bridge.Hooks;
import java.io.InputStream;
import java.lang.reflect.InvocationTargetException;
import java.lang.reflect.Method;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.function.Consumer;

/**
 * BoundaryTransformer against Fixture: only listed methods change, the
 * changed ones still verify and behave, every way out closes the bracket,
 * and classes or loaders outside the table are left alone.
 */
final class BoundaryTransformerTest {
    private static final String FIXTURE = "iormesh/agent/Fixture";
    private static final List<String> TABLE = List.of(
            row(1, "value", "()I"),
            row(2, "throwing", "()V"),
            row(3, "withCatch", "(I)I"),
            row(4, "loop", "(I)J"),
            row(5, "early", "(ZLjava/lang/StringBuilder;)V"));

    private BoundaryTransformerTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("transformer/rewrites only listed methods",
                        BoundaryTransformerTest::onlyListed),
                new AgentTests.Case("transformer/rewritten methods run and close brackets",
                        BoundaryTransformerTest::runs),
                new AgentTests.Case("transformer/unknown class is a no-op",
                        BoundaryTransformerTest::unknownClass),
                new AgentTests.Case("transformer/no listed method is a no-op",
                        BoundaryTransformerTest::noListedMethod),
                new AgentTests.Case("transformer/non-bootstrap loader is a no-op",
                        BoundaryTransformerTest::otherLoader),
                new AgentTests.Case("transformer/warm-up", BoundaryTransformer::warmUp));
    }

    private static String row(int id, String method, String desc) {
        return id + "\tread\t" + FIXTURE + "\t" + method + "\t" + desc + "\tnone\tnone";
    }

    private static BoundaryTransformer transformer(List<String> rows) {
        return new BoundaryTransformer(HookTable.parse("test", rows));
    }

    static byte[] fixtureBytes() throws Exception {
        try (InputStream in = Fixture.class.getResourceAsStream("Fixture.class")) {
            return in.readAllBytes();
        }
    }

    static void onlyListed() throws Exception {
        byte[] before = fixtureBytes();
        HookTable table = HookTable.parse("test", TABLE);
        BoundaryTransformer t = new BoundaryTransformer(table);
        byte[] after = t.rewrite(FIXTURE, before);
        check(after != null, "listed class rewritten");
        Map<String, String> was = listing(before);
        Map<String, String> now = listing(after);
        equal(was.keySet(), now.keySet(), "methods (none added or removed)");
        for (String m : was.keySet()) {
            int paren = m.indexOf('(');
            boolean listed = table.lookup(FIXTURE, m.substring(0, paren), m.substring(paren)) != null;
            boolean hooked = now.get(m).contains(BoundaryTransformer.HOOKS + ".enter");
            equal(listed, hooked, m + " hooked");
            if (!listed) {
                equal(was.get(m), now.get(m), m + " unchanged");
            }
        }
        equal(java.util.Set.of(1, 2, 3, 4, 5), t.applied(), "applied ids");
        equal(java.util.Set.of(FIXTURE), t.seen(), "seen classes");
    }

    /**
     * Loads the rewritten Fixture in its own loader (so it is verified)
     * and calls every method inside an outer bracket (hook 7) opened by
     * hand: a rewritten method that exited twice would close hook 7's
     * bracket early, which its count of 1 rules out.
     */
    static void runs() throws Exception {
        byte[] after = transformer(TABLE).rewrite(FIXTURE, fixtureBytes());
        Class<?> c = new OneClassLoader("iormesh.agent.Fixture", after)
                .loadClass("iormesh.agent.Fixture");
        Hooks.configure(8, true, true);
        Hooks.enter(7);
        callFixture(c);
        Hooks.exit(7);
        long[] n = Hooks.counts();
        equal(List.of(0L, 1L, 1L, 2L, 1L, 2L, 0L, 1L), List.of(n[0], n[1], n[2], n[3], n[4],
                n[5], n[6], n[7]), "closed brackets per hook (7 = the outer one)");
    }

    private static void callFixture(Class<?> c) throws Exception {
        Object f = c.getConstructor().newInstance();
        equal(7, c.getMethod("value").invoke(f), "value()");
        equal(9, c.getMethod("value", int.class).invoke(f, 2), "value(int)");
        equal(14, c.getMethod("unlisted").invoke(f), "unlisted()");
        equal(5, c.getMethod("withCatch", int.class).invoke(null, 2), "withCatch(2)");
        equal(-1, c.getMethod("withCatch", int.class).invoke(null, 0), "withCatch(0)");
        equal(45L, c.getMethod("loop", int.class).invoke(null, 10), "loop(10)");
        StringBuilder sb = new StringBuilder();
        Method early = c.getMethod("early", boolean.class, StringBuilder.class);
        early.invoke(null, true, sb);
        early.invoke(null, false, sb);
        equal("ran", sb.toString(), "early");
        try {
            c.getMethod("throwing").invoke(f);
            throw new AssertionError("throwing() returned");
        } catch (InvocationTargetException e) {
            equal(IllegalStateException.class, e.getCause().getClass(), "rethrown as is");
        }
    }

    static void unknownClass() throws Exception {
        BoundaryTransformer t = transformer(TABLE);
        equal(null, t.rewrite("iormesh/agent/NotListed", fixtureBytes()), "unlisted class");
        equal(null, t.rewrite(null, fixtureBytes()), "nameless class");
        check(t.applied().isEmpty() && t.seen().isEmpty(), "nothing recorded");
    }

    static void noListedMethod() throws Exception {
        BoundaryTransformer t = transformer(List.of(row(1, "value", "(J)I"), row(2, "gone", "()V")));
        equal(null, t.rewrite(FIXTURE, fixtureBytes()), "descriptor mismatch");
        equal(java.util.Set.of(FIXTURE), t.seen(), "seen, so its rows report missing");
        check(t.applied().isEmpty(), "nothing applied");
    }

    static void otherLoader() throws Exception {
        BoundaryTransformer t = transformer(TABLE);
        equal(null, t.transform(null, BoundaryTransformerTest.class.getClassLoader(), FIXTURE,
                null, null, fixtureBytes()), "app-loader class");
        check(t.transform(null, null, FIXTURE, null, null, fixtureBytes()) != null,
                "bootstrap (null) loader is rewritten");
    }

    /**
     * Method key ("name(desc)") → its instructions and handlers as text
     * (jump targets by kind only), enough to tell a method was changed.
     */
    private static Map<String, String> listing(byte[] cls) {
        Map<String, String> out = new TreeMap<>();
        new ClassReader(cls).accept(new ClassVisitor(Opcodes.ASM9) {
            @Override
            public MethodVisitor visitMethod(int acc, String name, String desc, String sig,
                                             String[] exc) {
                return new InsnText(text -> out.put(name + desc, text));
            }
        }, ClassReader.SKIP_DEBUG);
        return out;
    }

    /** One method's instructions as text, handed to sink at its end. */
    private static final class InsnText extends MethodVisitor {
        private final Consumer<String> sink;
        private final StringBuilder sb = new StringBuilder();

        InsnText(Consumer<String> sink) {
            super(Opcodes.ASM9);
            this.sink = sink;
        }

        @Override
        public void visitEnd() {
            sink.accept(sb.toString());
        }

        @Override
        public void visitInsn(int op) {
            sb.append(op).append(';');
        }

        @Override
        public void visitIntInsn(int op, int operand) {
            sb.append(op).append(' ').append(operand).append(';');
        }

        @Override
        public void visitVarInsn(int op, int var) {
            sb.append(op).append(" v").append(var).append(';');
        }

        @Override
        public void visitTypeInsn(int op, String type) {
            sb.append(op).append(' ').append(type).append(';');
        }

        @Override
        public void visitFieldInsn(int op, String o, String n, String d) {
            sb.append(op).append(' ').append(o).append('.').append(n).append(';');
        }

        @Override
        public void visitMethodInsn(int op, String o, String m, String d, boolean i) {
            sb.append(o).append('.').append(m).append(d).append(';');
        }

        @Override
        public void visitJumpInsn(int op, Label l) {
            sb.append(op).append(" jump;");
        }

        @Override
        public void visitLdcInsn(Object v) {
            sb.append("ldc ").append(v).append(';');
        }

        @Override
        public void visitIincInsn(int var, int inc) {
            sb.append("iinc v").append(var).append(' ').append(inc).append(';');
        }

        @Override
        public void visitTryCatchBlock(Label a, Label b, Label h, String type) {
            sb.append("try ").append(type).append(';');
        }
    }

    /** Defines one class from bytes, delegating everything else. */
    static final class OneClassLoader extends ClassLoader {
        private final String name;
        private final byte[] bytes;

        OneClassLoader(String name, byte[] bytes) {
            super(BoundaryTransformerTest.class.getClassLoader());
            this.name = name;
            this.bytes = bytes;
        }

        @Override
        protected Class<?> loadClass(String n, boolean resolve) throws ClassNotFoundException {
            if (!n.equals(name)) {
                return super.loadClass(n, resolve);
            }
            synchronized (getClassLoadingLock(n)) {
                Class<?> c = findLoadedClass(n);
                return c != null ? c : defineClass(n, bytes, 0, bytes.length);
            }
        }
    }
}
