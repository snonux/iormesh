package iormesh.agent;

import iormesh.agent.shaded.asm.ClassReader;
import iormesh.agent.shaded.asm.ClassVisitor;
import iormesh.agent.shaded.asm.ClassWriter;
import iormesh.agent.shaded.asm.Label;
import iormesh.agent.shaded.asm.MethodVisitor;
import iormesh.agent.shaded.asm.Opcodes;
import iormesh.agent.shaded.asm.Type;
import java.lang.instrument.ClassFileTransformer;
import java.lang.instrument.Instrumentation;
import java.security.ProtectionDomain;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * Arms the agent's OTEL span-context reading (decision #20, M3 step 8;
 * iormesh.bridge.Otel) when an app's OpenTelemetry initializes its
 * context storage: rewrites the static initializer of
 * {@code io.opentelemetry.context.LazyStorage}, in whatever class loader
 * defines it, to end with
 *
 * <pre>
 *   try { Hooks.otel(LazyStorage.class); } catch (Throwable t) { }
 * </pre>
 *
 * LazyStorage is the class OTEL initializes on the first use of its
 * context (ContextStorage.get, behind Context.current and makeCurrent),
 * so its initializer ending is the moment the storage exists and was
 * made by the app, not by the agent. The catch-all guards the app's
 * OTEL: a loader that cannot see the bootstrap-appended bridge (an OSGi
 * framework without boot delegation for iormesh.*) throws a linkage
 * error at the call, and an initializer that throws would make OTEL's
 * context unusable for the rest of the JVM's life.
 *
 * Nothing else of OTEL is touched, and no class of it is loaded or
 * initialized by the agent; an app without OTEL never loads LazyStorage.
 * Like the other transformers, the handler's frame is written by hand
 * (no COMPUTE_FRAMES, which would load classes from in here).
 */
final class OtelTransformer implements ClassFileTransformer {
    static final String LAZY_STORAGE = "io/opentelemetry/context/LazyStorage";
    static final String OTEL_DESC = "(Ljava/lang/Class;)V";
    /**
     * LazyStorage's binary name. Computed, not a constant, which also
     * gives this class the static initializer warmUp rewrites.
     */
    static final String LAZY_STORAGE_NAME = LAZY_STORAGE.replace('/', '.');

    private final String target;
    private final AtomicInteger rewritten = new AtomicInteger();

    OtelTransformer() {
        this(LAZY_STORAGE);
    }

    /** A transformer for another class name (the warm-up rewrites its own). */
    OtelTransformer(String target) {
        this.target = target;
    }

    /**
     * Only classes of an app loader are candidates: OTEL is never the
     * JDK's, and a bootstrap-defined LazyStorage would be a repackaged
     * copy the agent has no business arming.
     */
    @Override
    public byte[] transform(Module module, ClassLoader loader, String className,
                            Class<?> redefined, ProtectionDomain pd, byte[] bytes) {
        if (loader == null || redefined != null || !target.equals(className)) {
            return null;
        }
        try {
            return rewrite(bytes);
        } catch (Throwable t) {
            System.err.println("iormesh-agent: not rewriting " + className + ": " + t);
            return null;
        }
    }

    /** The class with its static initializer arming the reader, or null without one. */
    byte[] rewrite(byte[] bytes) {
        ClassReader reader = new ClassReader(bytes);
        ClassWriter writer = new ClassWriter(reader, ClassWriter.COMPUTE_MAXS);
        Rewriter rewriter = new Rewriter(writer);
        reader.accept(rewriter, 0);
        if (!rewriter.found) {
            return null;
        }
        rewritten.incrementAndGet();
        return writer.toByteArray();
    }

    /** LazyStorage classes rewritten so far (debug dump). */
    int rewritten() {
        return rewritten.get();
    }

