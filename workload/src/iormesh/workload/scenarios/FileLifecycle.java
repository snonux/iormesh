package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.nio.ByteBuffer;
import java.nio.channels.FileChannel;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.nio.file.attribute.FileTime;
import java.time.Instant;
import java.util.Map;
import java.util.Set;

/**
 * M2's structural file ops: what happened to a file besides its data —
 * created, renamed while open, moved, truncated, touched, deleted.
 * Options: {@code --dir} (default: a fresh temp directory). Syscalls on
 * the calling thread, in order (strace, JDK 25 running the --release 21
 * classes; the JDK adds stat calls around most of them, undeclared):
 *
 * <pre>
 *   mkdir(D)                                  D = $tmp/file-lifecycle.d
 *   openat(D/a.dat, O_WRONLY|O_CREAT|O_EXCL) = fd1
 *   write(fd1, 512)
 *   rename(D/a.dat, D/b.dat)                  fd1 stays open
 *   write(fd1, 512)                           lands in D/b.dat
 *   ftruncate(fd1, 512)
 *   close(fd1)
 *   openat(D/b.dat, O_RDONLY) = fd2
 *   utimensat(fd2, NULL, times)               touch, futimens form
 *   close(fd2)
 *   mkdir(D/sub)
 *   rename(D/b.dat, D/sub/c.dat)              cross-directory move
 *   unlink(D/sub/c.dat); rmdir(D/sub); rmdir(D)
 * </pre>
 *
 * The write after the rename is the point of the scenario: a correlator
 * that keeps fd1's path as D/a.dat attributes it to a file that no longer
 * exists (PLAN.md M2, rename-aware paths). Truncating back to the first
 * write's size makes the final content checkable. The JDK sets times
 * through an fd of its own (open, then utimensat with a NULL path), so
 * "touch" arrives as an fd event, not a path event.
 *
 * expect.json declares an opened edge for b.dat only: whether a.dat's
 * edge is rewritten by the rename is the rename-aware correlator's
 * decision (M2 step 5), not something this golden presumes.
 */
public final class FileLifecycle implements Scenario {
    static final String NAME = "file-lifecycle";
    static final int CHUNK = 512;
    // A fixed, non-current mtime, so the touch is visible as a change.
    static final FileTime TOUCHED = FileTime.from(Instant.parse("2020-01-02T03:04:05Z"));

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            Path dir = wd.file(NAME + ".d");
            try {
                lifecycle(dir);
            } finally {
                // A failed run must not leave a tree behind in a --dir the
                // harness reuses; a clean run has removed it already.
                Tree.delete(dir);
            }
        }
    }

    private static void lifecycle(Path dir) throws IOException {
        Files.createDirectory(dir);
        Path b = createRenamedWhileOpen(dir);
        Files.setLastModifiedTime(b, TOUCHED);
        if (!Files.getLastModifiedTime(b).equals(TOUCHED)) {
            throw new IOException("touch did not set mtime: " + b);
        }
        Path sub = Files.createDirectory(dir.resolve("sub"));
        Path c = Files.move(b, sub.resolve("c.dat"));
        if (Files.size(c) != CHUNK) {
            throw new IOException("moved file has " + Files.size(c) + " bytes: " + c);
        }
        Files.delete(c);
        Files.delete(sub);
        Files.delete(dir);
    }

    /**
     * Creates a.dat, renames it to b.dat while the fd stays open, writes
     * through that fd again and truncates; returns b.dat.
     */
    private static Path createRenamedWhileOpen(Path dir) throws IOException {
        Path a = dir.resolve("a.dat");
        Path b = dir.resolve("b.dat");
        // CREATE_NEW is O_CREAT|O_EXCL: "create", not "open or create".
        try (FileChannel ch = FileChannel.open(a, StandardOpenOption.CREATE_NEW,
                StandardOpenOption.WRITE)) {
            writeChunk(ch);
            Files.move(a, b);
            writeChunk(ch);
            ch.truncate(CHUNK);
        }
        return b;
    }

    /** One write(2) of CHUNK bytes; a regular file takes it whole. */
    private static void writeChunk(FileChannel ch) throws IOException {
        ByteBuffer buf = ByteBuffer.allocate(CHUNK);
        int n = ch.write(buf);
        if (n != CHUNK) {
            throw new IOException("short write: " + n + " of " + CHUNK);
        }
    }
}
