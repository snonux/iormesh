package iormesh.workload.scenarios;

import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.net.InetAddress;
import java.net.URI;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.EnumMap;
import java.util.List;
import java.util.Map;
import javax.net.ssl.SSLContext;

/**
 * One running microservices service: its life from boot to retirement,
 * in its own directory {@code <role>.d} inside the shared workdir.
 *
 * <pre>
 *   boot     (in the fresh dir D Tree.inFreshDir made) provision and
 *            load D/role.conf; pin the cert of
 *            every TLS service it calls (read D/../callee.crt)
 *   serve    (servers) publish role.crt if TLS, listen, answer the
 *            declared calls — the first one's handling makes this
 *            service's downstream calls, then saves its record — and
 *            withdraw the cert
 *   checkout (the gateway) make the downstream calls, save the record
 *   retire   unlink the record and the config, rmdir D
 * </pre>
 *
 * Every file op sits in a named method of its own (or DataStore's,
 * ServiceConfig's), never in a lambda: the causing method expect.json
 * declares is the innermost workload frame, and a lambda's frame would
 * be a synthetic {@code lambda$…} name that moves with every edit.
 * Each certificate is made in memory by its service ({@link
 * SelfSignedCert}); only the public half is written, and its callers
 * trust exactly that one.
 */
final class ServiceNode {
    /** Told the bound port once a server listens. */
    @FunctionalInterface
    interface Listening {
        void at(int port) throws Exception;
    }

    // A literal address, never a lookup: no resolver I/O in the bracket.
    private static final InetAddress LOOPBACK = loopback();

    private final Service svc;
    private final Workdir wd;
    /** The listening port of every service this one calls. */
    private final Map<Service, Integer> ports;
    // Set by boot, before the server starts: HttpServer.start begins the
    // dispatcher thread after, so the handler sees both (happens-before).
    private Path dir;
    private Map<Service, SSLContext> trust = Map.of();

    ServiceNode(Service svc, Workdir wd, Map<Service, Integer> ports) {
        this.svc = svc;
        this.wd = wd;
        this.ports = ports;
    }

    /**
     * Runs a server service: boot, serve its declared calls, retire.
     * waitSeconds bounds the wait for the calls (self-test); 0 waits as
     * long as it takes, like the other standalone servers — the harness
     * kills a stuck one.
     */
    void serve(int port, Listening listening, long waitSeconds) throws Exception {
        Tree.inFreshDir(wd.file(svc.dirName()), d -> {
            boot(d);
            listen(port, listening, waitSeconds);
            retire();
        });
    }

    /** Runs the gateway: boot, the checkout's calls, its record, retire. */
    void checkout() throws Exception {
        Tree.inFreshDir(wd.file(svc.dirName()), d -> {
            boot(d);
            callDownstream();
            DataStore.save(dir, svc.dataFile, svc.record());
            retire();
        });
    }

    private void boot(Path d) throws Exception {
        dir = d;
        Path conf = d.resolve(svc.configFile());
        ServiceConfig.provision(conf, svc.config());
        ServiceConfig.load(conf, svc.config());
        Map<Service, SSLContext> pinned = new EnumMap<>(Service.class);
        for (Service to : svc.downstream()) {
            if (to.tls) {
                pinned.put(to, SelfSignedCert.clientContext(readCert(to)));
            }
        }
        trust = pinned;
    }

    /**
     * Publishes the certificate (TLS only), serves, and withdraws it
     * again — also after a failure, so a reused --dir never hands a stale
     * certificate to the next run's callers.
     */
    private void listen(int port, Listening listening, long waitSeconds) throws Exception {
        SelfSignedCert cert = svc.tls ? SelfSignedCert.generate() : null;
        try {
            SSLContext ctx = cert == null ? null : publish(cert);
            try (HttpServe serve = HttpServe.startWorking(LOOPBACK, port, svc.serves, ctx,
                    this::handle)) {
                listening.at(serve.port());
                if (waitSeconds == 0) {
                    serve.awaitAll();
                } else {
                    serve.awaitAll(waitSeconds);
                }
            }
        } finally {
            if (cert != null) {
                withdraw();
            }
        }
    }

    /**
     * Request n's work, on the server's dispatcher thread: the first call
     * a service serves makes its downstream calls and saves its record;
     * inventory's second (the gateway's stock query) only answers.
     */
    private void handle(int n) throws Exception {
        if (n != 0) {
            return;
        }
        callDownstream();
        DataStore.save(dir, svc.dataFile, svc.record());
    }

    /**
     * Makes this service's hops in order, each on its own HttpClient and
     * so its own keep-alive connection, closed before the next hop: the
     * http events' req_seq is 1 on every connection.
     */
    private void callDownstream() throws Exception {
        for (Service.Hop h : svc.hops()) {
            Service to = h.to();
            URI base = URI.create((to.tls ? "https" : "http") + "://127.0.0.1:"
                    + ports.get(to) + "/");
            HttpFetch.run(base, List.of(h.call()), trust.get(to), false);
        }
    }

    /**
     * Writes the public certificate where callers pin it from, before the
     * server listens, so a caller released by the listening line always
     * finds it. java.io streams, like http-tls: few JDK frames between the
     * syscall and this method.
     */
    private SSLContext publish(SelfSignedCert cert) throws Exception {
        try (FileOutputStream out = new FileOutputStream(wd.file(svc.certFile()).toFile())) {
            out.write(cert.der());
        }
        return cert.serverContext();
    }

    private void withdraw() throws IOException {
        Files.deleteIfExists(wd.file(svc.certFile()));
    }

    /** The certificate a TLS callee published; missing is a failed run. */
    private byte[] readCert(Service to) throws IOException {
        Path crt = wd.file(to.certFile());
        if (!Files.isRegularFile(crt)) {
            // Never fall back to trusting anything else.
            throw new IOException("no " + to.role + " certificate at " + crt);
        }
        try (FileInputStream in = new FileInputStream(crt.toFile())) {
            return in.readAllBytes();
        }
    }

    /**
     * Removes what the service made, as declared syscalls, so
     * Tree.inFreshDir's cleanup finds nothing left on a clean run.
     */
    private void retire() throws IOException {
        Files.delete(dir.resolve(svc.dataFile));
        Files.delete(dir.resolve(svc.configFile()));
        Files.delete(dir);
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
