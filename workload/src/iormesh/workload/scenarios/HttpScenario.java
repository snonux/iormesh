package iormesh.workload.scenarios;

import iormesh.workload.Options;
import iormesh.workload.Scenario;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.net.InetAddress;
import java.net.URI;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;
import java.util.Map;
import java.util.Set;
import javax.net.ssl.SSLContext;

/**
 * M3's HTTP scenarios (PLAN.md M0 "HTTP scenarios"): a JDK HttpClient
 * sends declared requests to a JDK HttpServer on 127.0.0.1, one HTTP/1.1
 * keep-alive connection between two processes. Three variants share the
 * roles:
 *
 * <pre>
 *   http-plain      GET hello (200), POST echo (200), GET missing (404)
 *   http-tls        the same over TLS: a self-signed certificate made at
 *                   run time ({@link SelfSignedCert}), pinned by the client
 *   http-redaction  planted secrets in the Authorization, X-Api-Token and
 *                   Cookie headers, a query value and a Set-Cookie reply —
 *                   decision #9/#22's policy must keep every one of them
 *                   out of the trace (expect.json's must_not_appear)
 *
 *   --role server [--port N] [--dir D]   serve the calls, then stop
 *   --role client  --port N  [--dir D]   send the calls, then close
 * </pre>
 *
 * {@code --dir} is http-tls's only: the server writes the public
 * certificate to D/http-tls.crt before it prints {@code listening <port>}
 * and deletes it once served; the client reads it from the same D. The
 * private key never leaves the server's memory. Like tcp-loopback, the
 * client is started only after the listening line.
 */
public final class HttpScenario implements Scenario {
    static final String CERT_FILE = "http-tls.crt";

    // Planted secrets, each in its own place so a leak names its source.
    // expect.json lists them under must_not_appear; the server checks they
    // arrived, so the redaction is proven on a request that carried them.
    static final String AUTH_SECRET = "Hunter2PlantedAuthHeader";
    static final String API_SECRET = "Hunter2PlantedApiTokenHeader";
    static final String QUERY_SECRET = "Hunter2PlantedQuerySecret";
    static final String COOKIE_SECRET = "Hunter2PlantedSessionCookie";
    // The server's Set-Cookie carries a value of its own (not the one the
    // client sends back as Cookie), so a leak of either header is told
    // apart from the other.
    static final String SET_COOKIE_SECRET = "Hunter2PlantedSetCookie";

    // A literal address, never a lookup: no resolver I/O in the bracket.
    private static final InetAddress LOOPBACK = loopback();
    private static final long SELF_TEST_WAIT_SECONDS = 30;

    private final String name;
    private final boolean tls;
    private final List<HttpCall> calls;

    private HttpScenario(String name, boolean tls, List<HttpCall> calls) {
        this.name = name;
        this.tls = tls;
        this.calls = calls;
    }

    public static HttpScenario plain() {
        return new HttpScenario("http-plain", false, basicCalls("/plain"));
    }

    public static HttpScenario tls() {
        return new HttpScenario("http-tls", true, basicCalls("/tls"));
    }

    public static HttpScenario redaction() {
        return new HttpScenario("http-redaction", false, List.of(
                HttpCall.get("/redact/login?user=alice&token=" + QUERY_SECRET, 200, "welcome")
                        .withHeader("Authorization", "Bearer " + AUTH_SECRET)
                        .withHeader("X-Api-Token", API_SECRET)
                        .withReplyHeader("Set-Cookie", "sid=" + SET_COOKIE_SECRET),
                HttpCall.get("/redact/profile", 200, "alice")
                        .withHeader("Cookie", "sid=" + COOKIE_SECRET)));
    }

    /** Three calls, three statuses: what the http events must report. */
    private static List<HttpCall> basicCalls(String prefix) {
        return List.of(
                HttpCall.get(prefix + "/hello", 200, "hello"),
                HttpCall.post(prefix + "/echo", "ping!", 200, "ping!"),
                HttpCall.get(prefix + "/missing", 404, ""));
    }

    @Override
    public String name() {
        return name;
    }

    @Override
    public void run(String role, Map<String, String> opts) throws Exception {
        Options.requireRole(name, role, Set.of("client", "server"));
        Options.requireKnown(opts, tls ? Set.of("port", Workdir.OPTION) : Set.of("port"));
        // Parse the port before any I/O, so a bad option is a usage error
        // with an empty bracket.
        int port = role.equals("server") ? Options.port(opts, 0) : Options.requiredPort(opts);
        // Only http-tls has files; the others make no temp directory.
        try (Workdir wd = tls ? Workdir.open(opts) : null) {
            if (role.equals("server")) {
                server(wd, port);
            } else {
                client(wd, port);
            }
        }
    }

    /** Both roles in one process: the server's threads, this client. */
    @Override
    public void selfTest() throws Exception {
        try (Workdir wd = Workdir.temp()) {
            SelfSignedCert cert = tls ? SelfSignedCert.generate() : null;
            try (HttpServe serve = HttpServe.start(LOOPBACK, 0, calls, serverContext(wd, cert))) {
                client(wd, serve.port());
                serve.awaitAll(SELF_TEST_WAIT_SECONDS);
            } finally {
                Files.deleteIfExists(wd.file(CERT_FILE));
            }
        }
    }

    // No bound on the wait on purpose: a standalone server waits for its
    // client as long as it takes; the harness kills it on failure.
    private void server(Workdir wd, int port) throws Exception {
        SelfSignedCert cert = tls ? SelfSignedCert.generate() : null;
        try (HttpServe serve = HttpServe.start(LOOPBACK, port, calls, serverContext(wd, cert))) {
            System.out.println("listening " + serve.port());
            System.out.flush();
            serve.awaitAll();
        } finally {
            if (cert != null) {
                Files.deleteIfExists(wd.file(CERT_FILE));
            }
        }
    }

    /**
     * The server's TLS context, or null for plain HTTP. The certificate is
     * written before the server listens, so a client released by the
     * listening line always finds it.
     */
    private static SSLContext serverContext(Workdir wd, SelfSignedCert cert) throws Exception {
        if (cert == null) {
            return null;
        }
        // java.io streams, not Files.write/readAllBytes: fewer JDK frames
        // between the hooked native and this method, so the causing method
        // stays inside the agent's 10-frame stack (expect-pending.json).
        try (FileOutputStream out = new FileOutputStream(wd.file(CERT_FILE).toFile())) {
            out.write(cert.der());
        }
        return cert.serverContext();
    }

    private void client(Workdir wd, int port) throws Exception {
        SSLContext ctx = null;
        if (tls) {
            Path crt = wd.file(CERT_FILE);
            if (!Files.isRegularFile(crt)) {
                throw new IOException("no server certificate at " + crt);
            }
            byte[] der;
            try (FileInputStream in = new FileInputStream(crt.toFile())) {
                der = in.readAllBytes();
            }
            ctx = SelfSignedCert.clientContext(der);
        }
        URI base = URI.create((tls ? "https" : "http") + "://127.0.0.1:" + port + "/");
        HttpFetch.run(base, calls, ctx);
    }

    private static InetAddress loopback() {
        try {
            return InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        } catch (IOException e) {
            // getByAddress only throws for a wrong-length array.
            throw new IllegalStateException(e);
        }
    }
}
