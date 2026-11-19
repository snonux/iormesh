package iormesh.bridge;

import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.lang.ref.WeakReference;
import java.util.ArrayList;
import java.util.List;

/**
 * Reads the OpenTelemetry span context current on the calling thread
 * (PLAN.md M3 "OpenTelemetry introspection", decision #20: span-context
 * tagging), so each bracket and HTTP record carries the trace and span
 * ids of the span the I/O ran in.
 *
 * No OTEL dependency: the agent cannot know OTEL at compile time, and an
 * app may have none, or several copies on several class loaders. A copy
 * is read reflectively — {@code Span.current().getSpanContext()}'s
 * {@code getTraceId()} and {@code getSpanId()}, through method handles
 * looked up in the class loader that defined it — once it is
 * <em>armed</em>.
 *
 * Arming waits for the app: the agent rewrites the end of
 * {@code io.opentelemetry.context.LazyStorage}'s static initializer to
 * call {@link #arm} (OtelTransformer), so a copy is armed only after the
 * app itself initialized OTEL's context storage. The agent does not
 * initialize it on its own: that initializer reads system properties, runs a
 * ServiceLoader (jar I/O) and freezes the storage wrappers the app may
 * still register — done from inside a hooked I/O call of another thread
 * it could deadlock on a jar lock or change what the app's OTEL becomes.
 * (One exception: a live attach arms each LazyStorage already loaded,
 * and one loaded but not yet initialized — OTEL loads it only to call
 * into it, so this is a window, not a state — is initialized on the
 * attaching thread.) An app without OTEL never loads LazyStorage, and
 * then nothing here runs beyond one volatile read per bracket.
 *
 * Ids travel as numbers: a trace id as two longs (its 32 hex digits'
 * upper and lower halves), a span id as one; all zero means no span —
 * W3C ids are never all zero, and OTEL's invalid span context is.
 */
final class Otel {
    /** At most this many OTEL copies are read; a later one is refused (counted as a fail). */
    static final int MAX_READERS = 8;
    static final String SPAN = "io.opentelemetry.api.trace.Span";
    static final String SPAN_CONTEXT = "io.opentelemetry.api.trace.SpanContext";

    /**
     * Keeps each reader alive exactly as long as the LazyStorage class it
     * was armed by: the readers list holds them weakly, so an app server
     * that undeploys a web app does not find its class loader pinned by
     * the agent. A one-element box, since computeValue cannot take the
     * reader.
     */
    private static final ClassValue<Reader[]> KEEP = new ClassValue<>() {
        @Override
        protected Reader[] computeValue(Class<?> type) {
            return new Reader[1];
        }
    };

    /** Guards arming's dedupe and publish (never held across a read). */
    private static final Object LOCK = new Object();

    /** The armed readers, copy-on-write; read on every bracket. */
    @SuppressWarnings("unchecked")
    private static volatile WeakReference<Reader>[] readers =
            (WeakReference<Reader>[]) new WeakReference<?>[0];

    private Otel() {}

    /**
     * Arms the OTEL copy whose LazyStorage just initialized: resolves
     * its Span and SpanContext in that class's loader and reads once, so
     * the classes the read touches load now, inside the app's own OTEL
     * initialization, not later inside a hooked I/O call. False when
     * this copy is armed already or has no OTEL API beside the context
     * jar (throws then); throws past MAX_READERS, so Hooks.otel counts
     * the refused copy.
     *
     * The lock covers only the dedupe and the publish, never the
     * resolve and first read: on a live attach the attaching thread may
     * arm a LazyStorage whose initializer is still running on an app
     * thread, so its first read waits for that initializer — which ends
     * in arm on the app thread. Holding a lock across the read would
     * deadlock the two, and hang the app's OTEL for good (4b4 review).
     */
    static boolean arm(Class<?> lazyStorage, Brackets t) throws ReflectiveOperationException {
        ClassLoader loader = lazyStorage.getClassLoader();
        synchronized (LOCK) {
            if (refuse(loader)) {
                return false;
            }
        }
        Reader r = Reader.resolve(loader);
        try {
            r.read(t);
        } catch (Throwable e) {
            // A copy whose first read fails would fail every read: not armed.
            throw new ReflectiveOperationException("otel: first span read failed", e);
        }
        synchronized (LOCK) {
            // Again: another thread may have armed this copy meanwhile.
            if (refuse(loader)) {
                return false;
            }
            KEEP.get(lazyStorage)[0] = r;
            publish(r);
            return true;
        }
    }

    /**
     * Whether loader's copy is armed already (false to the caller);
     * throws when MAX_READERS live copies are armed. Under LOCK.
     */
    private static boolean refuse(ClassLoader loader) {
        int live = 0;
        for (WeakReference<Reader> w : readers) {
            Reader r = w.get();
            if (r != null && r.loader.get() == loader) {
                return true;
            }
            live += r != null ? 1 : 0;
        }
        if (live >= MAX_READERS) {
            throw new IllegalStateException("otel: more than " + MAX_READERS + " OTEL copies");
        }
        return false;
    }

    /** Adds r to the readers, dropping cleared ones. Under LOCK (tests: directly). */
    static void publish(Reader r) {
        List<WeakReference<Reader>> live = new ArrayList<>();
        for (WeakReference<Reader> w : readers) {
            if (w.get() != null) {
                live.add(w);
            }
        }
        live.add(new WeakReference<>(r));
        @SuppressWarnings("unchecked")
        WeakReference<Reader>[] next = live.toArray(
                (WeakReference<Reader>[]) new WeakReference<?>[0]);
        readers = next;
    }

