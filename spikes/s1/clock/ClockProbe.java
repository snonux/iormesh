import java.io.FileOutputStream;
import java.io.PrintStream;
import java.nio.file.Files;
import java.nio.file.Path;

/**
 * Throwaway (S1, task xa4): the agent's clock against the kernel's.
 * Brackets N three-byte writes to /dev/null with System.nanoTime(), the
 * way an agent hook brackets a native I/O call, and dumps the brackets
 * after the loop (no I/O of its own inside it); writes are paced 20 us
 * apart so bpftrace keeps every stamp. clock.bt stamps each
 * write's sys_enter with nsecs(monotonic) and nsecs(boot); clock.py
 * checks that every kernel stamp falls inside its bracket, in each clock
 * domain, and how much margin is left.
 *
 * Usage: java ClockProbe.java N GATE OUT — prints its PID, waits until
 * GATE exists (so the tracer attaches first), runs, writes OUT.
 */
public final class ClockProbe {
    public static void main(String[] args) throws Exception {
        int n = Integer.parseInt(args[0]);
        Path gate = Path.of(args[1]);
        System.out.println(ProcessHandle.current().pid());
        System.out.flush();
        while (!Files.exists(gate)) {
            Thread.sleep(10);
        }
        long[] enter = new long[n];
        long[] exit = new long[n];
        byte[] three = {1, 2, 3};   // count == 3 tells clock.bt "ours"
        try (FileOutputStream out = new FileOutputStream("/dev/null")) {
            for (int i = 0; i < n; i++) {
                enter[i] = System.nanoTime();
                out.write(three);
                exit[i] = System.nanoTime();
                // Pace the loop (outside the bracket): bpftrace's output
                // buffer drops events at full write speed.
                while (System.nanoTime() - exit[i] < 20_000) {
                    Thread.onSpinWait();
                }
            }
        }
        try (PrintStream ps = new PrintStream(args[2])) {
            for (int i = 0; i < n; i++) {
                ps.println(enter[i] + "\t" + exit[i]);
            }
        }
    }
}
