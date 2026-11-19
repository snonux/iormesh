package iormesh.bridge;

import java.lang.invoke.VarHandle;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicLongArray;
import java.util.function.BiFunction;
import java.util.function.LongSupplier;

/**
 * The entry points the rewritten JDK methods call: {@code enter(id)} at
 * the start of a hooked method, {@code exit(id)} before each return and
 * on the exceptional path (BoundaryTransformer). One enter/exit pair is
 * a <em>bracket</em> around the method's native I/O call.
 *
 * Loaded by the bootstrap class loader (the agent appends the bridge jar
 * to its search path), because java.base code can only link classes
 * that loader sees; java.base is also made to read this jar's unnamed
 * module (Agent). So this jar holds only what the hot path needs: no
 * ASM, nothing of the agent core.
 *
 * Each thread keeps a small stack of open brackets: hooked methods nest
 * (RandomAccessFile.write(byte[]) reaches the hooked writeBytes), and
 * the innermost bracket containing a syscall is the one the joiner
 * keeps. A bracket opened while the thread is already inside the bridge
 * (the stack walk loading a class, which reads a jar) is skipped, so the
 * agent never brackets its own work.
 *
 * Neither entry point ever throws into the target, and both keep the
 * per-thread depth balanced whatever fails: a slot is marked SKIPPED
 * before anything that can throw runs, and only marked with its hook
 * once the bracket is fully open. An exit that threw would be worse
 * than a lost bracket — the pre-return exit calls lie inside the
 * method's catch-all range, so the handler would exit a second time and
 * close the caller's bracket.
 *
 * A closed bracket becomes a BRACKET record in the ring (decision #27,
 * {@link Ring}); a thread's first bracket also writes its THREAD record,
 * and a stack's first sighting its STACK record (StackIds). Neither is
 * guaranteed to precede the brackets naming it — a dropped one is
 * retried later, and another thread may publish a bracket on a stack
 * whose STACK is still being written — so the joiner resolves ids
 * against everything it has read. Without a
 * ring (no spool= option) brackets are only counted, and only when the
 * agent's debug dump asked for counts ({@link #counts}).
 *
 * With an OTEL-instrumented app (decision #20, task 4b4), each bracket
 * and HTTP record also carries the trace and span ids of the OTEL span
 * current at its enter (at the request's end for HTTP), read by
 * {@link Otel} once the app's OTEL is armed through {@link #otel}.
 *
 * HTTP hooks (decision #22, task 3b4) are a second kind of entry point,
 * {@link #http}: called once when a hooked HTTP method returns (or
 * throws), with what the method has at hand, it hands those objects to
 * the agent's probe for that hook and writes the request the probe
 * describes as one HTTP record. No bracket: an HTTP event is not a
 * syscall to join, it is an event of its own, stamped and stacked here.
 * A hook whose method begins the request (a client's send, a server's
 * dispatch) also stamps its entry ({@link #httpEnter}, task wd4), so the
 * record carries the request's start and the tracer its latency.
 */
public final class Hooks {
    /** Deeper nesting than this is not tracked (still balanced). */
    static final int MAX_DEPTH = 16;
    /** Entry-stamped HTTP hooks kept per thread (Brackets.httpStarts). */
    static final int MAX_HTTP_DEPTH = 8;
    /** How long a dropped THREAD or STACK waits before it is tried again. */
    static final long RETRY_NS = 1_000_000;
    /** Marks a skipped slot, so its exit is dropped too. */
    private static final int SKIPPED = -1;
    private static final LongSupplier WALK = StackIds::current;
    private static final LongSupplier NO_WALK = () -> StackIds.NONE;

