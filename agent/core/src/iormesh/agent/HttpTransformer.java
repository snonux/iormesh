package iormesh.agent;

import iormesh.agent.http.Target;
import iormesh.agent.shaded.asm.ClassReader;
import iormesh.agent.shaded.asm.ClassVisitor;
import iormesh.agent.shaded.asm.ClassWriter;
import iormesh.agent.shaded.asm.Label;
import iormesh.agent.shaded.asm.MethodVisitor;
import iormesh.agent.shaded.asm.Opcodes;
import iormesh.agent.shaded.asm.Type;
import java.lang.instrument.ClassFileTransformer;
import java.security.ProtectionDomain;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Rewrites the HTTP hooks' methods (HttpHooks) so each exit hands what
 * the method has to {@code Hooks.http(ret, this, arg, id)}:
 *
 * <pre>
 *   try { original body, with Hooks.http(returned value, this, arg, id)
 *         before every return }
 *   catch (Throwable t) { Hooks.http(null, this, arg, id); throw t; }
 * </pre>
 *
 * An HTTP event is stamped when the request is done, and nothing joins
 * to its window, so most targets are exit only. A stamped target (the
 * method that begins a request, Target.stamped, task wd4) also calls
 * {@code Hooks.httpEnter(this, id)} first thing, and its exits call
 * {@code Hooks.httpExit} instead, which pops that stamp: the request's
 * start, for its latency. A constructor is hooked at its returns alone
 * (on its exceptional path this may still be uninitialized) and never
 * stamped — an unhooked throw would leave its stamp on the thread. Like BoundaryTransformer, body rewrites with a
 * hand-written handler frame (no COMPUTE_FRAMES, which would load
 * classes from inside the transformer); unlike it, classes of the
 * platform loader are candidates too — java.net.http and jdk.httpserver
 * are defined there.
 */
final class HttpTransformer implements ClassFileTransformer {
    static final String HTTP_DESC = "(Ljava/lang/Object;Ljava/lang/Object;Ljava/lang/Object;I)V";
    static final String ENTER_DESC = "(Ljava/lang/Object;I)V";

    private final HttpHooks hooks;
    private final Set<Integer> applied = ConcurrentHashMap.newKeySet();
    private final Set<String> seen = ConcurrentHashMap.newKeySet();

    HttpTransformer(HttpHooks hooks) {
        this.hooks = hooks;
    }

    @Override
    public byte[] transform(Module module, ClassLoader loader, String className,
                            Class<?> redefined, ProtectionDomain pd, byte[] bytes) {
        if (loader != null && loader != ClassLoader.getPlatformClassLoader()) {
            return null;
        }
        try {
            return rewrite(className, bytes);
        } catch (Throwable t) {
            System.err.println("iormesh-agent: not rewriting " + className + ": " + t);
            return null;
        }
    }

    /** The rewritten class, or null when none of its methods is listed. */
    byte[] rewrite(String className, byte[] bytes) {
        if (className == null || !hooks.hasClass(className)) {
            return null;
        }
        seen.add(className);
        ClassReader reader = new ClassReader(bytes);
        ClassWriter writer = new ClassWriter(reader, ClassWriter.COMPUTE_MAXS);
        Rewriter rewriter = new Rewriter(writer, className);
        reader.accept(rewriter, 0);
        return rewriter.rewritten == 0 ? null : writer.toByteArray();
    }

    HttpHooks hooks() {
        return hooks;
    }

    Set<String> seen() {
        return Set.copyOf(seen);
    }

    Set<Integer> applied() {
        return Set.copyOf(applied);
    }

    /**
     * Rewrites this class's own hooks() (an instance method: static ones
     * are never targets), so the rewriting classes are loaded before any
     * target class is: their loading reads the agent jar, which must not
     * happen inside the target's request.
     */
    static void warmUp() throws java.io.IOException {
        String self = HttpTransformer.class.getName().replace('.', '/');
        HttpHooks h = HttpHooks.of(java.util.List.of(new Target(self, "hooks",
                "()Liormesh/agent/HttpHooks;", Target.NO_ARG, null)));
        try (java.io.InputStream in = HttpTransformer.class
                .getResourceAsStream("HttpTransformer.class")) {
            if (in == null || new HttpTransformer(h).rewrite(self, in.readAllBytes()) == null) {
                throw new java.io.IOException("http transformer warm-up rewrote nothing");
            }
        }
    }

