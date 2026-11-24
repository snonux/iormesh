package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.file.DirectoryStream;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * M2's fd lifecycle: one fd number reused for a second file, an fd
 * duplicated, and fds inherited across fork/exec into a child process
 * — the classic mis-attribution cases (docs/trace-scope.md).
 * Options: {@code --dir} (default: a fresh temp directory). Syscalls, in
 * order (strace, JDK 25 running the --release 21 classes; see the
 * expect.json for the exact list):
 *
 * <pre>
 *   reuse:   openat(D/first.dat) = fd1; write(fd1, 64); close(fd1)
 *            openat(D/second.dat) = fd2 (same number); write(fd2, 64); close(fd2)
 *   dup:     openat(D, O_RDONLY) = fd3 (strace shows no O_DIRECTORY);
 *            dup(fd3) = fd4; getdents64(fd3)...; close(fd3); close(fd4)
 *   inherit: Files.write fills D/in.dat (openat = fd5, write 64, close);
 *            the parent opens D/in.dat (fd6) and D/out.dat (fd7), spawns
 *            the child; the child dup2s them onto 0 and 1 and execs
 *            a child JVM, which reads 64 bytes on fd 0 and writes them
 *            on fd 1 ({@link FdChild}); the parent closes fd6 and fd7
 *   clean:   unlink the four files; rmdir(D)
 * </pre>
 *
 * The fd names are the golden's ($fd1...): each open binds a fresh name
 * even where the kernel hands out the same number again (as for fd2).
 *
 * The spawn is the JDK's posix_spawn: clone3(CLONE_VM|CLONE_VFORK),
 * then dup2(fd6', 0) and dup2(fd7', 1) on the child's inherited copies
 * (the golden's $fd8 and $fd9), then exec of lib/jspawnhelper, which
 * execs java in the same process;
 * the child JVM's main thread then does the stdio I/O. Where the dup2s
 * run is JDK-dependent — JDK 25's jspawnhelper issues them (and closes
 * the sources), JDK 27's posix_spawn file actions issue them before
 * jspawnhelper (and leave the sources to close-on-exec) — but both run
 * on the child's main thread (TID = PID), so the golden declares the
 * two dup2s there and not the closes. posix_spawn has been the JDK's
 * default launch mechanism on Linux since JDK 12, so the shape is
 * expected on the Java 21 floor too — expected, not measured. The
 * golden declares no process-lifecycle events for the child, only the
 * fd events either side and the forked edge (parent to child) the
 * correlator records at the fork. The child never emits markers of its
 * own: its events count as the scenario's because it was forked inside
 * the parent's bracket, which the normalizer's per-process cut follows
 * through that forked edge (M2 step 6).
 *
 * A dup event's fd is its argument (fd3): the golden format binds
 * returned fds only for opens and accepts, so the duplicate (fd4) is
 * declared through its close alone.
 *
 * JDK-only means no dup(2) API: Files.newDirectoryStream is the one
 * call that dups in-process (the JDK keeps one fd for the DIR stream
 * and one for the SecureDirectoryStream's *at() calls). The inherited
 * fds are the child's 0 and 1 because ProcessBuilder closes every other
 * fd in the child before exec — so stdio redirection is the inheritance
 * a JVM can produce, and the one real services rely on.
 */
public final class FdLifecycle implements Scenario {
    static final String NAME = "fd-lifecycle";
    static final int SIZE = 64;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            Tree.inFreshDir(wd.file(NAME + ".d"), dir -> {
                reuse(dir);
                dup(dir);
                inherit(dir);
                clean(dir);
            });
        }
    }

    /** Two files, one after the other: the kernel reuses the fd number. */
    private static void reuse(Path dir) throws IOException {
        byte[] data = payload('R');
        for (String name : List.of("first.dat", "second.dat")) {
            // FileOutputStream: one openat, one write(2), one close.
            try (FileOutputStream out = new FileOutputStream(dir.resolve(name).toFile())) {
                out.write(data);
            }
        }
    }

    /** Lists dir through a stream that dups its directory fd. */
    private static void dup(Path dir) throws IOException {
        int n = 0;
        try (DirectoryStream<Path> ds = Files.newDirectoryStream(dir)) {
            for (Path ignored : ds) {
                n++;
            }
        }
        if (n != 2) {
            throw new IOException("listed " + n + " entries, want 2: " + dir);
        }
    }

    /** Runs FdChild with stdin and stdout redirected to files. */
    private static void inherit(Path dir) throws Exception {
        byte[] data = payload('I');
        Path in = Files.write(dir.resolve("in.dat"), data);
        Path out = dir.resolve("out.dat");
        Process p = new ProcessBuilder(ChildJvm.command(FdChild.class))
                .redirectInput(in.toFile())
                .redirectOutput(out.toFile())
                // The child's own complaints stay visible on our stderr.
                .redirectError(ProcessBuilder.Redirect.INHERIT)
                .start();
        int code = ChildJvm.await(p, NAME);
        if (code != 0) {
            throw new IOException("fd-lifecycle child exited " + code);
        }
        if (!Arrays.equals(Files.readAllBytes(out), data)) {
            throw new IOException("child output differs from its input: " + out);
        }
    }

    /**
     * Deletes what the scenario made, so Tree.inFreshDir has nothing
     * left to walk — its walk would open and dup the directory again, an
     * extra dup inside the bracket the golden does not declare.
     */
    private static void clean(Path dir) throws IOException {
        for (String name : List.of("first.dat", "second.dat", "in.dat", "out.dat")) {
            Files.delete(dir.resolve(name));
        }
        Files.delete(dir);
    }

    private static byte[] payload(char c) {
        byte[] b = new byte[SIZE];
        Arrays.fill(b, (byte) c);
        return b;
    }
}