    /** Where enter gets its stack id; package-private so tests can fail it. */
    static volatile LongSupplier stacks = WALK;
    /** Closed brackets per hook id; package-private so tests can break it. */
    static volatile AtomicLongArray counts = new AtomicLongArray(0);
    /** The transport, or null (no spool=): brackets then go nowhere. */
    static volatile Ring ring;
    private static final AtomicLong SKIPS = new AtomicLong();
    private static final AtomicLong FAILS = new AtomicLong();
    private static final AtomicBoolean CLAIMED = new AtomicBoolean();
    private static final ThreadLocal<Brackets> STATE = ThreadLocal.withInitial(Brackets::new);
    /**
     * The HTTP probes by HTTP hook id (iormesh.agent.http): given {return
     * value, this, argument, open, id} of a hooked method — open is the this
     * of the innermost entry-stamped HTTP hook running on the thread (the
     * method's own this at an entry-stamped hook's exit), or null — and a
     * long[Ring.META] to fill (Ring.M_*: status, request-header count,
     * flags, exchanges, start, body sizes; it arrives with that open
     * hook's start stamp at M_START and -1 for the sizes), a probe
     * returns the request's strings — the Ring.HTTP_FIXED leading ones,
     * then the headers, redacted — or null when this call records nothing
     * (a binding hook, a request already recorded, no response). A probe
     * keeps the start only when open is its request's own (its library
     * began the request there); a stamp it cannot tie to the request is
     * cleared, never guessed. JDK types only: the agent core, which
     * implements them, is on another loader.
     */
    @SuppressWarnings("unchecked")
    static volatile BiFunction<Object[], long[], String[]>[] httpProbes =
            (BiFunction<Object[], long[], String[]>[]) new BiFunction<?, ?, ?>[0];
    /** Requests recorded per HTTP hook id, kept like counts (debug dump only). */
    static volatile AtomicLongArray httpCounts = new AtomicLongArray(0);
    private static volatile boolean counting;

    private Hooks() {}

    /**
     * Claims the bridge for one agent install and returns whether this
     * caller got it. A second -javaagent or a repeated live attach finds
     * this (bootstrap-loaded, so shared) Hooks already claimed and must
     * not install again: it would rewrite the JDK twice — two brackets
     * per call — and reset the counters.
     */
    public static boolean claim() {
        return CLAIMED.compareAndSet(false, true);
    }

    /**
     * Undoes {@link #claim} after an install that failed before any JDK
     * class was rewritten, so a later live attach can still install;
     * also discards the ring, so the tracer does not wait on it.
     */
    public static void release() {
        Ring r = ring;
        ring = null;
        if (r != null) {
            try {
                r.discard();
            } catch (Exception e) {
                // The tracer then finds a ring that stays empty.
            }
        }
        CLAIMED.set(false);
    }

    /**
     * Called once by the agent before any JDK class is rewritten.
     * hookIds bounds the ids the table uses (ids are 1..hookIds-1);
     * stack=false records StackIds.NONE instead of walking (S1's cost
     * split, and an escape hatch while M3 engineers the walk down);
     * count=true keeps per-hook counts for the debug dump (an atomic
     * increment per bracket, so off unless asked for).
     */
    public static void configure(int hookIds, boolean stack, boolean count) {
        counts = new AtomicLongArray(count ? hookIds : 0);
        counting = count;
        stacks = stack ? WALK : NO_WALK;
    }

    /**
     * Creates and maps the ring in the tracer's spool directory and
     * returns its path; install time, before warmUp, so the warm-up
     * exercises the ring's paths too.
     */
    public static String openRing(String dir, long capacity) throws java.io.IOException {
        Ring r = Ring.create(java.nio.file.Path.of(dir), capacity);
        ring = r;
        return r.path().toString();
    }

    /**
     * Writes one HOOK record (a row of the agent's hook table) into the
     * ring; the agent calls it for every row at install, before any
     * bracket, so the joiner knows each hook's syscall allowlist. False
     * without a ring or when the record was dropped.
     */
    public static boolean describeHook(int id, String kind, String cls, String method,
                                       String nativeName, String syscalls) {
        Ring r = ring;
        return r != null && r.hook(id, kind, cls, method, nativeName, syscalls);
    }

    /**
     * Registers the probe of HTTP hook id (1..); install time, before the
     * first HTTP class is rewritten. A later registration of the same id
     * replaces the earlier one.
     */
    public static synchronized void httpProbe(int id, BiFunction<Object[], long[], String[]> probe) {
        BiFunction<Object[], long[], String[]>[] old = httpProbes;
        BiFunction<Object[], long[], String[]>[] p = java.util.Arrays.copyOf(old,
                Math.max(old.length, id + 1));
        p[id] = probe;
        httpProbes = p;
        if (counting && httpCounts.length() < p.length) {
            httpCounts = new AtomicLongArray(p.length);
        }
    }

