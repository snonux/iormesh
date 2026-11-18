package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.util.Map;
import java.util.Set;

/**
 * M1's file scenario: write 4096 bytes to a file, read them back, close
 * (see {@link FileWork} for the exact syscalls). Options: {@code --dir}
 * (default: a fresh temp directory).
 */
public final class FileBasic implements Scenario {
    static final String NAME = "file-basic";

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of(Workdir.OPTION));
        try (Workdir wd = Workdir.open(opts)) {
            FileWork.run(wd.file(NAME + ".dat"));
        }
    }
}
