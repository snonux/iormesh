package iormesh.workload.scenarios;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Map;

/**
 * The directory a scenario puts its files in. With {@code --dir} it is
 * that existing directory — the harness passes its own, so the
 * normalizer can rewrite the prefix to {@code $tmp} — otherwise a fresh
 * temp directory that close() removes again, so standalone runs never
 * collide with each other or leave litter.
 */
final class Workdir implements AutoCloseable {
    static final String OPTION = "dir";

    private final Path dir;
    private final boolean owned;

    private Workdir(Path dir, boolean owned) {
        this.dir = dir;
        this.owned = owned;
    }

    static Workdir open(Map<String, String> opts) throws IOException {
        String given = opts.get(OPTION);
        if (given == null) {
            return temp();
        }
        Path dir = Path.of(given);
        if (!Files.isDirectory(dir)) {
            throw new IOException("--dir is not a directory: " + dir);
        }
        return new Workdir(dir, false);
    }

    static Workdir temp() throws IOException {
        return new Workdir(Files.createTempDirectory("iormesh-workload-"), true);
    }

    Path file(String name) {
        return dir.resolve(name);
    }

    @Override
    public void close() throws IOException {
        if (owned) {
            Files.deleteIfExists(dir);
        }
    }
}