    /**
     * A request that completes later on another thread (HttpClient's
     * sendAsync, task wd4) is recorded in two steps: its probe, still on
     * the caller's thread, takes the caller's identity with
     * {@link #httpCaller()}'s supplier — stack id, Java thread id, kernel
     * tid and OTEL span, so the event names who sent the request — and
     * the completion hands that identity (with the sending hook's id
     * appended) and a describer to {@link #httpLate()}'s consumer, which
     * runs the describer like a probe — the thread busy, meta to fill —
     * and writes the request. JDK types only, as for probes; the agent
     * core gets both once, reflectively.
     */
    public static java.util.function.Supplier<long[]> httpCaller() {
        return Hooks::caller;
    }

    /** See {@link #httpCaller()}. */
    public static java.util.function.BiConsumer<long[], java.util.function.Function<long[], String[]>>
            httpLate() {
        return Hooks::late;
    }

    /** Length of {@link #caller}'s result: stack, thread, ktid, OTEL hi, lo, span. */
    static final int CALLER = 6;

    /**
     * The calling thread's identity for a later record; called inside a
     * probe (the thread is busy, so the walk brackets nothing).
     */
    private static long[] caller() {
        Brackets t = STATE.get();
        Ring r = ring;
        if (r != null && !t.described && System.nanoTime() >= t.retryAt) {
            describe(t, r);
        }
        // Walked inside the probe: its frames are dropped too. Off like
        // every walk when configure(stack=false) said so.
        long stack = stacks == NO_WALK ? StackIds.NONE : StackIds.belowAgent();
        int ktid = t.virtual ? Brackets.lookupKtid() : t.kernelTid();
        Otel.read(t);
        return new long[] {stack, t.owner.getId(), ktid, t.otelHi, t.otelLo, t.otelSpan};
    }

    /**
     * Writes a request finished on the completing thread under the
     * caller's identity: c is caller() then the sending hook's id; the
     * describer fills meta (sizes -1, no start, until it says otherwise)
     * and returns the strings, or null. Stamped now, under the watermark
     * promise like any HTTP record; a busy thread or a failure counts as a
     * lost HTTP record. Never throws.
     */
    private static void late(long[] c, java.util.function.Function<long[], String[]> describer) {
        Ring r = ring;
        try {
            if (r == null || c == null || describer == null || c.length != CALLER + 1) {
                return;
            }
            Brackets t = STATE.get();
            if (t.busy) {
                countSkip(SKIPS);
                httpLost(r);
                return;
            }
            t.busy = true;
            try {
                if (lateRecord(t, r, c, describer)) {
                    int id = (int) c[CALLER];
                    if (id > 0 && id < httpCounts.length()) {
                        httpCounts.incrementAndGet(id);
                    }
                }
            } finally {
                t.busy = false;
            }
        } catch (Throwable e) {
            countSkip(FAILS);
            httpLost(r);
        }
    }

    /** late's record, marked closing from before its stamp (see request). */
    private static boolean lateRecord(Brackets t, Ring r, long[] c,
                                      java.util.function.Function<long[], String[]> describer) {
        t.closing = System.nanoTime();
        VarHandle.fullFence();   // as in close: the stamp after the mark
        try {
            long mono = System.nanoTime();
            long[] meta = new long[Ring.META];
            meta[Ring.M_REQ_BYTES] = -1;
            meta[Ring.M_RESP_BYTES] = -1;
            String[] s = describer.apply(meta);
            if (s == null) {
                return false;
            }
            if (meta[Ring.M_START] > mono) {
                meta[Ring.M_START] = 0;
            }
            r.http(mono, c[0], c[1], (int) c[2], s, meta, c[3], c[4], c[5]);
            return true;
        } finally {
            t.closing = Brackets.IDLE;
        }
    }

