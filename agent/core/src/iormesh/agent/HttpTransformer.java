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
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;

/**
 * Rewrites the HTTP hooks' methods (HttpHooks) so each exit hands what
 * the method has to {@code Hooks.http(ret, this, arg, id)}:
 *
 * <pre>
 *   try { original body, with Hooks.http(returned value, this, arg, id)
 *         before every return }
 *   catch (Throwable t) { Hooks.http(t, this, arg, id); throw t; }
 * </pre>
 *
 * (Until task de4 the throw path handed over null; the throwable lets a
 * void method's probe — a servlet's service — tell a throw from a
 * return. Every probe tests ret by type.)
 *
 * An HTTP event is stamped when the request is done, and nothing joins
 * to its window, so most targets are exit only. A stamped target (the
 * method that begins a request, Target.stamped, task wd4) also calls
 * {@code Hooks.httpEnter(this, id)} first thing, and its exits call
 * {@code Hooks.httpExit} instead, which pops that stamp: the request's
 * start, for its latency. A marked target (sendAsync) calls
 * {@code Hooks.httpMark(this, id)} first thing. A constructor is hooked at its returns alone
 * (on its exceptional path this may still be uninitialized) and never
 * stamped — an unhooked throw would leave its stamp on the thread.
 * A static method passes null as this (InetAddress.getAllByName, the
 * DNS hook of task wd4); a DNS target's exits call {@code Hooks.dns}.
 *
 * Like BoundaryTransformer, body rewrites with a hand-written handler
 * frame (no COMPUTE_FRAMES, which would load classes from inside the
 * transformer); unlike it, classes of the platform loader are
 * candidates too — java.net.http and jdk.httpserver are defined there.
 *
 * Third-party libraries' classes (task de4, Library.appLoaded) are
 * rewritten in the application loaders that define them, never in the
 * JDK's, and only where the app is safe: the loader must see the bridge
 * (BridgeSight), and a listed method that stores into this or a passed
 * parameter's slot is left alone (Scan) — the catch-all's frame declares
 * those slots with their declared types, which javac's code never
 * contradicts but another compiler's might, and a VerifyError would fail
 * the app's class. The JDK's targets are not scanned: some reassign a
 * parameter with a value of its own type (getAllByName's host), which
 * verifies, and each JDK generation's rewrite is verified by the agent's
 * smoke run.
 */
final class HttpTransformer implements ClassFileTransformer {
    static final String HTTP_DESC = "(Ljava/lang/Object;Ljava/lang/Object;Ljava/lang/Object;I)V";
    static final String ENTER_DESC = "(Ljava/lang/Object;I)V";
    /** argSlot: no argument is passed. */
    static final int NO_SLOT = -1;
    /** argSlot: the named argument is a primitive, the target is refused. */
    static final int REFUSED = -2;

    private final HttpHooks hooks;
    private final BridgeSight sight;
    private final Set<Integer> applied = ConcurrentHashMap.newKeySet();
    private final Set<String> seen = ConcurrentHashMap.newKeySet();

    /** A transformer of the JDK's classes only (tests, warm-up). */
    HttpTransformer(HttpHooks hooks) {
        this(hooks, new BridgeSight(null, null));
    }

    HttpTransformer(HttpHooks hooks, BridgeSight sight) {
        this.hooks = hooks;
        this.sight = sight;
    }

    @Override
    public byte[] transform(Module module, ClassLoader loader, String className,
                            Class<?> redefined, ProtectionDomain pd, byte[] bytes) {
        try {
            boolean jdk = loader == null || loader == ClassLoader.getPlatformClassLoader();
            if (className == null || (jdk ? !hooks.hasClass(className)
                    : !hooks.hasAppClass(className) || !sight.linkable(loader, module, className))) {
                return null;
            }
            return rewrite(className, bytes, jdk ? null : loader);
        } catch (Throwable t) {
            System.err.println("iormesh-agent: not rewriting " + className + ": " + t);
            return null;
        }
    }

