package iormesh.workload.libs;

import iormesh.workload.scenarios.HttpCall;
import jakarta.servlet.http.HttpServlet;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.io.IOException;
import java.io.OutputStream;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.Comparator;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.Stream;
import java.util.logging.Level;
import java.util.logging.Logger;
import org.apache.catalina.LifecycleException;
import org.apache.catalina.connector.Connector;
import org.apache.catalina.core.StandardContext;
import org.apache.catalina.startup.Tomcat;

/**
 * The server end of http-servlet: an embedded Tomcat (vendored, workload/
 * libs/third_party/tomcat) bound to 127.0.0.1 with one servlet that
 * answers exactly the declared calls, in order, like the core scenarios'
 * HttpServe. One request thread (maxThreads 1), so every request has one
 * thread; Tomcat's base directory (it writes its work directories there)
 * is a fresh temp directory removed on close.
 *
 * The servlet flushes its reply before it returns, so the reply's write
 * happens inside service, before the agent's http event at service's
 * exit — the order http-plain's server shows — and the latch, counted
 * down after the flush, never lets the owner stop Tomcat with a reply
 * unsent.
 */
final class TomcatServe implements AutoCloseable {
    /**
     * Tomcat logs its life cycle at INFO on stderr; warnings are enough.
     * Held here: java.util.logging keeps loggers weakly, and a collected
     * one would forget its level.
     */
    private static final Logger TOMCAT_LOG = quiet();

    private final Tomcat tomcat;
    private final Connector connector;
    private final Path base;
    private final Answer answer;

    private TomcatServe(Tomcat tomcat, Connector connector, Path base, Answer answer) {
        this.tomcat = tomcat;
        this.connector = connector;
        this.base = base;
        this.answer = answer;
    }

    /** Binds 127.0.0.1:port (0 = kernel-picked) and starts serving calls. */
    static TomcatServe start(int port, List<HttpCall> calls) throws Exception {
        Path base = Files.createTempDirectory("iormesh-tomcat-");
        Tomcat tomcat = new Tomcat();
        tomcat.setBaseDir(base.toString());
        Connector c = new Connector("HTTP/1.1");
        c.setPort(port);
        c.setProperty("address", "127.0.0.1");
        c.setProperty("maxThreads", "1");
        c.setProperty("minSpareThreads", "1");
        tomcat.setConnector(c);
        StandardContext ctx = (StandardContext) tomcat.addContext("", null);
        // Its leak checks at stop reflect into java.base and warn without
        // --add-opens; one web app of our own leaks nothing to look for.
        ctx.setClearReferencesThreadLocals(false);
        ctx.setClearReferencesRmiTargets(false);
        Answer answer = new Answer(calls);
        Tomcat.addServlet(ctx, "calls", answer);
        ctx.addServletMapping("/*", "calls");
        TomcatServe serve = new TomcatServe(tomcat, c, base, answer);
        try {
            tomcat.start();
        } catch (LifecycleException | RuntimeException e) {
            serve.close();
            throw e;
        }
        return serve;
    }

    private static Logger quiet() {
        Logger log = Logger.getLogger("org.apache");
        log.setLevel(Level.WARNING);
        return log;
    }

    int port() {
        return connector.getLocalPort();
    }

    /** Waits, without a bound, until every declared call was answered. */
    void awaitAll() throws Exception {
        answer.done.await();
        answer.check();
    }

    /** awaitAll with a bound, for --self-test where both ends share a JVM. */
    void awaitAll(long seconds) throws Exception {
        if (!answer.done.await(seconds, TimeUnit.SECONDS)) {
            throw new IOException("server answered " + answer.next.get() + " of "
                    + answer.calls.size() + " requests");
        }
        answer.check();
    }

    /** Stops Tomcat and removes its base directory. */
    @Override
    public void close() throws IOException, LifecycleException {
        try {
            tomcat.stop();
            tomcat.destroy();
        } finally {
            try (Stream<Path> files = Files.walk(base)) {
                for (Path p : files.sorted(Comparator.reverseOrder()).toList()) {
                    Files.deleteIfExists(p);
                }
            }
        }
    }

    /**
     * The servlet: request n gets call n. A request the declaration does
     * not expect gets 500 and fails the run; the latch is then released in
     * full, so a standalone server never waits for requests that will not
     * come.
     */
    static final class Answer extends HttpServlet {
        private static final long serialVersionUID = 1L;

        final transient List<HttpCall> calls;
        final transient AtomicInteger next = new AtomicInteger();
        final transient CountDownLatch done;
        private transient volatile String failure;

        Answer(List<HttpCall> calls) {
            this.calls = calls;
            this.done = new CountDownLatch(calls.size());
        }

        @Override
        protected void service(HttpServletRequest req, HttpServletResponse resp)
                throws IOException {
            int n = next.getAndIncrement();
            try {
                String body = new String(req.getInputStream().readAllBytes(), StandardCharsets.UTF_8);
                HttpCall call = n < calls.size() ? calls.get(n) : null;
                String problem = call == null ? "unexpected request " + (n + 1)
                        : mismatch(call, req, body);
                if (problem != null) {
                    failure = problem;
                    resp.setStatus(500);
                    resp.setContentLength(0);
                } else {
                    reply(resp, call);
                }
                resp.flushBuffer();
            } finally {
                done.countDown();
                while (failure != null && done.getCount() > 0) {
                    done.countDown();
                }
            }
        }

        void check() throws IOException {
            if (failure != null) {
                throw new IOException("server: " + failure);
            }
        }

        /** What the request got wrong, or null; never prints a header's value. */
        private static String mismatch(HttpCall c, HttpServletRequest req, String body) {
            String q = req.getQueryString();
            String target = req.getRequestURI() + (q == null ? "" : "?" + q);
            if (!c.method().equals(req.getMethod()) || !c.target().equals(target)) {
                return "want " + c.method() + " " + c.where() + ", got " + req.getMethod() + " "
                        + req.getRequestURI() + " (or another query)";
            }
            if (!c.body().equals(body)) {
                return c.where() + ": body differs";
            }
            for (Map.Entry<String, String> h : c.headers().entrySet()) {
                if (!h.getValue().equals(req.getHeader(h.getKey()))) {
                    return c.where() + ": header " + h.getKey() + " missing or different";
                }
            }
            return null;
        }

        private static void reply(HttpServletResponse resp, HttpCall call) throws IOException {
            resp.setStatus(call.status());
            for (Map.Entry<String, String> h : call.replyHeaders().entrySet()) {
                resp.addHeader(h.getKey(), h.getValue());
            }
            byte[] reply = call.reply().getBytes(StandardCharsets.UTF_8);
            resp.setContentLength(reply.length);
            try (OutputStream out = resp.getOutputStream()) {
                out.write(reply);
            }
        }
    }
}