    /** Requests recorded so far per HTTP hook id (index = id). */
    public static long[] httpCounts() {
        AtomicLongArray c = httpCounts;
        long[] out = new long[c.length()];
        for (int i = 0; i < out.length; i++) {
            out[i] = c.get(i);
        }
        return out;
    }

    /** Starts the watermark thread (Watermarks); needs the ring. */
    public static void startWatermarks(long intervalMs) {
        Ring r = ring;
        if (r != null && intervalMs > 0) {
            Watermarks.start(r, intervalMs);
        }
    }

    /**
     * Marks the calling thread as the agent's own for good: its hooked
     * I/O is never bracketed. The agent calls it on its shutdown-hook
     * thread, iormesh-agent-exit, whose debug dump writes a file through
     * hooked JDK methods; that I/O is the agent's, not the target's.
     */
    public static void agentThread() {
        STATE.get().busy = true;
    }

    /** Writes a watermark now (the agent's shutdown hook). */
    public static void watermark() {
        Ring r = ring;
        if (r != null) {
            Watermarks.emit(r);
        }
    }

    /** Records dropped by the ring so far, or 0 without one. */
    public static long ringDropped() {
        Ring r = ring;
        return r == null ? 0 : r.dropped();
    }

    /**
     * Initializes and links what the hot path touches (the thread state,
     * the kernel tid lookup, the stack walker's stream machinery, the
     * ring's record paths) at install time, so none of that class
     * loading — and its jar reads — lands inside the target's own I/O
     * later. With a ring this writes real records: the installing
     * thread's THREAD, the warm-up stack's STACK and a first WATERMARK.
     */
    public static void warmUp() {
        Brackets t = STATE.get();
        t.busy = true;
        try {
            Ring r = ring;
            t.kernelTid();
            if (r != null) {
                describe(t, r);
                Watermarks.emit(r);
                r.dropped();
            }
            stacks.getAsLong();
            StackIds.belowAgent();   // sendAsync's caller walk, linked now too
            Otel.read(t);   // loads Otel now, not inside the target's first I/O
        } finally {
            t.busy = false;
        }
    }

