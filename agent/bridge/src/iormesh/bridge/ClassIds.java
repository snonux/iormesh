package iormesh.bridge;

import java.net.URL;
import java.nio.file.Path;
import java.security.CodeSource;
import java.security.ProtectionDomain;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;

/**
 * The class-load map (task fe4, PLAN.md M3 "Supporting mappings"): a
 * per-JVM id for each class a recorded frame names, described once in a
 * CLASS record — binary name, class loader, code source — so the tracer
 * can join every frame to its JAR.
 *
 * Only classes on recorded stacks are described, never every loaded
 * class: StackIds asks for a class's id when a new stack names it, so
 * the work is per distinct call site, not per bracket, and an
 * application's tens of thousands of loaded classes that never reach an
 * I/O stack cost nothing.
 *
 * Safety: the id lives in a {@link ClassValue}, which holds it as long
 * as the class lives — no class or loader is pinned, so an undeployed
 * web app can still be collected. ClassValue runs computeValue without
 * holding its own lock, and describing a class only reads what the
 * class already has (its loader, protection domain, module): nothing
 * initializes a class or takes a lock the app could hold across one
 * (4b4's live-attach deadlock lesson). Every step is caught; a class
 * that cannot be described gets an empty code source, never an error
 * in the target.
 *
 * Order: the CLASS record is written inside computeValue, so any thread
 * that gets the id saw the record reserved first, and a STACK naming
 * it lies behind it in the ring. Two threads racing on a new class may
 * each compute an entry and write a CLASS (ClassValue keeps one id); the
 * loser's row names an id no frame uses, which the tracer never lists.
 * One entry's CLASS is written at most once: a writer claims it first.
 * A dropped CLASS is retried when a later new stack names the class
 * (after the back-off), and then follows the STACKs already naming it.
 * The tracer resolves a stack once, when its first event joins: a stack
 * resolved before the retried CLASS arrived keeps no class for those
 * frames (method rows without class_id), and the drop is counted.
 */
final class ClassIds {
    /** 0 means "not described" in a STACK's class ids. */
    static final long NONE = 0;

    private static final AtomicLong NEXT = new AtomicLong();
    private static final ClassValue<Entry> IDS = new ClassValue<>() {
        @Override
        protected Entry computeValue(Class<?> c) {
            Entry e = new Entry(NEXT.incrementAndGet());
            e.write(c);
            return e;
        }
    };
    /** After a dropped CLASS, no retry before this nanoTime. */
    private static volatile long retryAt;

    private ClassIds() {}

    /**
     * One class's id and whether its CLASS reached the ring or is being
     * written: claimed by compare-and-set, so two threads retrying a
     * dropped one never both write it (the ring would hold one id twice).
     */
    private static final class Entry {
        final long id;
        final AtomicBoolean written = new AtomicBoolean();

        Entry(long id) {
            this.id = id;
        }

        /**
         * Writes the CLASS record. Without a ring (released after a
         * failed install, between a test's rings) it stays unwritten,
         * so the next ring still gets it.
         */
        void write(Class<?> c) {
            Ring r = Hooks.ring;
            if (r == null || !written.compareAndSet(false, true)) {
                return;
            }
            boolean ok = false;
            try {
                ok = r.classRecord(id, c.getName(), loader(c.getClassLoader()), codeSource(c));
            } finally {
                if (!ok) {
                    written.set(false);
                    retryAt = System.nanoTime() + Hooks.RETRY_NS;
                }
            }
        }
    }

    /**
     * Returns c's id, describing c the first time, or retrying a dropped
     * description after the back-off. The id is returned whether or not
     * its CLASS reached the ring (the tracer then has no class for it);
     * NONE only when getting the id itself threw.
     */
    static long of(Class<?> c) {
        try {
            Entry e = IDS.get(c);
            if (!e.written.get() && System.nanoTime() >= retryAt) {
                e.write(c);
            }
            return e.id;
        } catch (Throwable t) {
            return NONE;
        }
    }

    /**
     * A loader's label: "bootstrap" for the boot loader, the name the
     * loader was given (the JDK's "app" and "platform", a container's
     * named loaders), else its class name.
     */
    static String loader(ClassLoader l) {
        if (l == null) {
            return "bootstrap";
        }
        String n = l.getName();
        return n != null ? n : l.getClass().getName();
    }

    /**
     * Where c was loaded from: a {@code file:} location as a path (the
     * JAR or class directory), a runtime-image class of the boot loader
     * as {@code jrt:/<module>} (its protection domain has no code
     * source; the platform loader's classes say jrt themselves), any
     * other location without userinfo, query or fragment; "" when c has
     * none (generated classes) or it cannot be read.
     */
    static String codeSource(Class<?> c) {
        try {
            ProtectionDomain pd = protectionDomain(c);
            CodeSource cs = pd == null ? null : pd.getCodeSource();
            URL u = cs == null ? null : cs.getLocation();
            if (u != null) {
                return location(u);
            }
            Module m = c.getModule();
            // The boot layer's: a proxy's dynamic module is named too,
            // but not in the runtime image.
            if (c.getClassLoader() == null && m.isNamed() && m.getLayer() == ModuleLayer.boot()) {
                return "jrt:/" + m.getName();
            }
        } catch (Throwable ignored) {
            // A policy refusing even the privileged read, or a URL that
            // is no URI: no code source, never a failed bracket.
        }
        return "";
    }

    /**
     * c's protection domain. Under a SecurityManager (JDK 17–23) it needs
     * RuntimePermission("getProtectionDomain"), which the app's frames on
     * the stack may lack: read in doPrivileged, which the
     * bootstrap-loaded bridge holds.
     */
    @SuppressWarnings("removal")
    private static ProtectionDomain protectionDomain(Class<?> c) {
        return java.security.AccessController.doPrivileged(
                (java.security.PrivilegedAction<ProtectionDomain>) c::getProtectionDomain);
    }

    /**
     * A location as recorded: file URLs as paths; others as their text
     * through {@link #clean}, so credentials in userinfo or a query never
     * reach the ring (the tracer strips them again).
     */
    static String location(URL u) {
        if ("file".equals(u.getProtocol())) {
            try {
                return Path.of(u.toURI()).toString();
            } catch (Throwable ignored) {
                // Not a valid file URI (unescaped characters): fall back
                // to the generic form, still credential-free.
            }
        }
        return clean(u.toExternalForm());
    }

    /**
     * Strips the userinfo of every authority ("scheme://user:pw@host"),
     * nested ones included ("jar:https://…!/"), and everything from the
     * first '?' or '#' on; text without "://" is returned as is. Go's
     * schema.CleanCodeSource is the same algorithm.
     */
    static String clean(String s) {
        if (!s.contains("://")) {
            return s;
        }
        int q = indexOfAny(s, '?', '#');
        if (q >= 0) {
            s = s.substring(0, q);
        }
        StringBuilder b = new StringBuilder();
        for (int at = s.indexOf("://"); at >= 0; at = s.indexOf("://")) {
            b.append(s, 0, at + 3);
            String after = s.substring(at + 3);
            int slash = after.indexOf('/');
            String auth = slash >= 0 ? after.substring(0, slash) : after;
            b.append(auth.substring(auth.lastIndexOf('@') + 1));
            s = slash >= 0 ? after.substring(slash) : "";
        }
        return b.append(s).toString();
    }

    private static int indexOfAny(String s, char a, char c) {
        int i = s.indexOf(a);
        int j = s.indexOf(c);
        return i < 0 ? j : j < 0 ? i : Math.min(i, j);
    }
}
