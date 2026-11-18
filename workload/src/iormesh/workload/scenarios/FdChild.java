package iormesh.workload.scenarios;

import java.io.FileDescriptor;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;

/**
 * fd-lifecycle's child process: copies its inherited stdin to its
 * inherited stdout. It is a plain main class, not a scenario — it has
 * no markers of its own; the parent's bracket spans its whole life.
 * The fds are used raw (no System.in/System.out buffering), so the
 * syscalls are exactly what fd-lifecycle's expect.json declares.
 */
public final class FdChild {
    private FdChild() {}

    public static void main(String[] args) throws IOException {
        // readAllBytes on a FileInputStream sizes its buffer from the
        // file, so a 64-byte stdin file is one read(2) plus one at EOF.
        byte[] data = new FileInputStream(FileDescriptor.in).readAllBytes();
        new FileOutputStream(FileDescriptor.out).write(data);
    }
}