    /** Finds the listed methods of one class. */
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
            HttpHooks.Hook h = hooks.lookup(className, name, descriptor);
            if (h == null || (access & (Opcodes.ACC_ABSTRACT | Opcodes.ACC_NATIVE
                    | Opcodes.ACC_STATIC)) != 0) {
                return mv;   // not listed, no body, or no this to hand over
            }
            int slot = argSlot(descriptor, h.target().arg());
            if (slot == 0) {
                return mv;   // the named argument is a primitive: nothing to pass
            }
            rewritten++;
            applied.add(h.id());
            boolean ctor = name.equals("<init>");
            return new Exit(mv, h.id(), className, slot, argType(descriptor, h.target().arg()),
                    ctor, h.target().stamp() && !ctor, version >= Opcodes.V1_6);
        }
    }

    /**
     * The local slot of the argument index arg names (this is slot 0),
     * -1 for none, 0 when it is not a reference (refused).
     */
    static int argSlot(String descriptor, int arg) {
        Type[] args = Type.getArgumentTypes(descriptor);
        int i = arg == Target.LAST_ARG ? args.length - 1 : arg;
        if (arg == Target.NO_ARG || i < 0 || i >= args.length) {
            return -1;
        }
        int sort = args[i].getSort();
        if (sort != Type.OBJECT && sort != Type.ARRAY) {
            return 0;
        }
        int slot = 1;
        for (int k = 0; k < i; k++) {
            slot += args[k].getSize();
        }
        return slot;
    }

    /** The internal name of that argument's type, for the handler's frame. */
    private static String argType(String descriptor, int arg) {
        Type[] args = Type.getArgumentTypes(descriptor);
        int i = arg == Target.LAST_ARG ? args.length - 1 : arg;
        return i >= 0 && i < args.length ? args[i].getInternalName() : null;
    }

    /**
     * Calls Hooks.http (Hooks.httpExit when stamped) before every return
     * and, for a method, on its throw; a stamped one calls Hooks.httpEnter
     * first.
     */
    private static final class Exit extends MethodVisitor {
        private final int id;
        private final String owner;
        private final int slot;
        private final String slotType;
        private final boolean ctor;
        private final boolean stamp;
        private final boolean frames;
        private final Label start = new Label();
        private final Label handler = new Label();

        Exit(MethodVisitor next, int id, String owner, int slot, String slotType, boolean ctor,
             boolean stamp, boolean frames) {
            super(Opcodes.ASM9, next);
            this.id = id;
            this.owner = owner;
            this.slot = slot;
            this.slotType = slotType;
            this.ctor = ctor;
            this.stamp = stamp;
            this.frames = frames;
        }

        /**
         * The entry stamp goes before the try range: httpEnter never
         * throws, and were it to, its exit must not pop a stamp never
         * pushed.
         */
        @Override
        public void visitCode() {
            super.visitCode();
            if (stamp) {
                super.visitVarInsn(Opcodes.ALOAD, 0);
                pushId();
                super.visitMethodInsn(Opcodes.INVOKESTATIC, BoundaryTransformer.HOOKS,
                        "httpEnter", ENTER_DESC, false);
            }
            super.visitLabel(start);
        }

        @Override
        public void visitInsn(int opcode) {
            if (opcode == Opcodes.ARETURN) {
                super.visitInsn(Opcodes.DUP);
                callHook();
            } else if (opcode >= Opcodes.IRETURN && opcode <= Opcodes.RETURN) {
                super.visitInsn(Opcodes.ACONST_NULL);
                callHook();
            }
            super.visitInsn(opcode);
        }

        @Override
        public void visitMaxs(int maxStack, int maxLocals) {
            if (!ctor) {
                handler();
            }
            super.visitMaxs(maxStack, maxLocals);
        }

        /**
         * The catch-all, appended after the original code (its own
         * handlers come first in the table and still win). Its frame
         * declares only what it loads: this, and the passed argument's
         * slot — javac never stores another type into a parameter slot,
         * and the slots between are TOP.
         */
        private void handler() {
            super.visitLabel(handler);
            if (frames) {
                int n = slot > 0 ? slot + 1 : 1;
                Object[] locals = new Object[n];
                java.util.Arrays.fill(locals, Opcodes.TOP);
                locals[0] = owner;
                if (slot > 0) {
                    locals[slot] = slotType;
                }
                super.visitFrame(Opcodes.F_FULL, n, locals, 1,
                        new Object[] {"java/lang/Throwable"});
            }
            super.visitInsn(Opcodes.ACONST_NULL);
            callHook();
            super.visitInsn(Opcodes.ATHROW);
            super.visitTryCatchBlock(start, handler, handler, null);
        }

        /** Stack: [.., ret] → pushes this, arg and id, calls Hooks.http. */
        private void callHook() {
            super.visitVarInsn(Opcodes.ALOAD, 0);
            if (slot > 0) {
                super.visitVarInsn(Opcodes.ALOAD, slot);
            } else {
                super.visitInsn(Opcodes.ACONST_NULL);
            }
            pushId();
            super.visitMethodInsn(Opcodes.INVOKESTATIC, BoundaryTransformer.HOOKS,
                    stamp ? "httpExit" : "http", HTTP_DESC, false);
        }

        private void pushId() {
            if (id <= Byte.MAX_VALUE) {
                super.visitIntInsn(Opcodes.BIPUSH, id);
            } else {
                super.visitIntInsn(Opcodes.SIPUSH, id);
            }
        }
    }
}
