package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Arrays;
import java.util.Map;
import java.util.Set;

/**
 * M2's link ops: a symlink and a hard link to one file, each read back
 * the way a reader of the trace must tell apart — lstat (the link
 * itself), readlink (its text), open (follows it). Options: {@code
 * --dir} (default: a fresh temp directory). Syscalls on the calling
 * thread, in order (strace, JDK 25 running the --release 21 classes):
 *
 * <pre>
 *   mkdir(D)                               D = $tmp/file-links.d
 *   openat(D/target.dat, O_WRONLY|O_CREAT|O_TRUNC) = fd1; write(fd1, 64); close(fd1)
 *   symlink("target.dat", D/sym.lnk)       relative target, as tools make them
 *   readlink(D/sym.lnk)
 *   statx(D/sym.lnk, AT_SYMLINK_NOFOLLOW)  lstat; JDKs before 22: newfstatat
 *   openat(D/sym.lnk, O_RDONLY) = fd2; read(fd2, 64); read(fd2) = 0; close(fd2)
 *   link(D/target.dat, D/hard.dat)
 *   openat(D/hard.dat, O_RDONLY) = fd3; read(fd3, 64); read(fd3) = 0; close(fd3)
 *   unlink(D/sym.lnk); unlink(D/hard.dat); unlink(D/target.dat); rmdir(D)
 * </pre>
 *
 * The open through the symlink carries the link's path, not the
 * target's: resolving it to target.dat is the correlator's job (via the
 * fd's inode), which is what this scenario lets M2 prove. So
 * expect.json declares an opened edge for target.dat only; how opens
 * via sym.lnk and hard.dat show in the graph is for M2 to decide. The
 * EOF reads (readAllBytes probing for more) are left undeclared.
 */
public final class FileLinks implements Scenario {
    static final String NAME = "file-links";
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
            Tree.inFreshDir(wd.file(NAME + ".d"), FileLinks::links);
        }
    }

    /** Runs in D, which Tree.inFreshDir has just made (the mkdir). */
    private static void links(Path dir) throws IOException {
        Path target = dir.resolve("target.dat");
        byte[] data = new byte[SIZE];
        Arrays.fill(data, (byte) 'L');
        Files.write(target, data);
        symlink(dir, target, data);
        Path hard = Files.createLink(dir.resolve("hard.dat"), target);
        expectContent(hard, data);
        Files.delete(dir.resolve("sym.lnk"));
        Files.delete(hard);
        Files.delete(target);
        Files.delete(dir);
    }

    /** Creates sym.lnk → target.dat and reads it three ways. */
    private static void symlink(Path dir, Path target, byte[] data) throws IOException {
        Path rel = target.getFileName();
        Path link = Files.createSymbolicLink(dir.resolve("sym.lnk"), rel);
        if (!Files.readSymbolicLink(link).equals(rel)) {
            throw new IOException("readlink differs from " + rel + ": " + link);
        }
        // isSymbolicLink stats without following: true only for the link.
        if (!Files.isSymbolicLink(link)) {
            throw new IOException("not a symlink: " + link);
        }
        expectContent(link, data);
    }

    private static void expectContent(Path p, byte[] want) throws IOException {
        if (!Arrays.equals(Files.readAllBytes(p), want)) {
            throw new IOException("content differs: " + p);
        }
    }
}