    /**
     * The rewritten class, or null when none of its methods is listed. For
     * a third-party class a first pass (Scan) finds the listed methods
     * that store into a slot the catch-all's frame declares; those are
     * not rewritten.
     */
    byte[] rewrite(String className, byte[] bytes) {
        return rewrite(className, bytes, null);
    }

    /**
     * As {@link #rewrite(String, byte[])}; with an application loader
     * (a third-party class), each library whose method was rewritten is
     * then told so, once the bytes are complete (Library.rewritten, task
     * 6j4: Netty pairs only where both its hooks are in).
     */
    byte[] rewrite(String className, byte[] bytes, ClassLoader loader) {
        if (className == null || !(hooks.hasClass(className) || hooks.hasAppClass(className))) {
            return null;
        }
        seen.add(className);
        ClassReader reader = new ClassReader(bytes);
        Set<String> unsafe = Set.of();
        if (hooks.hasAppClass(className)) {
            Scan scan = new Scan(className);
            reader.accept(scan, ClassReader.SKIP_DEBUG | ClassReader.SKIP_FRAMES);
            unsafe = scan.unsafe;
        }
        ClassWriter writer = new ClassWriter(reader, ClassWriter.COMPUTE_MAXS);
        Rewriter rewriter = new Rewriter(writer, className, unsafe);
        reader.accept(rewriter, 0);
        if (rewriter.hooked.isEmpty()) {
            return null;
        }
        byte[] out = writer.toByteArray();
        for (HttpHooks.Hook h : loader == null ? List.<HttpHooks.Hook>of() : rewriter.hooked) {
            if (h.library() != null) {
                h.library().rewritten(loader, h.target());
            }
        }
        return out;
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
     * Rewrites this class's own hooks(), so the rewriting classes are
     * loaded before any target class is: their loading reads the agent
     * jar, which must not happen inside the target's request. Listed as a
     * third-party class, so the scan's classes load too.
     */
    static void warmUp() throws java.io.IOException {
        String self = HttpTransformer.class.getName().replace('.', '/');
        HttpHooks h = HttpHooks.of(java.util.List.of(new Target(self, "hooks",
                "()Liormesh/agent/HttpHooks;", Target.NO_ARG, null)), true);
        try (java.io.InputStream in = HttpTransformer.class
                .getResourceAsStream("HttpTransformer.class")) {
            if (in == null || new HttpTransformer(h).rewrite(self, in.readAllBytes()) == null) {
                throw new java.io.IOException("http transformer warm-up rewrote nothing");
            }
        }
    }

    /**
     * How one listed method is rewritten: its hook id, owner (null when
     * static), the slots and types of the arguments passed (NO_SLOT for
     * none; with slot2, the two go as an Object[] pair), and its exit.
     */
    record Plan(int id, String owner, int slot, String slotType, int slot2, String slotType2,
                boolean ctor, boolean stamp, String exit, boolean frames) {
        /** Whether the method's code stores into a slot the catch-all's frame declares. */
        boolean guards(int var, int size) {
            for (int s : new int[] {owner == null ? NO_SLOT : 0, slot, slot2}) {
                if (s >= 0 && (var == s || (size == 2 && var + 1 == s))) {
                    return true;
                }
            }
            return false;
        }
    }

    /**
     * The plan for hook h's method, or null when it cannot be hooked: no
     * body, or a named argument that is a primitive (nothing to pass).
     */
    static Plan plan(HttpHooks.Hook h, String className, int access, String name,
                     String descriptor, boolean frames) {
        if ((access & (Opcodes.ACC_ABSTRACT | Opcodes.ACC_NATIVE)) != 0) {
            return null;
        }
        Target t = h.target();
        boolean isStatic = (access & Opcodes.ACC_STATIC) != 0;
        int slot = argSlot(descriptor, t.arg(), isStatic);
        int slot2 = argSlot(descriptor, t.arg2(), isStatic);
        if (slot == REFUSED || slot2 == REFUSED) {
            return null;
        }
        boolean ctor = name.equals("<init>");
        String exit = t.dns() ? "dns" : t.stamp() && !ctor ? "httpExit" : "http";
        return new Plan(h.id(), isStatic ? null : className, slot, argType(descriptor, t.arg()),
                slot2, argType(descriptor, t.arg2()), ctor, t.stamp() && !ctor, exit, frames);
    }

    /**
     * The first pass: the listed methods ("name+descriptor") whose code
     * stores into this or a passed argument's slot (a reassigned
     * parameter, or a slot reused by another compiler) — rewriting those
     * could fail verification.
     */
    private final class Scan extends ClassVisitor {
        private final String className;
        final Set<String> unsafe = new HashSet<>();

        Scan(String className) {
            super(Opcodes.ASM9);
            this.className = className;
        }

        @Override
        public MethodVisitor visitMethod(int access, String name, String descriptor,
                                         String signature, String[] exceptions) {
            HttpHooks.Hook h = hooks.lookup(className, name, descriptor);
            Plan p = h == null ? null : plan(h, className, access, name, descriptor, false);
            if (p == null) {
                return null;
            }
            String key = name + descriptor;
            return new MethodVisitor(Opcodes.ASM9) {
                @Override
                public void visitVarInsn(int opcode, int var) {
                    if (opcode >= Opcodes.ISTORE && opcode <= Opcodes.ASTORE && p.guards(var,
                            opcode == Opcodes.LSTORE || opcode == Opcodes.DSTORE ? 2 : 1)) {
                        unsafe.add(key);
                    }
                }

                @Override
                public void visitIincInsn(int var, int increment) {
                    if (p.guards(var, 1)) {
                        unsafe.add(key);
                    }
                }
            };
        }
    }

    /** Finds the listed methods of one class. */
    private final class Rewriter extends ClassVisitor {
        private final String className;
        private final Set<String> unsafe;
        private int version;
        /** The hooks whose methods this class had rewritten. */
        final List<HttpHooks.Hook> hooked = new ArrayList<>();

        Rewriter(ClassVisitor next, String className, Set<String> unsafe) {
            super(Opcodes.ASM9, next);
            this.className = className;
            this.unsafe = unsafe;
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
            Plan p = h == null ? null
                    : plan(h, className, access, name, descriptor, version >= Opcodes.V1_6);
            if (p == null) {
                return mv;   // not listed, no body, or a primitive to pass
            }
            if (unsafe.contains(name + descriptor)) {
                System.err.println("iormesh-agent: not hooking " + className + "." + name
                        + descriptor + ": it stores into a slot the hook reads");
                return mv;
            }
            hooked.add(h);
            applied.add(h.id());
            Exit e = new Exit(mv, p);
            e.mark = h.target().mark();
            return e;
        }
    }

    /**
     * The local slot of the argument index arg names (this is slot 0 of
     * an instance method; a static one's arguments start at 0), NO_SLOT
     * for none, REFUSED when it is not a reference.
     */
    static int argSlot(String descriptor, int arg, boolean isStatic) {
        Type[] args = Type.getArgumentTypes(descriptor);
        int i = arg == Target.LAST_ARG ? args.length - 1 : arg;
        if (arg == Target.NO_ARG || i < 0 || i >= args.length) {
            return NO_SLOT;
        }
        int sort = args[i].getSort();
        if (sort != Type.OBJECT && sort != Type.ARRAY) {
            return REFUSED;
        }
        int slot = isStatic ? 0 : 1;
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
     * Calls the exit entry point (Hooks.http, httpExit when stamped, dns
     * for a lookup) before every return and, for a method, on its throw;
     * a stamped one calls Hooks.httpEnter first. A static method (no
     * owner) hands over null as this; a target with two arguments hands
     * over the pair as one Object[].
     */
    private static final class Exit extends MethodVisitor {
        private final Plan p;
        private final Label start = new Label();
        private final Label handler = new Label();
        /** Calls Hooks.httpMark first thing (a marked target). */
        boolean mark;

        Exit(MethodVisitor next, Plan p) {
            super(Opcodes.ASM9, next);
            this.p = p;
        }

        /**
         * The entry stamp goes before the try range: httpEnter never
         * throws, and were it to, its exit must not pop a stamp never
         * pushed.
         */
        @Override
        public void visitCode() {
            super.visitCode();
            if (mark) {
                pushThis();
                pushId();
                super.visitMethodInsn(Opcodes.INVOKESTATIC, BoundaryTransformer.HOOKS,
                        "httpMark", ENTER_DESC, false);
            }
            if (p.stamp()) {
                pushThis();
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
            if (!p.ctor()) {
                handler();
            }
            super.visitMaxs(maxStack, maxLocals);
        }

        /**
         * The catch-all, appended after the original code (its own
         * handlers come first in the table and still win). Its frame
         * declares only what it loads: this (none for a static method),
         * and the passed arguments' slots — the code stores into none of
         * them (Scan), and the slots between are TOP.
         */
        private void handler() {
            super.visitLabel(handler);
            if (p.frames()) {
                int n = Math.max(Math.max(p.slot(), p.slot2()) + 1, p.owner() == null ? 0 : 1);
                Object[] locals = new Object[n];
                java.util.Arrays.fill(locals, Opcodes.TOP);
                if (p.owner() != null) {
                    locals[0] = p.owner();
                }
                if (p.slot() >= 0) {
                    locals[p.slot()] = p.slotType();
                }
                if (p.slot2() >= 0) {
                    locals[p.slot2()] = p.slotType2();
                }
                super.visitFrame(Opcodes.F_FULL, n, locals, 1,
                        new Object[] {"java/lang/Throwable"});
            }
            // The throwable is the exit's "returned value" (task de4): a
            // void method's probe tells a throw from a return by it.
            super.visitInsn(Opcodes.DUP);
            callHook();
            super.visitInsn(Opcodes.ATHROW);
            super.visitTryCatchBlock(start, handler, handler, null);
        }

        /** Stack: [.., ret] → pushes this, arg and id, calls the exit entry point. */
        private void callHook() {
            pushThis();
            if (p.slot2() >= 0) {
                pushPair();
            } else if (p.slot() >= 0) {
                super.visitVarInsn(Opcodes.ALOAD, p.slot());
            } else {
                super.visitInsn(Opcodes.ACONST_NULL);
            }
            pushId();
            super.visitMethodInsn(Opcodes.INVOKESTATIC, BoundaryTransformer.HOOKS, p.exit(),
                    HTTP_DESC, false);
        }

        /** Pushes new Object[] {first, second} (first null when the target names none). */
        private void pushPair() {
            super.visitInsn(Opcodes.ICONST_2);
            super.visitTypeInsn(Opcodes.ANEWARRAY, "java/lang/Object");
            if (p.slot() >= 0) {
                super.visitInsn(Opcodes.DUP);
                super.visitInsn(Opcodes.ICONST_0);
                super.visitVarInsn(Opcodes.ALOAD, p.slot());
                super.visitInsn(Opcodes.AASTORE);
            }
            super.visitInsn(Opcodes.DUP);
            super.visitInsn(Opcodes.ICONST_1);
            super.visitVarInsn(Opcodes.ALOAD, p.slot2());
            super.visitInsn(Opcodes.AASTORE);
        }

        private void pushThis() {
            if (p.owner() == null) {
                super.visitInsn(Opcodes.ACONST_NULL);
            } else {
                super.visitVarInsn(Opcodes.ALOAD, 0);
            }
        }

        private void pushId() {
            if (p.id() <= Byte.MAX_VALUE) {
                super.visitIntInsn(Opcodes.BIPUSH, p.id());
            } else {
                super.visitIntInsn(Opcodes.SIPUSH, p.id());
            }
        }
    }
}