    /**
     * Arms span-context reading for the OTEL copy that defined
     * lazyStorage (io.opentelemetry.context.LazyStorage): called by that
     * class's rewritten static initializer, at its end (OtelTransformer),
     * on the app thread that first used OTEL's context. Never throws into
     * it — a throw would fail the app's OTEL for good — and keeps the
     * thread busy, so the class loading arming does is not bracketed.
     */
    public static void otel(Class<?> lazyStorage) {
        try {
            Brackets t = STATE.get();
            boolean wasBusy = t.busy;
            t.busy = true;
            try {
                Otel.arm(lazyStorage, t);
            } finally {
                t.busy = wasBusy;
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
    }

    /** The OTEL copies armed so far and still loaded (debug dump). */
    public static int otelArmed() {
        return Otel.armed();
    }

    public static void enter(int hook) {
        Brackets t;
        try {
            t = STATE.get();
        } catch (Throwable e) {
            return;           // no state, no slot: exit finds none either
        }
        int d = t.depth++;
        if (d >= MAX_DEPTH) {
            return;           // too deep: exit sees d >= MAX_DEPTH and drops it
        }
        // Skipped until fully open, so a throw below leaves a balanced,
        // dropped slot instead of a half-written bracket.
        t.hooks[d] = SKIPPED;
        if (t.busy) {
            countSkip(SKIPS);
            return;
        }
        t.busy = true;
        try {
            Ring r = ring;
            if (r != null && !t.described && System.nanoTime() >= t.retryAt) {
                describe(t, r);
            }
            t.stacks[d] = stacks.getAsLong();
            Otel.read(t);
            t.traceHis[d] = t.otelHi;
            t.traceLos[d] = t.otelLo;
            t.spans[d] = t.otelSpan;
            if (t.virtual) {
                // The carrier now; close compares it with the one then.
                t.ktids[d] = Brackets.lookupKtid();
            }
            // Stamped after the walk, so the walk is outside the bracket.
            t.enters[d] = System.nanoTime();
            t.hooks[d] = hook;
        } catch (Throwable e) {
            countSkip(FAILS);  // e.g. StackOverflowError in a deep recursion
        } finally {
            t.busy = false;
        }
    }

    public static void exit(int hook) {
        try {
            Brackets t = STATE.get();
            if (t.depth == 0) {
                return;       // retransformed mid-call: no matching enter
            }
            int d = --t.depth;
            if (d >= MAX_DEPTH || t.hooks[d] != hook) {
                return;       // skipped, failed or overflowed enter
            }
            Ring r = ring;
            if (r != null) {
                close(t, d, r);
            }
            AtomicLongArray c = counts;
            if (hook > 0 && hook < c.length()) {
                c.incrementAndGet(hook);
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
    }

    /** A marker's strings: HTTP_FIXED empty ones. */
    private static final String[] NO_STRINGS = {"", "", "", "", "", "", ""};

    /**
     * The entry of a marking HTTP hook (Target.mark: HttpClient.sendAsync,
     * task wd4 review round 2): writes an HTTP record flagged
     * Ring.HTTP_ASYNC_MARK, stamped now, before the method body can hand
     * the request to any connection. Written even on a busy thread — it
     * runs no probe and walks no stack — under the watermark promise like
     * any HTTP record (closing marked before the stamp), so once the
     * tracer has a watermark past a request's stamp it has every marker
     * stamped before it. A marker the ring drops counts as a dropped HTTP
     * record, which stops the JVM's numbering altogether: the safe side.
     * Never throws into the target.
     */
    public static void httpMark(Object self, int id) {
        Ring r = ring;
        try {
            if (r == null) {
                return;
            }
            Brackets t = STATE.get();
            long wasClosing = t.closing;
            t.closing = System.nanoTime();
            VarHandle.fullFence();   // as in close: the stamp after the mark
            try {
                long[] meta = {0, 0, Ring.HTTP_ASYNC_MARK, 1, 0, -1, -1};
                r.http(System.nanoTime(), StackIds.NONE, t.owner.getId(), 0, NO_STRINGS, meta,
                        0, 0, 0);
            } finally {
                t.closing = wasClosing;
            }
        } catch (Throwable e) {
            countSkip(FAILS);
            httpLost(r);
        }
    }

    /**
     * The entry stamp of an entry-stamped HTTP hook (Target.stamp), called
     * first thing by its rewritten method: pushes System.nanoTime() and
     * self on the thread's HTTP stack, which {@link #httpExit} pops. It
     * pushes even on a busy thread — a stamp is no I/O and no probe — so
     * every exit finds its own entry. Never throws into the target.
     */
    public static void httpEnter(Object self, int id) {
        try {
            Brackets t = STATE.get();
            int d = t.httpDepth++;
            if (d < MAX_HTTP_DEPTH) {
                t.httpSelves[d] = self;
                t.httpStarts[d] = System.nanoTime();
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
    }

    /**
     * The exit of an entry-stamped HTTP hook: pops the stamp its entry
     * pushed and records like {@link #http}, with the method's own this
     * and start as the open hook.
     */
    public static void httpExit(Object ret, Object self, Object arg, int id) {
        long start = 0;
        Object open = null;
        try {
            Brackets t = STATE.get();
            if (t.httpDepth > 0) {
                int d = --t.httpDepth;
                if (d < MAX_HTTP_DEPTH) {
                    start = t.httpStarts[d];
                    open = t.httpSelves[d];
                    t.httpSelves[d] = null;
                }
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
        record(ret, self, arg, id, open, start);
    }

    /**
     * The entry point of HTTP hook id, called by the rewritten method
     * before each return (ret = the value returned, or null) and on its
     * exceptional exit (ret null); self is the method's this, arg the
     * argument its row names (or null). The open hook is the innermost
     * entry-stamped one still running on this thread, if any (a server's
     * exchange close inside the dispatch that read the request). Never
     * throws into the target.
     */
    public static void http(Object ret, Object self, Object arg, int id) {
        long start = 0;
        Object open = null;
        try {
            Brackets t = STATE.get();
            int d = t.httpDepth - 1;
            if (d >= 0 && d < MAX_HTTP_DEPTH) {
                start = t.httpStarts[d];
                open = t.httpSelves[d];
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
        record(ret, self, arg, id, open, start);
    }

    /**
     * Hands the hooked method's objects to id's probe and writes what it
     * describes. The thread is busy throughout, so the probe's own work —
     * reflection, never I/O — is not bracketed, and an HTTP hook reached
     * from inside another one's probe records nothing.
     */
    private static void record(Object ret, Object self, Object arg, int id, Object open,
                               long start) {
        Ring r = ring;
        try {
            BiFunction<Object[], long[], String[]>[] p = httpProbes;
            if (r == null || id <= 0 || id >= p.length || p[id] == null) {
                return;
            }
            Brackets t = STATE.get();
            if (t.busy) {
                countSkip(SKIPS);
                httpLost(r);
                return;
            }
            t.busy = true;
            try {
                if (request(t, r, p[id], new Object[] {ret, self, arg, open, id}, start)
                        && id < httpCounts.length()) {
                    httpCounts.incrementAndGet(id);
                }
            } finally {
                t.busy = false;
            }
        } catch (Throwable e) {
            countSkip(FAILS);
            httpLost(r);
        }
    }

    /**
     * The exit of a DNS hook (decision #8, task wd4): the probe of id
     * returns {host, address...} for a lookup that resolved and is new
     * to this JVM, or null, and that mapping becomes one DNS record with
     * the thread's stack. A lookup on a busy thread is the agent's own
     * (an HTTP probe parsing a literal) and records nothing; unlike an
     * HTTP record, a lost mapping shifts no count, so nothing stops.
     * Never throws into the target.
     */
    public static void dns(Object ret, Object self, Object arg, int id) {
        try {
            Ring r = ring;
            BiFunction<Object[], long[], String[]>[] p = httpProbes;
            if (r == null || id <= 0 || id >= p.length || p[id] == null) {
                return;
            }
            Brackets t = STATE.get();
            if (t.busy) {
                return;
            }
            t.busy = true;
            try {
                if (lookup(t, r, p[id], new Object[] {ret, self, arg, null, id})
                        && id < httpCounts.length()) {
                    httpCounts.incrementAndGet(id);
                }
            } finally {
                t.busy = false;
            }
        } catch (Throwable e) {
            countSkip(FAILS);
        }
    }

    /** dns's record, marked closing from before its stamp (see request). */
    private static boolean lookup(Brackets t, Ring r, BiFunction<Object[], long[], String[]> probe,
                                  Object[] args) {
        t.closing = System.nanoTime();
        VarHandle.fullFence();   // as in close: the stamp after the mark
        try {
            long mono = System.nanoTime();
            String[] s = probe.apply(args, new long[Ring.META]);
            if (s == null || s.length < 2) {
                return false;
            }
            if (!t.described && System.nanoTime() >= t.retryAt) {
                describe(t, r);
            }
            long stack = stacks.getAsLong();
            int ktid = t.virtual ? Brackets.lookupKtid() : t.kernelTid();
            Otel.read(t);
            r.dns(mono, stack, t.owner.getId(), ktid, s, t.otelHi, t.otelLo, t.otelSpan);
            return true;
        } finally {
            t.closing = Brackets.IDLE;
        }
    }

    /**
     * Counts a request this hook may have failed to record (a skipped or
     * failed call) as a dropped HTTP record: the tracer stops numbering
     * the JVM's requests once any is lost, which a silent failure here
     * would otherwise shift (task 3b4 review). A binding hook's failure
     * counts too — over-cautious, never a wrong ordinal.
     */
    private static void httpLost(Ring r) {
        try {
            if (r != null) {
                r.lost(Ring.HTTP);
            }
        } catch (Throwable ignored) {
            // Counting is best effort, like countSkip.
        }
    }

    /**
     * Asks the probe and writes its request as an HTTP record. The record
     * keeps the watermark promise like a bracket (close): the thread is
     * marked closing before its stamp is taken and until the record is
     * published, so no watermark passes the stamp while the probe and the
     * stack walk run. The walk happens only for a call that records;
     * true when the probe described a request (written or dropped).
     */
    private static boolean request(Brackets t, Ring r,
                                   BiFunction<Object[], long[], String[]> probe, Object[] args,
                                   long start) {
        t.closing = System.nanoTime();
        VarHandle.fullFence();   // as in close: the stamp after the mark
        try {
            long mono = System.nanoTime();
            long[] meta = new long[Ring.META];
            meta[Ring.M_START] = start;
            meta[Ring.M_REQ_BYTES] = -1;
            meta[Ring.M_RESP_BYTES] = -1;
            String[] s = probe.apply(args, meta);
            if (s == null) {
                return false;
            }
            if (meta[Ring.M_START] > mono) {
                meta[Ring.M_START] = 0;   // a stamp from after the exit is no start
            }
            if (!t.described && System.nanoTime() >= t.retryAt) {
                describe(t, r);
            }
            long stack = stacks.getAsLong();
            int ktid = t.virtual ? Brackets.lookupKtid() : t.kernelTid();
            Otel.read(t);
            r.http(mono, stack, t.owner.getId(), ktid, s, meta, t.otelHi, t.otelLo, t.otelSpan);
            return true;
        } finally {
            t.closing = Brackets.IDLE;
        }
    }

    /** The calling thread's state (the watermark thread's own, too). */
    static Brackets state() {
        return STATE.get();
    }

    /**
     * Writes slot d's BRACKET. The thread is marked closing (Watermarks)
     * before the exit is stamped and until the record is published, and
     * busy, so nothing the write might do is bracketed itself. A dropped
     * record is counted in the ring header, not here.
     */
    private static void close(Brackets t, int d, Ring r) {
        t.closing = t.enters[d];
        // The JMM does not order System.nanoTime() against volatiles:
        // without a full fence the clock read below may take effect
        // before the closing store is visible (AArch64's stlr has no
        // trailing barrier), and a watermark that read the clock later
        // could still see this thread idle and claim a time before our
        // exit. Paired with the fence in Watermarks.emit (Dekker).
        VarHandle.fullFence();
        boolean wasBusy = t.busy;
        t.busy = true;
        try {
            int ktid = t.carrierTid(d);
            long exit = System.nanoTime();
            r.bracket(t.hooks[d], ktid, t.enters[d], exit, t.stacks[d], t.owner.getId(),
                    t.traceHis[d], t.traceLos[d], t.spans[d]);
        } finally {
            t.busy = wasBusy;
            t.closing = Brackets.IDLE;
        }
    }

    /**
     * Writes the thread's THREAD record (its first bracket, or the
     * warm-up); a dropped one is retried at the next bracket, and the
     * kernel tid lookup — a syscall — happens only once regardless.
     * After a drop the retry waits RETRY_NS: under back-pressure every
     * bracket would otherwise encode the name again (an allocation) only
     * to be dropped again.
     */
    private static void describe(Brackets t, Ring r) {
        t.described = r.thread(t.owner.getId(), t.kernelTid(),
                t.virtual ? Ring.VIRTUAL : 0, t.owner.getName());
        if (!t.described) {
            t.retryAt = System.nanoTime() + RETRY_NS;
        }
    }

    /** Counts a dropped bracket; must not throw either. */
    private static void countSkip(AtomicLong n) {
        try {
            n.incrementAndGet();
        } catch (Throwable ignored) {
            // Counting is best effort; the target's call goes on.
        }
    }

    /** Closed brackets per hook id so far (index = hook id). */
    public static long[] counts() {
        AtomicLongArray c = counts;
        long[] out = new long[c.length()];
        for (int i = 0; i < out.length; i++) {
            out[i] = c.get(i);
        }
        return out;
    }

    /** Brackets skipped because the thread was inside the bridge already. */
    public static long skips() {
        return SKIPS.get();
    }

    /** Brackets dropped because opening or closing them threw. */
    public static long fails() {
        return FAILS.get();
    }

    /** The calling thread's open-bracket depth (tests). */
    static int depth() {
        return STATE.get().depth;
    }
}
