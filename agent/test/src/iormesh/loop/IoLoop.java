package iormesh.loop;

import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Path;

/**
 * A JVM that keeps doing file I/O until its stdin closes, for
 * internal/agentspool's Java-driven resync test (task 5j4): run under
 * the agent with a spool, it brackets the same few call sites over and
 * over, so a tracer session opened late still sees each of them again
 * after the agent resent its dictionary. A daemon thread waits for EOF on
 * stdin and ends the JVM, so the test controls its life.
 *
 * Usage: IoLoop DIR — writes and reads DIR/loop.dat every 2 ms.
 */
public final class IoLoop {
    private static final long PAUSE_MS = 2;

    private IoLoop() {}

    public static void main(String[] args) throws Exception {
        Path file = Path.of(args[0], "loop.dat");
        Thread stop = new Thread(IoLoop::awaitEof, "stdin-watch");
        stop.setDaemon(true);
        stop.start();
        System.out.println("looping");
        System.out.flush();
        byte[] data = new byte[64];
        byte[] back = new byte[64];
        while (stop.isAlive()) {
            Writer.write(file, data);
            Reader.read(file, back);
            Thread.sleep(PAUSE_MS);
        }
    }

    /** Returns at EOF on stdin, which ends the loop. */
    private static void awaitEof() {
        try {
            while (System.in.read() >= 0) {
                // A line only keeps it going; EOF stops it.
            }
        } catch (IOException ignored) {
            // A broken stdin stops the loop too.
        }
    }

    /** One call site for the writes, its own class (a CLASS record). */
    static final class Writer {
        static void write(Path f, byte[] data) throws IOException {
            try (FileOutputStream out = new FileOutputStream(f.toFile())) {
                out.write(data);
            }
        }
    }

    /** One call site for the reads. */
    static final class Reader {
        static void read(Path f, byte[] into) throws IOException {
            try (FileInputStream in = new FileInputStream(f.toFile())) {
                if (in.read(into) < 0) {
                    throw new IOException("empty " + f);
                }
            }
        }
    }
}
