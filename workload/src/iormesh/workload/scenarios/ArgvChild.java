package iormesh.workload.scenarios;

import java.io.FileDescriptor;
import java.io.FileOutputStream;
import java.io.IOException;

/**
 * argv-secret's child process: its command line is the point (see
 * {@link ArgvSecret}), so it ignores its arguments, announces it ran
 * with one byte on stdout and exits. A plain main class with no markers
 * of its own; the parent's bracket spans its whole life.
 */
public final class ArgvChild {
    static final int READY = 'A';

    private ArgvChild() {}

    public static void main(String[] args) throws IOException {
        new FileOutputStream(FileDescriptor.out).write(READY);
    }
}
