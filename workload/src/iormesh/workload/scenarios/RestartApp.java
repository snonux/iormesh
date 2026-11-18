package iormesh.workload.scenarios;

import iormesh.workload.Marker;
import java.io.FileInputStream;
import java.io.FileNotFoundException;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;

/**
 * One incarnation of the restart scenario's app: recover the checkpoint
 * the previous incarnation left (none on a first start), then write its
 * own. Usage: {@code RestartApp <dir>}. Every incarnation gets the
 * identical argv, so a pattern-bound session matches each one alike; the
 * state file alone tells them apart, as a real app's on-disk state does.
 *
 * It brackets itself with the restart scenario's markers (see Restart):
 * a pattern session never traces the launcher, so the incarnation must
 * carry its own bracket.
 */
public final class RestartApp {
    static final String STATE_FILE = "restart.state";
    // Fixed width ("incarn-N", 8 bytes, N < 10): one read(2) of exactly
    // this size recovers it, so the declared byte counts hold.
    static final int STATE_LEN = 8;
    private static final String PREFIX = "incarn-";

    private RestartApp() {}

    public static void main(String[] args) throws IOException {
        if (args.length != 1) {
            System.err.println("usage: RestartApp <dir>");
            System.exit(2);
        }
        Path state = Path.of(args[0]).resolve(STATE_FILE);
        Marker.begin(Restart.NAME);
        int previous = recover(state);
        save(state, previous + 1);
        Marker.end(Restart.NAME);
    }

    /** The state an incarnation writes: its number, fixed width. */
    static String checkpoint(int incarnation) {
        if (incarnation < 1 || incarnation > 9) {
            throw new IllegalArgumentException("incarnation out of range: " + incarnation);
        }
        return PREFIX + incarnation;
    }

    /** The previous incarnation's number, or 0 on a first start. */
    private static int recover(Path state) throws IOException {
        byte[] buf = new byte[STATE_LEN];
        int n;
        try (FileInputStream in = new FileInputStream(state.toFile())) {
            n = in.read(buf);
        } catch (FileNotFoundException first) {
            return 0;
        }
        String got = new String(buf, 0, Math.max(n, 0), StandardCharsets.US_ASCII);
        char digit = n == STATE_LEN ? got.charAt(STATE_LEN - 1) : ' ';
        if (!got.startsWith(PREFIX) || !Character.isDigit(digit)) {
            throw new IOException("bad restart state: " + got);
        }
        return digit - '0';
    }

    private static void save(Path state, int incarnation) throws IOException {
        try (FileOutputStream out = new FileOutputStream(state.toFile())) {
            out.write(checkpoint(incarnation).getBytes(StandardCharsets.US_ASCII));
        }
    }
}
