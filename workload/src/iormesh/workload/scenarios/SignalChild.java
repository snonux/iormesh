package iormesh.workload.scenarios;

import java.io.FileDescriptor;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;

/**
 * The signals scenario's child process: announces it is up with one
 * byte on stdout, then waits for the parent's signal. A plain main
 * class with no markers of its own.
 *
 * It waits by reading stdin, and EOF does not end it: Process.destroy()
 * closes the parent's pipe ends right after kill(2), so EOF races the
 * signal, and exiting on it would turn SIGTERM's 143 into a 0. After
 * EOF it sleeps out a grace period instead — the signal ends it long
 * before — and exits 0 only if no signal came (the parent then reports
 * the failure), or if the parent died, so no orphan lingers for long.
 */
public final class SignalChild {
    static final int READY = 'R';
    static final long GRACE_MS = 10_000;

    private SignalChild() {}

    public static void main(String[] args) throws IOException, InterruptedException {
        new FileOutputStream(FileDescriptor.out).write(READY);
        FileInputStream in = new FileInputStream(FileDescriptor.in);
        while (in.read() != -1) {
            // Ignore input; only the signal (or EOF) ends this loop.
        }
        Thread.sleep(GRACE_MS);
    }
}
