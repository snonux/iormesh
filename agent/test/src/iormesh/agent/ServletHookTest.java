package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.http.Servlet;
import iormesh.agent.http.Target;
import iormesh.bridge.Hooks;
import jakarta.servlet.DispatcherType;
import jakarta.servlet.http.HttpServlet;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import java.io.InputStream;
import java.lang.reflect.InvocationTargetException;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collection;
import java.util.Collections;
import java.util.Enumeration;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.function.BiFunction;

/**
 * The Servlet hook (task de4) end to end on stand-ins of the Servlet API
 * (agent/test/src/jakarta/servlet): the agent's own targets rewrite
 * HttpServlet.service, and the probe makes one request of the original
 * dispatch — redacted, with its addresses, sizes and own start — and
 * nothing of a forward; an async request goes without status, unordered;
 * a throw before the reply is committed leaves the status unknown.
 */
final class ServletHookTest {
    private ServletHookTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("servlet/request recorded", ServletHookTest::recorded),
                new AgentTests.Case("servlet/forward, async, throw", ServletHookTest::special),
                new AgentTests.Case("servlet/nested service records once",
                        ServletHookTest::nested),
                new AgentTests.Case("servlet/javax, Servlet 2.5 shape", ServletHookTest::javax));
    }

    /** One probe result: the record's strings and meta (status, flags, start, sizes). */
    record Got(List<String> s, long status, long flags, boolean started, long req, long resp) {}

    /** A request the probe sees; headers by name, one value each. */
    static final class Req implements HttpServletRequest, javax.servlet.http.HttpServletRequest {
        String method = "GET";
        String uri = "/orders/7";
        String query = "token=s3cret";
        DispatcherType type = DispatcherType.REQUEST;
        boolean async;
        Map<String, String> headers = new TreeMap<>(Map.of("Authorization", "Bearer x",
                "Accept", "*/*"));

        public String getMethod() { return method; }
        public String getRequestURI() { return uri; }
        public String getQueryString() { return query; }
        public Enumeration<String> getHeaderNames() { return Collections.enumeration(headers.keySet()); }
        public Enumeration<String> getHeaders(String n) {
            return Collections.enumeration(List.of(headers.get(n)));
        }
        public String getScheme() { return "http"; }
        public String getProtocol() { return "HTTP/1.1"; }
        public String getLocalAddr() { return "127.0.0.1"; }
        public int getLocalPort() { return 8080; }
        public String getRemoteAddr() { return "0:0:0:0:0:0:0:1"; }
        public int getRemotePort() { return 40000; }
        public long getContentLengthLong() { return 0; }
        public DispatcherType getDispatcherType() { return type; }
        public boolean isAsyncStarted() { return async; }
    }

    /** A response with a status, a Content-Length kept outside its header map, as Tomcat does. */
    static final class Resp implements HttpServletResponse, javax.servlet.http.HttpServletResponse {
        int status = 200;
        boolean committed;

        public int getStatus() { return status; }
        public Collection<String> getHeaderNames() { return List.of("Set-Cookie"); }
        public Collection<String> getHeaders(String n) { return List.of("sid=1"); }
        public String getHeader(String n) { return n.equals("Content-Length") ? "5" : null; }
        public boolean isCommitted() { return committed; }
    }

    /** The rewritten HttpServlet, its probe calls collected into got. */
    private static Class<?> rewritten(List<Got> got) throws Exception {
        return rewritten(got, HttpServlet.class);
    }

    /** The stand-in servlet class (jakarta or javax) rewritten by the agent's own targets. */
    static Class<?> rewritten(List<Got> got, Class<?> servlet) throws Exception {
        List<Target> targets = new ArrayList<>();
        for (int i = 0; i < 200; i++) {
            targets.add(new Target("x/Pad", "m" + i, "()V", Target.NO_ARG, null));
        }
        targets.addAll(new Servlet().targets());
        HttpHooks hooks = HttpHooks.of(targets, true);
        for (HttpHooks.Hook h : hooks.hooks()) {
            BiFunction<Object[], long[], String[]> p = h.target().probe();
            if (p != null) {
                Hooks.httpProbe(h.id(), (a, meta) -> {
                    String[] s = p.apply(a, meta);
                    if (s != null) {
                        got.add(new Got(Arrays.asList(s), meta[0], meta[2], meta[4] > 0,
                                meta[5], meta[6]));
                    }
                    return null;
                });
            }
        }
        byte[] bytes;
        try (InputStream in = servlet.getResourceAsStream("HttpServlet.class")) {
            bytes = in.readAllBytes();
        }
        HttpTransformer t = new HttpTransformer(hooks, new BridgeSight(Hooks.class, null));
        byte[] after = t.transform(null, ServletHookTest.class.getClassLoader(),
                servlet.getName().replace('.', '/'), null,
                null, bytes);
        check(after != null && t.applied().size() == 1, servlet.getName() + " service rewritten");
        return new BoundaryTransformerTest.OneClassLoader(servlet.getName(), after)
                .loadClass(servlet.getName());
    }

    /** Runs service on a new servlet doing body; true if it threw. */
    private static boolean serve(Class<?> c, HttpServlet.Body body, Req req, Resp resp)
            throws Exception {
        Object servlet = c.getConstructor(HttpServlet.Body.class).newInstance(body);
        try {
            c.getMethod("service", jakarta.servlet.ServletRequest.class,
                    jakarta.servlet.ServletResponse.class).invoke(servlet, req, resp);
            return false;
        } catch (InvocationTargetException e) {
            return true;
        }
    }

    static void recorded() throws Exception {
        List<Got> got = new ArrayList<>();
        Path dir = Files.createTempDirectory("iormesh-servlet-test");
        Hooks.openRing(dir.toString(), 1 << 16);
        try {
            Class<?> c = rewritten(got);
            check(!serve(c, (q, s) -> { }, new Req(), new Resp()), "served");
            equal(1, got.size(), "one request");
            Got g = got.get(0);
            equal(List.of("GET", "http", "", "/orders/7?token=***", "HTTP/1.1", "127.0.0.1:8080",
                    "[0:0:0:0:0:0:0:1]:40000", "accept: */*", "authorization: ***",
                    "set-cookie: ***"), g.s(), "strings, redacted");
            equal(new Got(g.s(), 200, 1, true, 0, 5), g, "status, server, own start, sizes");
        } finally {
            Hooks.release();
            Files.deleteIfExists(dir);
        }
    }

    static void special() throws Exception {
        List<Got> got = new ArrayList<>();
        Path dir = Files.createTempDirectory("iormesh-servlet-test");
        Hooks.openRing(dir.toString(), 1 << 16);
        try {
            Class<?> c = rewritten(got);
            Req forward = new Req();
            forward.type = DispatcherType.FORWARD;
            serve(c, (q, s) -> { }, forward, new Resp());
            equal(0, got.size(), "a forward is no request");
            Req async = new Req();
            async.async = true;
            serve(c, (q, s) -> { }, async, new Resp());
            equal(List.of(0L, 3L, false, -1L), fields(got.remove(0)), "async: no status, unordered");
            check(serve(c, (q, s) -> { throw new IllegalStateException("x"); }, new Req(),
                    new Resp()), "threw");
            equal(List.of(0L, 1L, true, -1L), fields(got.remove(0)), "thrown before the reply");
            Resp sent = new Resp();
            sent.committed = true;
            serve(c, (q, s) -> { throw new IllegalStateException("x"); }, new Req(), sent);
            equal(List.of(200L, 1L, true, 5L), fields(got.remove(0)), "thrown after the reply");
            check(got.isEmpty(), "nothing else");
        } finally {
            Hooks.release();
            Files.deleteIfExists(dir);
        }
    }

    private static List<Object> fields(Got g) {
        return List.of(g.status(), g.flags(), g.started(), g.resp());
    }

    /**
     * A service reached from inside another's on the same request (a JSP
     * under JspServlet, a wrapped servlet, a Servlet 2.5 forward): only
     * the outermost records, with its own start (review of de4).
     */
    static void nested() throws Exception {
        List<Got> got = new ArrayList<>();
        Path dir = Files.createTempDirectory("iormesh-servlet-test");
        Hooks.openRing(dir.toString(), 1 << 16);
        try {
            Class<?> c = rewritten(got);
            Object inner = c.getConstructor(HttpServlet.Body.class).newInstance(
                    (HttpServlet.Body) (q, s) -> { });
            java.lang.reflect.Method service = c.getMethod("service",
                    jakarta.servlet.ServletRequest.class, jakarta.servlet.ServletResponse.class);
            check(!serve(c, (q, s) -> service.invoke(inner, q, s), new Req(), new Resp()),
                    "served");
            equal(1, got.size(), "one request, not two");
            check(got.get(0).started(), "the outer's own start");
        } finally {
            Hooks.release();
            Files.deleteIfExists(dir);
        }
    }

    /**
     * The javax target (task de4 review) on a Servlet 2.5-shaped stand-in:
     * no dispatcher type, so the request counts as the original; a
     * forward, nested, records nothing more.
     */
    static void javax() throws Exception {
        List<Got> got = new ArrayList<>();
        Path dir = Files.createTempDirectory("iormesh-servlet-test");
        Hooks.openRing(dir.toString(), 1 << 16);
        try {
            Class<?> c = rewritten(got, javax.servlet.http.HttpServlet.class);
            Object inner = c.getConstructor(javax.servlet.http.HttpServlet.Body.class)
                    .newInstance((javax.servlet.http.HttpServlet.Body) (q, s) -> { });
            java.lang.reflect.Method service = c.getMethod("service",
                    javax.servlet.ServletRequest.class, javax.servlet.ServletResponse.class);
            Object outer = c.getConstructor(javax.servlet.http.HttpServlet.Body.class)
                    .newInstance((javax.servlet.http.HttpServlet.Body)
                            (q, s) -> service.invoke(inner, q, s));
            service.invoke(outer, new Req(), new Resp());
            equal(1, got.size(), "one request");
            equal("/orders/7?token=***", got.get(0).s().get(3), "redacted path");
            equal(200L, got.get(0).status(), "status");
        } finally {
            Hooks.release();
            Files.deleteIfExists(dir);
        }
    }
}
