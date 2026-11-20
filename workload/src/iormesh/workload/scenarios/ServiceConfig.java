package iormesh.workload.scenarios;

import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.util.Arrays;

/**
 * A microservices service's config file: provisioned into the service's
 * fresh directory (what a deployment would ship), then read back the way
 * the service boots from it. Syscalls, in order, on the calling thread:
 *
 * <pre>
 *   openat(D/role.conf, O_WRONLY|O_CREAT|O_TRUNC) = fdA; write(fdA, n); close(fdA)
 *   openat(D/role.conf, O_RDONLY) = fdB; read(fdB, n); close(fdB)
 * </pre>
 *
 * The read is a single read(2) into a buffer larger than any config, not
 * readAllBytes, which would add a second read for the EOF.
 */
final class ServiceConfig {
    // Far above the few dozen bytes a config holds; a config that fills it
    // is rejected rather than read in pieces.
    private static final int MAX = 1024;

    private ServiceConfig() {}

    /** Writes text as the config file. */
    static void provision(Path file, String text) throws IOException {
        try (FileOutputStream out = new FileOutputStream(file.toFile())) {
            out.write(text.getBytes(StandardCharsets.UTF_8));
        }
    }

    /** Reads the config file and checks it holds want. */
    static void load(Path file, String want) throws IOException {
        byte[] buf = new byte[MAX];
        int n;
        try (FileInputStream in = new FileInputStream(file.toFile())) {
            n = in.read(buf);
        }
        String got = n < 0 ? "" : new String(buf, 0, n, StandardCharsets.UTF_8);
        if (n >= MAX || !got.equals(want)) {
            throw new IOException("config " + file + " differs from what was provisioned: "
                    + Arrays.toString(got.split("\n")));
        }
    }
}
