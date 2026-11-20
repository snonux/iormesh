package iormesh.workload.scenarios;

import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardCopyOption;

/**
 * A microservices service's data records, saved the way services persist
 * a file safely: written to a temp name first, then renamed over the
 * final one, so a reader never sees half a record. Syscalls, in order,
 * on the calling thread (the JVM adds stat calls, undeclared):
 *
 * <pre>
 *   openat(D/name.tmp, O_WRONLY|O_CREAT|O_TRUNC) = fd; write(fd, n); close(fd)
 *   rename(D/name.tmp, D/name)
 * </pre>
 *
 * Both happen in {@link #save} itself, so it is the causing method of all
 * four events (expect.json; the eval's "which method wrote order-42.json").
 */
final class DataStore {
    private DataStore() {}

    /** Saves content as dir/name via dir/name.tmp; returns the record. */
    static Path save(Path dir, String name, String content) throws IOException {
        Path tmp = dir.resolve(name + ".tmp");
        Path target = dir.resolve(name);
        // One write(2): FileOutputStream.write(byte[]) hands the whole
        // array to the kernel, and a regular file takes it all at once.
        try (FileOutputStream out = new FileOutputStream(tmp.toFile())) {
            out.write(content.getBytes(StandardCharsets.UTF_8));
        }
        // ATOMIC_MOVE is a plain rename(2) on Linux: no copy fallback, so
        // the record either appears whole or not at all.
        Files.move(tmp, target, StandardCopyOption.ATOMIC_MOVE);
        return target;
    }
}
