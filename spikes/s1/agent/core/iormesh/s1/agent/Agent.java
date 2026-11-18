package iormesh.s1.agent;

import java.io.PrintStream;
import java.lang.classfile.ClassFile;
import java.lang.classfile.ClassTransform;
import java.lang.classfile.CodeBuilder;
import java.lang.classfile.CodeElement;
import java.lang.classfile.CodeTransform;
import java.lang.classfile.Label;
import java.lang.classfile.MethodModel;
import java.lang.classfile.MethodTransform;
import java.lang.classfile.instruction.ReturnInstruction;
import java.lang.constant.ClassDesc;
import java.lang.constant.MethodTypeDesc;
import java.lang.instrument.ClassFileTransformer;
import java.lang.instrument.Instrumentation;
import java.security.ProtectionDomain;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.jar.JarFile;

/**
 * Throwaway (S1, task wa4): proves the hook set is reachable with pure
 * body rewrites of already-loaded java.base classes (retransformation
 * may not add methods, so no native-method-prefix wrapper). Each hooked
 * Java method gets Hooks.enter(id) at entry and Hooks.exit(id) before
 * every return and in a catch-all handler that rethrows.
 *
 * Uses the java.lang.classfile API (final in JDK 24), so this prototype
 * runs on JDK 25 only; M3 shades ASM to cover JDK 21.
 *
 * Agent args: {@code <bridge.jar>,<out-prefix>}; writes
 * {@code <out-prefix>.hooks} (id → class.method) at premain and
 * {@code <out-prefix>.recs} (Hooks records) at shutdown.
 */
public final class Agent {
    private static final ClassDesc HOOKS = ClassDesc.of("iormesh.s1.bridge.Hooks");
    private static final MethodTypeDesc INT_VOID = MethodTypeDesc.ofDescriptor("(I)V");

    private Agent() {}

    public static void premain(String args, Instrumentation inst) throws Exception {
        String[] a = args.split(",", 2);
        inst.appendToBootstrapClassLoaderSearch(new JarFile(a[0]));
        Class<?> hooks = Class.forName("iormesh.s1.bridge.Hooks", true, null);
        // java.base must read the bootstrap loader's unnamed module, or
        // the rewritten JDK methods fail to link against Hooks.
        inst.redefineModule(Object.class.getModule(), Set.of(hooks.getModule()),
                Map.of(), Map.of(), Set.of(), Map.of());
        List<String> ids = HookTable.ids();
        try (PrintStream out = new PrintStream(a[1] + ".hooks")) {
            for (int i = 0; i < ids.size(); i++) {
                out.println(i + "\t" + ids.get(i));
            }
        }
        inst.addTransformer(new Rewriter(ids), true);
        inst.retransformClasses(loadedTargets(inst));
        Runtime.getRuntime().addShutdownHook(new Thread(() -> dump(hooks, a[1] + ".recs")));
    }

    private static Class<?>[] loadedTargets(Instrumentation inst) {
        List<Class<?>> out = new ArrayList<>();
        for (Class<?> c : inst.getAllLoadedClasses()) {
            if (HookTable.CLASSES.contains(c.getName().replace('.', '/'))) {
                out.add(c);
            }
        }
        return out.toArray(new Class<?>[0]);
    }

    private static void dump(Class<?> hooks, String path) {
        try {
            hooks.getMethod("dump", String.class).invoke(null, path);
        } catch (ReflectiveOperationException e) {
            e.printStackTrace();
        }
    }

    /** Rewrites the bodies of the hook table's methods, nothing else. */
    private static final class Rewriter implements ClassFileTransformer {
        private final List<String> ids;

        Rewriter(List<String> ids) {
            this.ids = ids;
        }

        @Override
        public byte[] transform(Module module, ClassLoader loader, String name,
                                Class<?> redefined, ProtectionDomain pd, byte[] bytes) {
            if (name == null || !HookTable.CLASSES.contains(name)) {
                return null;
            }
            try {
                ClassFile cf = ClassFile.of();
                ClassTransform ct = (clb, ce) -> {
                    int id = ce instanceof MethodModel m ? ids.indexOf(key(name, m)) : -1;
                    if (id >= 0 && ((MethodModel) ce).code().isPresent()) {
                        // A fresh Bracket per method: it carries labels.
                        clb.transformMethod((MethodModel) ce,
                                MethodTransform.transformingCode(new Bracket(id)));
                    } else {
                        clb.with(ce);
                    }
                };
                return cf.transformClass(cf.parse(bytes), ct);
            } catch (Throwable t) {
                // A failed transform must not kill the target; log and skip.
                t.printStackTrace();
                return null;
            }
        }
    }

    static String key(String cls, MethodModel m) {
        return cls + "." + m.methodName().stringValue();
    }

    /**
     * Wraps one method body: enter at the start, exit before each return,
     * and a catch-all handler (exit, rethrow) covering the original code.
     */
    private static final class Bracket implements CodeTransform {
        private final int id;
        private Label start;

        Bracket(int id) {
            this.id = id;
        }

        @Override
        public void accept(CodeBuilder b, CodeElement e) {
            if (start == null) {
                b.loadConstant(id).invokestatic(HOOKS, "enter", INT_VOID);
                start = b.newBoundLabel();
            }
            if (e instanceof ReturnInstruction) {
                b.loadConstant(id).invokestatic(HOOKS, "exit", INT_VOID);
            }
            b.with(e);
        }

        @Override
        public void atEnd(CodeBuilder b) {
            Label end = b.newBoundLabel();
            // Stack: [Throwable]. Stamp the exit, then rethrow it unchanged.
            b.loadConstant(id).invokestatic(HOOKS, "exit", INT_VOID).athrow();
            b.exceptionCatchAll(start, end, end);
        }
    }
}
