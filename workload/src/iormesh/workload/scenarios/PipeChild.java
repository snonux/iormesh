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
        byte[] data = in.readNBytes(PipeIpc.SIZE);
        if (data.length != PipeIpc.SIZE) {
            System.exit(1);
        }
        new FileOutputStream(FileDescriptor.out).write(data);
        if (in.read() != -1) {
            System.exit(1);
        }
    }
}
