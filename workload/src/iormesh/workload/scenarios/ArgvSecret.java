package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.io.InputStream;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Set;

/**
 * M2's argv redaction (decision #9; docs/implementation.md M2 step 8):
 * this process starts a child JVM whose command line carries a planted
 * secret in every shape the redaction policy names — a system property
 * (-Dpassword=…), an option with its value in the next argument
 * (--token …), an option=value (--api-key=…) and a URL with userinfo and
 * a query value. The tracer records the child's exec with its argv; the
 * golden lists the secret under must_not_appear, so it fails if any of
 * them reaches events.ndjson or graph.json unmasked. Solo, no options.
 * Syscalls (the declared subset):
 *
 * <pre>
 *   parent: spawn ArgvChild (stdout a pipe); read its 1-byte ready
 *           message; close the pipe; reap the child
 *   child:  exec (jspawnhelper, then java — the exec events carry the
 *           argv); write(1, 1 byte)
 * </pre>
 *
 * The child's argv is all this scenario is about; the child itself
 * ignores its arguments. The environment is never captured (decision
 * #9), so no secret is planted there.
 */
public final class ArgvSecret implements Scenario {
    static final String NAME = "argv-secret";
    /** The planted secret; expect.json's must_not_appear names it. */
    static final String SECRET = "Hunter2PlantedArgvSecret";

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of());
        Process p = new ProcessBuilder(command())
                .redirectError(ProcessBuilder.Redirect.INHERIT)
                .start();
        try {
            p.getOutputStream().close();
            try (InputStream in = p.getInputStream()) {
                int b = in.read();
                if (b != ArgvChild.READY) {
                    throw new IOException("argv-secret child not ready: " + b);
                }
            }
            int code = ChildJvm.await(p, NAME);
            if (code != 0) {
                throw new IOException("argv-secret child exited " + code);
            }
        } finally {
            // On failure, no child may outlive the scenario; on success it
            // is reaped already and this adds no syscall.
            ChildJvm.kill(p);
        }
    }

    /**
     * ChildJvm's command line with the secret planted: the system property
     * goes before the main class (a JVM option), the rest after it.
     */
    static List<String> command() {
        List<String> cmd = new ArrayList<>(ChildJvm.command(ArgvChild.class,
                "--token", SECRET,
                "--api-key=" + SECRET,
                "https://bob:" + SECRET + "@example.invalid/x?sig=" + SECRET));
        cmd.add(1, "-Dpassword=" + SECRET);
        return cmd;
    }
}
