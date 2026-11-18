package iormesh.bridge;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;

/**
 * The Java side of internal/agentspool's cross-language test (run by
 * `mage agentTest` through `go test`): several threads write to one
 * ring at once while the Go reader drains it, so the ring wraps many
 * times and the CAS on head is contended.
 *
 * Usage: RingStress DIR CAPACITY THREADS PER_THREAD. Thread i writes
 * PER_THREAD BRACKETs with hook = i + 1 and stack = its sequence number
 * (0, 1, …), and every 64th record a STACK whose frames carry the same
 * numbers and some non-ASCII text, so the reader can check order per
 * writer, string decoding and delivered + dropped = written. Waits for
 * a line on stdin before writing, so the reader is attached first; then
 * prints "written N" (records attempted) and exits.
 */
public final class RingStress {
    private RingStress() {}

    public static void main(String[] args) throws Exception {
        Ring ring = Ring.create(Path.of(args[0]), Long.parseLong(args[1]));
        int threads = Integer.parseInt(args[2]);
        int per = Integer.parseInt(args[3]);
        System.out.println("ready " + ring.path());
        System.out.flush();
        if (System.in.read() < 0) {
            throw new IllegalStateException("stdin closed before start");
        }
        List<Thread> ts = new ArrayList<>();
        for (int i = 0; i < threads; i++) {
            int id = i;
            ts.add(new Thread(() -> write(ring, id, per)));
        }
        ts.forEach(Thread::start);
        for (Thread t : ts) {
            t.join();
        }
        System.out.println("written " + (long) threads * (per + per / 64));
    }

    private static void write(Ring ring, int id, int per) {
        for (int seq = 0; seq < per; seq++) {
            ring.bracket(id + 1, 1000 + id, seq, seq + 1, seq, id);
            if (seq % 64 == 63) {
                ring.stack(seq, new String[] {"stress#w" + id + "(Gr\u00f6\u00dfe.java:" + seq + ")",
                        "java.lang.Thread#run(Thread.java:1)"});
            }
        }
    }
}
