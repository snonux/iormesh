package iormesh.workload.scenarios;

import java.io.FileDescriptor;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;

/**
 * pipe-ipc's child process: echoes one 64-byte message from its stdin
 * pipe to its stdout pipe, then waits for EOF on stdin before exiting,
 * so the parent always reads the echo before the child is gone (see
 * PipeIpc). A plain main class with no markers of its own. The fds are
 * used raw (no System.in/System.out buffering), so each step is one
 * read(2) or write(2). Exit 1 means the parent broke the protocol.
 */
public final class PipeChild {
    private PipeChild() {}

    public static void main(String[] args) throws IOException {
        FileInputStream in = new FileInputStream(FileDescriptor.in);
        byte[] data = new byte[PipeIpc.SIZE];
        // A plain read loop, not readNBytes(int): some JDK 21 updates
        // (Temurin 21.0.13) have FileInputStream.readNBytes ask for the
        // file position first, which throws "Illegal seek" on a pipe.
        int n = 0;
        while (n < data.length) {
            int got = in.read(data, n, data.length - n);
            if (got < 0) {
                System.exit(1);
            }
            n += got;
        }
        new FileOutputStream(FileDescriptor.out).write(data);
        if (in.read() != -1) {
            System.exit(1);
        }
    }
}