    /** Forgets every armed copy (tests only: the cap test fills the list). */
    @SuppressWarnings("unchecked")
    static void reset() {
        synchronized (LOCK) {
            readers = (WeakReference<Reader>[]) new WeakReference<?>[0];
        }
    }

    /** The number of OTEL copies armed and still loaded (debug dump). */
    static int armed() {
        int n = 0;
        for (WeakReference<Reader> w : readers) {
            n += w.get() != null ? 1 : 0;
        }
        return n;
    }

    /**
     * Sets t's current ids (otelHi, otelLo, otelSpan) to the span current
     * on this thread: the first armed copy with a valid span wins, all
     * zero when none has one. Callers hold t busy, so I/O a read might
     * cause is not bracketed; a reader that throws counts as no span.
     */
    static void read(Brackets t) {
        WeakReference<Reader>[] rs = readers;
        for (WeakReference<Reader> w : rs) {
            Reader r = w.get();
            try {
                if (r != null && r.read(t)) {
                    return;
                }
            } catch (Throwable e) {
                // An app's OTEL that throws is no span here; never the
                // target's problem. The cache may hold ids parsed just
                // before the throw: forget it with them.
                t.otelContext = null;
            }
        }
        t.otelHi = 0;
        t.otelLo = 0;
        t.otelSpan = 0;
    }

    /** Whether every char of s is a hex digit (W3C ids are lowercase; OTEL makes them so). */
    static boolean isHex(String s) {
        for (int i = 0; i < s.length(); i++) {
            if (digit(s.charAt(i)) < 0) {
                return false;
            }
        }
        return true;
    }

    /** Parses 16 hex digits of s from off (checked by isHex) as an unsigned long. */
    static long hex16(String s, int off) {
        long v = 0;
        for (int i = off; i < off + 16; i++) {
            v = v << 4 | digit(s.charAt(i));
        }
        return v;
    }

    private static int digit(char c) {
        return c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10
                : c >= 'A' && c <= 'F' ? c - 'A' + 10 : -1;
    }

    /** One OTEL copy's read path: method handles erased to Object. */
    static final class Reader {
        /** Weak: the handles pin the loader already, this is only for identity. */
        final WeakReference<ClassLoader> loader;
        /**
         * Span.current().getSpanContext() as one handle: each invocation
         * of a handle that is not a constant costs a call the JIT cannot
         * inline, and this one runs at every bracket.
         */
        private final MethodHandle context;
        private final MethodHandle traceId;
        private final MethodHandle spanId;

        Reader(ClassLoader loader, MethodHandle context, MethodHandle traceId,
                       MethodHandle spanId) {
            this.loader = new WeakReference<>(loader);
            this.context = context;
            this.traceId = traceId;
            this.spanId = spanId;
        }

        /**
         * Looks the API up in loader with the public lookup: Span and
         * SpanContext are public interfaces in exported packages, so no
         * module needs opening; a missing API jar throws, and nothing is
         * armed.
         */
        static Reader resolve(ClassLoader loader) throws ReflectiveOperationException {
            Class<?> span = Class.forName(SPAN, false, loader);
            Class<?> ctx = Class.forName(SPAN_CONTEXT, false, loader);
            MethodHandles.Lookup lk = MethodHandles.publicLookup();
            MethodType str = MethodType.methodType(String.class, Object.class);
            MethodHandle current = lk.findStatic(span, "current", MethodType.methodType(span));
            MethodHandle spanContext = lk.findVirtual(span, "getSpanContext",
                    MethodType.methodType(ctx));
            return new Reader(loader,
                    MethodHandles.filterReturnValue(current, spanContext)
                            .asType(MethodType.methodType(Object.class)),
                    lk.findVirtual(ctx, "getTraceId", MethodType.methodType(String.class))
                            .asType(str),
                    lk.findVirtual(ctx, "getSpanId", MethodType.methodType(String.class))
                            .asType(str));
        }

        /**
         * Reads this copy's current span into t; true when it is valid.
         * The span context is compared by identity with the one this
         * thread read last (span contexts are immutable), so a thread
         * inside one span fetches and parses its ids once — the common
         * case costs one handle call. The last context is held weakly:
         * the agent must not keep an app's object, and with it its
         * class loader, alive.
         */
        boolean read(Brackets t) throws Throwable {
            Object ctx = (Object) context.invokeExact();
            if (ctx == null) {
                return false;
            }
            WeakReference<Object> last = t.otelContext;
            if (last == null || last.get() != ctx) {
                parse(t, (String) traceId.invokeExact(ctx), (String) spanId.invokeExact(ctx));
                t.otelContext = new WeakReference<>(ctx);
            }
            return t.otelSpan != 0;
        }

        /** Parses W3C ids into t, all zero unless both are valid. */
        private static void parse(Brackets t, String trace, String id) {
            t.otelHi = 0;
            t.otelLo = 0;
            t.otelSpan = 0;
            if (trace == null || id == null || trace.length() != 32 || id.length() != 16
                    || !isHex(trace) || !isHex(id)) {
                return;
            }
            long hi = hex16(trace, 0);
            long lo = hex16(trace, 16);
            long sp = hex16(id, 0);
            if ((hi == 0 && lo == 0) || sp == 0) {
                return;   // OTEL's invalid span context: no span
            }
            t.otelHi = hi;
            t.otelLo = lo;
            t.otelSpan = sp;
        }
    }
}
