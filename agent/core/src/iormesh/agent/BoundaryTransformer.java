package iormesh.agent;

import iormesh.agent.shaded.asm.ClassReader;
import iormesh.agent.shaded.asm.ClassVisitor;
import iormesh.agent.shaded.asm.ClassWriter;
import iormesh.agent.shaded.asm.Label;
import iormesh.agent.shaded.asm.MethodVisitor;
import iormesh.agent.shaded.asm.Opcodes;
import java.lang.instrument.ClassFileTransformer;
import java.security.ProtectionDomain;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Rewrites the bodies of the hook table's methods — and nothing else —
 * so each call is bracketed by {@code Hooks.enter(id)} and
 * {@code Hooks.exit(id)}:
 *
 * <pre>
 *   Hooks.enter(id);
 *   try { original body, with Hooks.exit(id) before every return }
 *   catch (Throwable t) { Hooks.exit(id); throw t; }
 * </pre>
 *
 * Body rewrites only: the JDK's I/O classes are loaded before any agent
 * runs, and retransformation may not add, remove or rename methods
 * (S1), so the native-method-prefix trick is out.
 *
 * Uses ASM, shaded under iormesh.agent.shaded (the S1 prototype used
 * java.lang.classfile, final only in JDK 24; the agent runs on 17+,
 * decision #29). Frames are not recomputed: the one new frame — the
 * catch-all handler's, appended after the original code — is written by
 * hand, so ASM never needs ClassWriter.getCommonSuperClass, which would
 * load classes from inside a transformer.
 */
final class BoundaryTransformer implements ClassFileTransformer {
    static final String HOOKS = "iormesh/bridge/Hooks";
    private static final String INT_VOID = "(I)V";

    private final HookTable table;
    private final Set<Integer> applied = ConcurrentHashMap.newKeySet();
    private final Set<String> seen = ConcurrentHashMap.newKeySet();

    BoundaryTransformer(HookTable table) {
        this.table = table;
    }

    /**
     * Instrumentation entry point. Only bootstrap-loaded classes are
     * candidates: every listed class is a JDK class, and a same-named
     * class of another loader is not the JDK's.
     */
    @Override
    public byte[] transform(Module module, ClassLoader loader, String className,
                            Class<?> redefined, ProtectionDomain pd, byte[] bytes) {
        if (loader != null) {
            return null;
        }
        try {
            return rewrite(className, bytes);
        } catch (Throwable t) {
            // A failed rewrite must never break the target: leave the
            // class as it is (no attribution for its hooks) and say so.
            System.err.println("iormesh-agent: not rewriting " + className + ": " + t);
            return null;
        }
    }

    /**
     * Returns the rewritten class, or null (meaning "unchanged" to the
     * JVM) when the class is not in the table or none of its listed
     * methods is present.
     */
    byte[] rewrite(String className, byte[] bytes) {
        if (className == null || !table.hasClass(className)) {
            return null;
        }
        seen.add(className);
        ClassReader reader = new ClassReader(bytes);
        // Passing the reader lets ASM copy unchanged methods verbatim;
        // COMPUTE_MAXS only adds the bracket's stack slots.
        ClassWriter writer = new ClassWriter(reader, ClassWriter.COMPUTE_MAXS);
        Rewriter rewriter = new Rewriter(writer, className);
        reader.accept(rewriter, 0);
        return rewriter.rewritten == 0 ? null : writer.toByteArray();
    }

    /**
     * Listed classes the transformer has been handed so far; a row of
     * such a class that is not {@link #applied} names a method this JDK
     * does not have — a table/JDK mismatch, not a class not loaded yet.
     */
    Set<String> seen() {
        return Set.copyOf(seen);
    }

    /** Ids of the hooks whose method has been rewritten so far. */
    Set<Integer> applied() {
        return Set.copyOf(applied);
    }

    /**
     * Runs one rewrite of this class's own bytes, so ASM's classes are
     * loaded and linked before any JDK class is retransformed — their
     * loading reads the agent jar, which must not happen inside the
     * target's hooked I/O.
     */
    static void warmUp() throws java.io.IOException {
        String self = BoundaryTransformer.class.getName().replace('.', '/');
        HookTable t = HookTable.parse("warm-up", java.util.List.of(
                "1\twarm-up\t" + self + "\twarmUp\t()V\tnone\tnone"));
        try (java.io.InputStream in = BoundaryTransformer.class
                .getResourceAsStream("BoundaryTransformer.class")) {
            if (in == null || new BoundaryTransformer(t).rewrite(self, in.readAllBytes()) == null) {
                throw new java.io.IOException("transformer warm-up rewrote nothing");
            }
        }
    }

    /** Wraps the listed methods of one class. */
    private final class Rewriter extends ClassVisitor {
        private final String className;
        private int version;
        int rewritten;

        Rewriter(ClassVisitor next, String className) {
            super(Opcodes.ASM9, next);
            this.className = className;
        }

        @Override
        public void visit(int version, int access, String name, String signature,
                          String superName, String[] interfaces) {
            this.version = version & 0xffff;
            super.visit(version, access, name, signature, superName, interfaces);
        }

        @Override
        public MethodVisitor visitMethod(int access, String name, String descriptor,
                                         String signature, String[] exceptions) {
            MethodVisitor mv = super.visitMethod(access, name, descriptor, signature, exceptions);
            HookTable.Hook h = table.lookup(className, name, descriptor);
            if (h == null || (access & (Opcodes.ACC_ABSTRACT | Opcodes.ACC_NATIVE)) != 0
                    || name.equals("<init>")) {
                return mv;   // not listed, or no body to rewrite
            }
            rewritten++;
            applied.add(h.id());
            return new Bracket(mv, h.id(), version >= Opcodes.V1_6);
        }
    }

    /**
     * Brackets one method body: enter at the start, exit before each
     * return, and a catch-all handler (exit, rethrow) covering the
     * original code. The handler is appended last, so the method's own
     * handlers, earlier in the exception table, still win.
     */
    private static final class Bracket extends MethodVisitor {
        private final int id;
        private final boolean frames;
        private final Label start = new Label();
        private final Label handler = new Label();

        Bracket(MethodVisitor next, int id, boolean frames) {
            super(Opcodes.ASM9, next);
            this.id = id;
            this.frames = frames;
        }

        @Override
        public void visitCode() {
            super.visitCode();
            callHook("enter");
            // After the enter call: a branch back to the original first
            // instruction must not open a second bracket.
            super.visitLabel(start);
        }

        @Override
        public void visitInsn(int opcode) {
            if (opcode >= Opcodes.IRETURN && opcode <= Opcodes.RETURN) {
                callHook("exit");
            }
            super.visitInsn(opcode);
        }

        @Override
        public void visitMaxs(int maxStack, int maxLocals) {
            // The original code never falls through to here (it ends in a
            // return, throw or goto), so the handler is reached only by
            // an exception: stack [Throwable], no locals it needs.
            super.visitLabel(handler);
            if (frames) {
                super.visitFrame(Opcodes.F_FULL, 0, null, 1, new Object[] {"java/lang/Throwable"});
            }
            callHook("exit");
            super.visitInsn(Opcodes.ATHROW);
            super.visitTryCatchBlock(start, handler, handler, null);
            super.visitMaxs(maxStack, maxLocals);
        }

        private void callHook(String name) {
            pushInt(id);
            super.visitMethodInsn(Opcodes.INVOKESTATIC, HOOKS, name, INT_VOID, false);
        }

        private void pushInt(int v) {
            if (v >= -1 && v <= 5) {
                super.visitInsn(Opcodes.ICONST_0 + v);
            } else if (v >= Byte.MIN_VALUE && v <= Byte.MAX_VALUE) {
                super.visitIntInsn(Opcodes.BIPUSH, v);
            } else {
                super.visitIntInsn(Opcodes.SIPUSH, v);   // ids are <= Short.MAX_VALUE
            }
        }
    }
}
