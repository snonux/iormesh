package iormesh.s1.bridge;

import java.io.PrintStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.stream.Collectors;

/**
 * Throwaway (S1, task wa4). Loaded by the bootstrap loader, because the
 * rewritten java.base methods can only link against classes it sees.
 * Each hooked call records (kernel TID, hook, enter/exit nanoTime, the
 * caller stack) in memory; nothing is written until JVM shutdown, so
 * the agent adds no I/O of its own inside the scenario bracket. The
 * tracer-side join (agentjoin.py) matches these records to the uprobe
 * marks by TID and time: bpftrace's nsecs and System.nanoTime() are both
 * CLOCK_MONOTONIC on Linux, so no clock conversion is needed here.
 */
public final class Hooks {
    /** Set while the hook itself runs, and for good once dumping starts. */
    private static final ThreadLocal<boolean[]> BUSY =
            ThreadLocal.withInitial(() -> new boolean[1]);
    private static final ThreadLocal<long[]> KTID = new ThreadLocal<>();
    private static final ThreadLocal<ArrayList<Rec>> OPEN =
            ThreadLocal.withInitial(ArrayList::new);
    private static final List<Rec> DONE = new ArrayList<>();
    private static volatile boolean dumping;
    private static final StackWalker WALKER = StackWalker.getInstance();

    /** One hooked call; exit is stamped on normal and exceptional return. */
    private record Rec(int hook, long ktid, String thread, long enter, String stack,
                       long[] exit) {}

    private Hooks() {}

    public static void enter(int hook) {
        boolean[] busy = BUSY.get();
        if (busy[0] || dumping) {
            OPEN.get().add(null);   // keep enter/exit pairs balanced
            return;
        }
        busy[0] = true;
        try {
            long t0 = System.nanoTime();
            OPEN.get().add(new Rec(hook, kernelTid(), Thread.currentThread().getName(),
                    t0, stack(), new long[1]));
        } finally {
            busy[0] = false;
        }
    }

    public static void exit(int hook) {
        long t1 = System.nanoTime();
        ArrayList<Rec> open = OPEN.get();
        if (open.isEmpty()) {
            return;
        }
        Rec r = open.remove(open.size() - 1);
        if (r == null) {
            return;
        }
        r.exit()[0] = t1;
        synchronized (DONE) {
            DONE.add(r);
        }
    }

    /** /proc/thread-self resolves to /proc/PID/task/TID: one readlink per thread. */
    private static long kernelTid() {
        long[] cached = KTID.get();
        if (cached != null) {
            return cached[0];
        }
        long tid = -1;
        try {
            tid = Long.parseLong(Files.readSymbolicLink(Path.of("/proc/thread-self"))
                    .getFileName().toString());
        } catch (Exception e) {
            // Leave -1: the record then cannot join, which agentjoin reports.
        }
        KTID.set(new long[] {tid});
        return tid;
    }

    /** Caller frames above the hooked JDK method, innermost first. */
    private static String stack() {
        return WALKER.walk(s -> s.skip(2).limit(10)
                .map(f -> f.getClassName() + "." + f.getMethodName() + ":" + f.getLineNumber())
                .collect(Collectors.joining(" < ")));
    }

    /** Writes every record as tab-separated lines; called once, at shutdown. */
    public static void dump(String path) {
        dumping = true;
        try (PrintStream out = new PrintStream(path)) {
            synchronized (DONE) {
                for (Rec r : DONE) {
                    out.printf("%d\t%d\t%s\t%d\t%d\t%s%n", r.hook(), r.ktid(), r.thread(),
                            r.enter(), r.exit()[0], r.stack());
                }
            }
        } catch (Exception e) {
            e.printStackTrace();
        }
    }
}
