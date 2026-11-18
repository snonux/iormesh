package iormesh.workload.scenarios;

import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;

/**
 * file-basic's file work, shared with every scenario that also needs
 * file edges (tcp-loopback's both sides). Syscalls, in order, on the
 * calling thread (strace, JDK 25 running the --release 21 classes; the
 * JVM adds an fstat after each open):
 *
 * <pre>
 *   openat(path, O_WRONLY|O_CREAT|O_TRUNC) = fdA; write(fdA, 4096) = 4096; close(fdA)
 *   openat(path, O_RDONLY) = fdB;  read(fdB, 4096) = 4096;  close(fdB)
 *   unlink(path)
 * </pre>
 *
 * Two opens, because FileOutputStream and FileInputStream each own an
 * fd; the kernel usually hands fdB the number fdA just freed, which is
 * why expect.json tells them apart by symbol ($fd1, $fd2), not number.
 * Class loading for the scenario also happens inside the bracket (reads
 * on the jar's fd); undeclared, so the golden check ignores it.
 */
final class FileWork {
    static final int SIZE = 4096;

    private FileWork() {}

    static void run(Path file) throws IOException {
        byte[] data = pattern();
        try {
            write(file, data);
            byte[] back = read(file);
            if (!Arrays.equals(data, back)) {
                throw new IOException("read back differs from what was written: " + file);
            }
        } finally {
            // Always remove the file, also after a failure, so a --dir the
            // harness reuses never holds a stale file from an earlier run.
            Files.deleteIfExists(file);
        }
    }

    /** A fixed byte pattern: the same content, run after run. */
    private static byte[] pattern() {
        byte[] b = new byte[SIZE];
        for (int i = 0; i < SIZE; i++) {
            b[i] = (byte) i;
        }
        return b;
    }

    private static void write(Path file, byte[] data) throws IOException {
        // One write(2): FileOutputStream.write(byte[]) hands the whole
        // array to the kernel, and a regular file takes it all at once.
        try (FileOutputStream out = new FileOutputStream(file.toFile())) {
            out.write(data);
        }
    }

    private static byte[] read(Path file) throws IOException {
        byte[] back = new byte[SIZE];
        // readNBytes into a sized buffer stops at SIZE, so a full first
        // read(2) is the only one — no trailing read for EOF.
        try (FileInputStream in = new FileInputStream(file.toFile())) {
            int n = in.readNBytes(back, 0, SIZE);
            if (n != SIZE) {
                throw new IOException("short read: " + n + " of " + SIZE + " bytes");
            }
        }
        return back;
    }
}
