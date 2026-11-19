package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.shaded.asm.ClassReader;
import iormesh.agent.shaded.asm.ClassVisitor;
import iormesh.agent.shaded.asm.ClassWriter;
import iormesh.agent.shaded.asm.MethodVisitor;
import iormesh.agent.shaded.asm.Opcodes;
import iormesh.bridge.Hooks;
import java.util.List;
import java.util.concurrent.CountDownLatch;

/**
 * The live-attach interleaving of the 4b4 review: an app thread is
 * inside LazyStorage's (rewritten) initializer when the attaching thread
 * arms that class. The attacher's first read waits for the initializer;
 * the initializer ends in Hooks.otel on the app thread. Arming must not
 * hold a lock across that read, or both threads hang — and the app's
 * OTEL with them. The app thread is paused at the start of the
 * initializer until the attacher is blocked on it, so the bad order is
 * forced, not hoped for.
 */
public final class OtelAttachRaceTest {
    private static final long JOIN_MS = 10_000;
    private static volatile CountDownLatch inInit;
    private static volatile Thread attacher;

    private OtelAttachRaceTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("otel attach/arm during the app's init",
                OtelAttachRaceTest::race));
    }

    /**
     * Called first in the test copy's LazyStorage initializer: tells the
     * attacher to go, then waits until it is blocked (on this class's
     * initialization), at most 5 s, plus a little.
     */
    public static void pause() {
        CountDownLatch l = inInit;
        if (l == null) {
            return;
        }
        l.countDown();
        long end = System.nanoTime() + 5_000_000_000L;
        try {
            while (System.nanoTime() < end) {
                Thread a = attacher;
                if (a != null && (a.getState() == Thread.State.WAITING
                        || a.getState() == Thread.State.BLOCKED)) {
                    break;
                }
                Thread.sleep(5);
            }
            Thread.sleep(100);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }

    static void race() throws Exception {
        ClassLoader l = OtelFixture.loader(true, OtelAttachRaceTest.class.getClassLoader(),
                OtelAttachRaceTest::rewrite);
        int before = Hooks.otelArmed();
        Exception[] err = new Exception[2];
        inInit = new CountDownLatch(1);
        Thread app = new Thread(() -> {
            try {
                OtelFixture.initContext(l);
            } catch (Exception e) {
                err[0] = e;
            }
        }, "otel-race-app");
        Thread attach = new Thread(() -> {
            try {
                inInit.await();
                Hooks.otel(OtelFixture.lazyStorage(l));   // as Agent.installOtel does
            } catch (Exception e) {
                err[1] = e;
            }
        }, "otel-race-attach");
        attacher = attach;
        try {
            for (Thread t : new Thread[] {attach, app}) {
                t.setDaemon(true);   // a hang must not keep the test JVM alive
                t.start();
            }
            attach.join(JOIN_MS);
            app.join(JOIN_MS);
            check(!attach.isAlive() && !app.isAlive(), "attach and app threads deadlocked");
        } finally {
            inInit = null;
            attacher = null;
        }
        check(err[0] == null && err[1] == null, "errors " + err[0] + ", " + err[1]);
        equal(before + 1, Hooks.otelArmed(), "armed once");
    }

    /** OtelTransformer's rewrite, then pause() first in the initializer. */
    private static byte[] rewrite(byte[] b) {
        ClassReader reader = new ClassReader(new OtelTransformer().rewrite(b));
        ClassWriter writer = new ClassWriter(reader, ClassWriter.COMPUTE_MAXS);
        reader.accept(new ClassVisitor(Opcodes.ASM9, writer) {
            @Override
            public MethodVisitor visitMethod(int access, String name, String desc,
                                             String sig, String[] ex) {
                MethodVisitor mv = super.visitMethod(access, name, desc, sig, ex);
                if (!name.equals("<clinit>")) {
                    return mv;
                }
                return new MethodVisitor(Opcodes.ASM9, mv) {
                    @Override
                    public void visitCode() {
                        super.visitCode();
                        super.visitMethodInsn(Opcodes.INVOKESTATIC,
                                "iormesh/agent/OtelAttachRaceTest", "pause", "()V", false);
                    }
                };
            }
        }, 0);
        return writer.toByteArray();
    }
}
