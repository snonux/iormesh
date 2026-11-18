package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.nio.channels.Pipe;
import java.util.Arrays;
import java.util.Map;
import java.util.Set;

/**
 * M2's pipes, both ends: an in-process pipe (java.nio.channels.Pipe)
 * and the stdio pipes of a child JVM (ProcessBuilder's default
 * redirects) — the two ways plain Java gets a pipe. Solo, no options.
 * Syscalls (strace, JDK 25, 26 and 27-ea; see expect.json for the
 * declared subset):
 *
 * <pre>
 *   in-process: pipe2([r, w], 0); write(w, 64); read(r, 64);
 *               close(r); close(w)
 *   child:      four pipe2 calls — the stdin and stdout pipes first,
 *               then two the spawn uses to talk to jspawnhelper; the
 *               child's side dup2s the pipe ends onto its 0 and 1 and
 *               execs PipeChild's JVM; the parent writes 64 bytes on
 *               the stdin pipe; the child reads them on fd 0 and writes
 *               them back on fd 1; the parent reads 64, closes its
 *               stdout end, then its stdin end; the child reads EOF on
 *               fd 0 and exits
 * </pre>
 *
 * JDK-dependent details the golden steps around: JDK 27 creates the
 * spawn pipes with O_CLOEXEC (JDK 25 and 26: flags 0) and issues the
 * dup2s before exec'ing jspawnhelper rather than inside it, plus extra
 * dup2s of its own; Pipe.open() also makes a socketpair the first time
 * NIO is used (the JDK's pre-close fd), inside the bracket.
 *
 * The parent closes its read end before the child exits on purpose:
 * on exit the JDK's process reaper thread drains whatever is left in
 * that pipe on its own thread, so an open read end would make the
 * thread (and number) of the parent's reads depend on timing. The
 * child brackets nothing itself; like fd-lifecycle's child it belongs
 * to the scenario because it was forked inside the bracket.
 *
 * Which pipe2 call makes which pipe is JDK-internal, and a pipe2 event
 * has two fds where the golden format binds one, so the golden binds
 * the parent's pipe fds by their read and write, not by pipe2.
 */
public final class PipeIpc implements Scenario {
    static final String NAME = "pipe-ipc";
    static final int SIZE = 64;

    @Override
    public String name() {
        return NAME;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(NAME, role, Set.of("solo"));
        Options.requireKnown(opts, Set.of());
        inProcess();
        child();
    }

    /**
     * One pipe, both ends on this thread: 64 bytes fit the pipe buffer,
     * so the write never blocks waiting for the read.
     */
    private static void inProcess() throws IOException {
        byte[] data = payload('P');
        Pipe pipe = Pipe.open();
        // Closed in reverse order: the read end first, then the write end.
        try (Pipe.SinkChannel sink = pipe.sink(); Pipe.SourceChannel source = pipe.source()) {
            Wire.send(sink, data);
            Wire.expect(source, data);
        }
    }

    /** Round-trips 64 bytes through PipeChild's stdin and stdout. */
    private static void child() throws Exception {
        byte[] data = payload('C');
        Process p = new ProcessBuilder(ChildJvm.command(PipeChild.class))
                // The child's own complaints stay visible on our stderr.
                .redirectError(ProcessBuilder.Redirect.INHERIT)
                .start();
        OutputStream toChild = p.getOutputStream();
        InputStream fromChild = p.getInputStream();
        try {
            toChild.write(data);
            toChild.flush();
            byte[] got = fromChild.readNBytes(SIZE);
            if (!Arrays.equals(got, data)) {
                throw new IOException("child echoed " + got.length + " bytes that differ");
            }
        } finally {
            // Read end first: see the class comment. The stdin close is
            // the child's EOF, its cue to exit.
            fromChild.close();
            toChild.close();
        }
        int code = p.waitFor();
        if (code != 0) {
            throw new IOException("pipe-ipc child exited " + code);
        }
    }

    static byte[] payload(char c) {
        byte[] b = new byte[SIZE];
        Arrays.fill(b, (byte) c);
        return b;
    }
}
