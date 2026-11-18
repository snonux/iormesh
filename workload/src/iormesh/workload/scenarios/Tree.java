package iormesh.workload.scenarios;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.LinkOption;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.stream.Stream;

/**
 * Failure cleanup for scenarios that build a directory tree in their
 * workdir. On a clean run the scenario has deleted everything itself,
 * as declared syscalls, so the cleanup finds nothing and adds just one
 * undeclared lstat to the bracket.
 */
final class Tree {
    private Tree() {}

    /** A scenario body working inside the directory it was handed. */
    interface Body {
        void run(Path dir) throws Exception;
    }

    /**
     * Creates dir (its mkdir is the scenario's first declared syscall),
     * runs body in it, then removes whatever the body left behind. The
     * cleanup only starts once createDirectory succeeded: a dir that
     * already existed — left by someone else in a reused --dir — is not
     * ours to delete, so that run just fails. A cleanup error never
     * replaces the body's: it rides along as a suppressed exception.
     */
    static void inFreshDir(Path dir, Body body) throws Exception {
        Files.createDirectory(dir);
        try {
            body.run(dir);
        } catch (Throwable t) {
            try {
                delete(dir);
            } catch (IOException e) {
                t.addSuppressed(e);
            }
            throw t;
        }
        delete(dir);
    }

    /** Removes root and everything under it; absent root is fine. */
    private static void delete(Path root) throws IOException {
        if (!Files.exists(root, LinkOption.NOFOLLOW_LINKS)) {
            return;
        }
        List<Path> paths = new ArrayList<>();
        // Files.walk does not follow symlinks by default, so a planted
        // link never leads the cleanup outside the tree.
        try (Stream<Path> s = Files.walk(root)) {
            s.forEach(paths::add);
        }
        // Children before parents: walk lists a directory before its
        // entries, so the reversed list empties each directory first.
        for (int i = paths.size() - 1; i >= 0; i--) {
            Files.deleteIfExists(paths.get(i));
        }
    }
}
