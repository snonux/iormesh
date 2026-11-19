package iormesh.bridge;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;

/**
 * The Java side of internal/agentspool's cross-language tests (run by
 * `mage agentTest` through `go test`): several threads write to one
 * ring at once while the Go reader drains it, so the ring wraps many
 * times and the CAS on head is contended.
 *
 * Usage: RingStress MODE DIR CAPACITY THREADS PER_THREAD.
 * <ul>
 * <li>{@code raw}: thread i writes PER_THREAD BRACKETs straight to the
 *     ring with hook = i + 1 and stack = its sequence number (0, 1, …),
 *     and every 64th record a STACK whose frames carry the same numbers
 *     and some non-ASCII text, so the reader can check order per writer,
 *     string decoding and delivered + dropped = written. Prints
 *     "written N", every record attempted.</li>
 * <li>{@code hooks}: each thread closes PER_THREAD brackets of hook 1
 *     through Hooks, all from one call site (so the threads race on the
 *     first sighting of one stack id), while the watermark thread runs
 *     every millisecond; the reader checks the watermark promise and
 *     that every id resolves. Prints "written N", the brackets.</li>
 * </ul>
 * Waits for a line on stdin before writing, so the reader is attached
 * first.
 */
public final class RingStress {
    private RingStress() {}

    public static void main(String[] args) throws Exception {
        boolean hooks = args[0].equals("hooks");
        Ring ring = Ring.create(Path.of(args[1]), Long.parseLong(args[2]));
        int threads = Integer.parseInt(args[3]);
        int per = Integer.parseInt(args[4]);
        System.out.println("ready " + ring.path());
        System.out.flush();
        if (System.in.read() < 0) {
            throw new IllegalStateException("stdin closed before start");
        }
        if (hooks) {
            Hooks.configure(2, true, false);
            Hooks.ring = ring;
            // As the agent's install does: the table ahead of any bracket.
            Hooks.describeHook(1, "write", "iormesh/bridge/RingStress", "stress", "none", "write");
            Watermarks.start(ring, 1);
        }
        List<Thread> ts = new ArrayList<>();
        for (int i = 0; i < threads; i++) {
            int id = i;
            ts.add(new Thread(() -> {
                if (hooks) {
                    brackets(per);
                } else {
                    write(ring, id, per);
                }
            }));
        }
        ts.forEach(Thread::start);
        for (Thread t : ts) {
            t.join();
        }
        if (hooks) {
            Watermarks.emit(ring);
        }
        System.out.println("written " + (long) threads * (hooks ? per : per + per / 64));
    }

    /** Brackets through the real entry points; the spin widens the bracket. */
    private static void brackets(int per) {
        long sink = 0;
        for (int seq = 0; seq < per; seq++) {
            Hooks.enter(1);
            for (int i = 0; i < seq % 16; i++) {
                sink += System.nanoTime();
            }
            Hooks.exit(1);
        }
        if (sink == 42) {
            System.out.print("");  // keeps the spin from being optimized away
        }
    }

    private static void write(Ring ring, int id, int per) {
        for (int seq = 0; seq < per; seq++) {
            ring.bracket(id + 1, 1000 + id, seq, seq + 1, seq, id);
            if (seq % 64 == 63) {
                ring.stack(seq, new long[] {seq + 1, 1}, new String[] {"stress#w" + id + "(Gr\u00f6\u00dfe.java:" + seq + ")",
                        "java.lang.Thread#run(Thread.java:1)"});
            }
        }
    }
}
