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
 * workdir. Only the finally paths call it: on a clean run the scenario
 * has deleted everything itself, as declared syscalls, so delete() finds
 * nothing and adds just one undeclared lstat to the bracket.
 */
final class Tree {
    private Tree() {}

    /** Removes root and everything under it; absent root is fine. */
    static void delete(Path root) throws IOException {
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
