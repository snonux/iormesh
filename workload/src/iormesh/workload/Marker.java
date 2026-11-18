package iormesh.workload;

import java.io.FileInputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

/**
 * Emits the begin/end bracket every golden check cuts to. A marker is an
 * open(2) of a reserved path that never exists: it costs one failed
 * syscall, needs no tracer cooperation, and the correlator turns the
 * openat of /iormesh-marker/&lt;scenario&gt;/&lt;edge&gt; into a synthetic
 * marker event. Public because helper processes that are traced on their
 * own — restart's incarnations, matched by a pattern session that never
 * sees the launcher — must bracket themselves.
 */
public final class Marker {
    static final String ROOT = "/iormesh-marker/";

    // Checked once at class init, i.e. before the first begin marker and so
    // outside every bracket: if the reserved root exists (even as a
    // directory, which open(2) of a child would not reveal), markers are
    // ambiguous and every golden check on this host is suspect.
    static {
        if (Files.exists(Path.of(ROOT))) {
            throw new IllegalStateException("reserved marker root exists: " + ROOT);
        }
    }

    private Marker() {}

    public static void begin(String scenario) {
        touch(scenario, "begin");
    }

    public static void end(String scenario) {
        touch(scenario, "end");
    }

    /** The reserved path for one marker edge. */
    static String path(String scenario, String edge) {
        return ROOT + scenario + "/" + edge;
    }

    private static void touch(String scenario, String edge) {
        String path = path(scenario, edge);
        try {
            new FileInputStream(path).close();
            // The reserved root must never exist; if it does, the bracket
            // is ambiguous and every golden check on this host is suspect.
            throw new IllegalStateException("marker path exists: " + path);
        } catch (IOException expected) {
            // ENOENT is the point: the tracer sees the openat and its path.
        }
        // stderr, so stdout stays reserved for the PID the harness reads.
        System.err.println("marker " + scenario + "/" + edge);
    }
}