    /**
     * A live attach finds OTEL in use already: each LazyStorage loaded
     * then has run its initializer (OTEL loads it only to call into it),
     * or is running it on another thread — arming then waits on the
     * initialization, on this, the attaching thread. premain finds none.
     */
    static List<Class<?>> loaded(Instrumentation inst) {
        List<Class<?>> out = new ArrayList<>();
        for (Class<?> c : inst.getAllLoadedClasses()) {
            if (c.getName().equals(LAZY_STORAGE_NAME) && c.getClassLoader() != null) {
                out.add(c);
            }
        }
        return out;
    }

    /**
     * Rewrites this class's own static initializer, so the rewriting
     * classes are loaded before an app's LazyStorage is: their loading
     * reads the agent jar, which must not happen inside the app's OTEL
     * initialization.
     */
    static void warmUp() throws java.io.IOException {
        String self = OtelTransformer.class.getName().replace('.', '/');
        try (java.io.InputStream in = OtelTransformer.class
                .getResourceAsStream("OtelTransformer.class")) {
            if (in == null || new OtelTransformer(self).rewrite(in.readAllBytes()) == null) {
                throw new java.io.IOException("otel transformer warm-up rewrote nothing");
            }
        }
    }

    /** Finds the static initializer. */
    private static final class Rewriter extends ClassVisitor {
        private String owner;
        private boolean frames;
        boolean found;

        Rewriter(ClassVisitor next) {
            super(Opcodes.ASM9, next);
        }

        @Override
        public void visit(int version, int access, String name, String signature,
                          String superName, String[] interfaces) {
            owner = name;
            frames = (version & 0xffff) >= Opcodes.V1_6;
            super.visit(version, access, name, signature, superName, interfaces);
        }

        @Override
        public MethodVisitor visitMethod(int access, String name, String descriptor,
                                         String signature, String[] exceptions) {
            MethodVisitor mv = super.visitMethod(access, name, descriptor, signature, exceptions);
            if (!name.equals("<clinit>")) {
                return mv;
            }
            found = true;
            return new Arm(mv, owner, frames);
        }
    }

    /**
     * Inserts the guarded Hooks.otel call before each return of the
     * initializer. Each call is its own try range; the shared handler,
     * appended after the original code, drops the throwable and returns —
     * the call is the initializer's last act, so nothing is skipped.
     */
    private static final class Arm extends MethodVisitor {
        private final String owner;
        private final boolean frames;
        private final Label handler = new Label();
        private boolean any;

        Arm(MethodVisitor next, String owner, boolean frames) {
            super(Opcodes.ASM9, next);
            this.owner = owner;
            this.frames = frames;
        }

        @Override
        public void visitInsn(int opcode) {
            if (opcode == Opcodes.RETURN) {
                Label start = new Label();
                Label end = new Label();
                // Declared before its labels, as ASM's contract asks; it
                // still follows the original handlers in the table, which
                // win where they cover a return.
                super.visitTryCatchBlock(start, end, handler, "java/lang/Throwable");
                super.visitLabel(start);
                super.visitLdcInsn(Type.getObjectType(owner));
                super.visitMethodInsn(Opcodes.INVOKESTATIC, BoundaryTransformer.HOOKS, "otel",
                        OTEL_DESC, false);
                super.visitLabel(end);
                any = true;
            }
            super.visitInsn(opcode);
        }

        /**
         * The handler declares no locals: it reads none, and a frame with
         * fewer locals than the code before it is always assignable.
         */
        @Override
        public void visitMaxs(int maxStack, int maxLocals) {
            if (any) {
                super.visitLabel(handler);
                if (frames) {
                    super.visitFrame(Opcodes.F_FULL, 0, new Object[0], 1,
                            new Object[] {"java/lang/Throwable"});
                }
                super.visitInsn(Opcodes.POP);
                super.visitInsn(Opcodes.RETURN);
            }
            super.visitMaxs(Math.max(maxStack, 1), maxLocals);
        }
    }
}
