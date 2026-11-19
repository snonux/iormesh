package iormesh.agent.http;

import java.lang.invoke.MethodHandle;
import java.util.Collection;
import java.util.Enumeration;
import java.util.List;
import java.util.function.BiFunction;

/**
 * The Servlet API (decision #22's v1 set, task de4), both namespaces —
 * jakarta.servlet (Servlet 5+: Tomcat 10+, Jetty 11+) and javax.servlet
 * (Tomcat 9 and older, Spring Boot 2): one http event per request a
 * servlet container hands to an HttpServlet, whatever the container.
 *
 * The hook is HttpServlet.service(ServletRequest, ServletResponse), the
 * public entry every container calls through its filter chain and which
 * dispatches to doGet/doPost/… — so plain servlets and frameworks built
 * on HttpServlet (Spring's DispatcherServlet) are seen. It is entry
 * stamped: the latency runs from the container's dispatch into the
 * servlet to its return (the request line and headers were read before;
 * a reply the container buffered is written after — the event then
 * precedes its write). Not seen: a servlet that overrides this public
 * service without calling it (Jersey's ServletContainer), and a filter
 * that answers without reaching any servlet.
 *
 * The probe reads the request and response through their public API,
 * reflectively in the loader that defines them (Handles; per class, so
 * several web apps each with their own copy are fine). It never calls
 * anything that would change the request: no getParameter (it would read
 * a form body), no getRemoteHost (a reverse lookup). Only the original
 * dispatch records — a forward, include, error page or async dispatch of
 * the same request is not another request — and of it only the
 * outermost service on the thread (review of de4): a JSP's service runs
 * inside Tomcat's JspServlet's, Spring's ServletWrappingController calls
 * a servlet's from inside DispatcherServlet's, and a Servlet 2.5 forward
 * has no dispatcher type to tell it by. A request still in
 * asynchronous processing when service returns is recorded then, without
 * a status or reply size (not decided yet) and unordered (the reply
 * comes later, out of order with the connection's next requests). A
 * service that throws records the status only once the reply is
 * committed; otherwise the container decides it (an error page), so it
 * is left unknown.
 */
public final class Servlet implements Library {
    @Override
    public String name() {
        return "servlet";
    }

    @Override
    public String module() {
        return null;
    }

    @Override
    public List<String> opens() {
        return List.of();
    }

    @Override
    public boolean appLoaded() {
        return true;
    }

    @Override
    public List<Target> targets() {
        return List.of(target("jakarta.servlet"), target("javax.servlet"));
    }

    private static Target target(String pkg) {
        String p = pkg.replace('.', '/');
        return new Target(p + "/http/HttpServlet", "service",
                "(L" + p + "/ServletRequest;L" + p + "/ServletResponse;)V", 0, new Probe(pkg))
                .withSecond(1).stamped();
    }

    /** service's exit: a[2] is {request, response}. */
    static final class Probe implements BiFunction<Object[], long[], String[]> {
        private final ClassValue<In> in;
        private final ClassValue<Out> out;

        Probe(String pkg) {
            in = new ClassValue<>() {
                @Override
                protected In computeValue(Class<?> c) {
                    return In.of(c, pkg);
                }
            };
            out = new ClassValue<>() {
                @Override
                protected Out computeValue(Class<?> c) {
                    return Out.of(c, pkg);
                }
            };
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            if (!(a[2] instanceof Object[] pair) || pair.length != 2 || pair[0] == null
                    || pair[1] == null) {
                return null;
            }
            try {
                return describe(a, meta, pair[0], pair[1]);
            } catch (RuntimeException | Error e) {
                throw e;
            } catch (Throwable t) {
                throw new IllegalStateException(t);   // counted as a lost record by Hooks
            }
        }

        private String[] describe(Object[] a, long[] meta, Object req, Object resp)
                throws Throwable {
            In q = in.get(req.getClass());
            Out s = out.get(resp.getClass());
            boolean nested = a.length > 5 && Boolean.TRUE.equals(a[5]);
            if (nested || q == null || s == null || !q.original(req)) {
                return null;   // inside another service, not HTTP, or a nested dispatch
            }
            Request r = q.describe(req);
            boolean async = q.async(req);
            boolean threw = a[0] instanceof Throwable;
            if (async || (threw && !s.committed(resp))) {
                r.status = 0;   // not decided yet: unknown, never a guess
                r.responseHeaders(s.headers(resp));
            } else {
                s.describe(resp, r);
            }
            r.unordered = async;
            r.start = Request.ownStart(a, meta);
            return r.toRecord(meta);
        }
    }

    /** HttpServletRequest's getters, found in one request class. */
    record In(MethodHandle method, MethodHandle scheme, MethodHandle uri, MethodHandle query,
              MethodHandle protocol, MethodHandle localAddr, MethodHandle localPort,
              MethodHandle remoteAddr, MethodHandle remotePort, MethodHandle names,
              MethodHandle values, MethodHandle length, MethodHandle dispatch,
              MethodHandle async) {
        /** The getters, or null when c is no HttpServletRequest of pkg. */
        static In of(Class<?> c, String pkg) {
            Class<?> api = Handles.find(c, pkg + ".http.HttpServletRequest");
            if (api == null) {
                return null;
            }
            Class<?> dt = Handles.sibling(api, pkg + ".DispatcherType");
            return new In(get(api, "getMethod", String.class), get(api, "getScheme", String.class),
                    get(api, "getRequestURI", String.class), get(api, "getQueryString", String.class),
                    get(api, "getProtocol", String.class), get(api, "getLocalAddr", String.class),
                    get(api, "getLocalPort", int.class), get(api, "getRemoteAddr", String.class),
                    get(api, "getRemotePort", int.class),
                    get(api, "getHeaderNames", Enumeration.class),
                    Handles.virtual(api, "getHeaders", Enumeration.class, String.class),
                    get(api, "getContentLengthLong", long.class),
                    get(api, "getDispatcherType", dt), get(api, "isAsyncStarted", boolean.class));
        }

        private static MethodHandle get(Class<?> api, String name, Class<?> ret) {
            return Handles.virtual(api, name, ret);
        }

        /**
         * Not a forward, include, error or async dispatch (REQUEST). A
         * Servlet 2.5 API has no dispatcher type: its forwards and
         * includes are nested and skipped as such, an error page's
         * dispatch (after service returned) would record twice.
         */
        boolean original(Object req) throws Throwable {
            Object t = Handles.call(dispatch, req);
            return t == null || (t instanceof Enum<?> e && e.name().equals("REQUEST"));
        }

        boolean async(Object req) throws Throwable {
            return Boolean.TRUE.equals(Handles.call(async, req));
        }

        Request describe(Object req) throws Throwable {
            Request r = new Request();
            r.server = true;
            r.method = nz(Handles.string(method, req));
            r.scheme = nz(Handles.string(scheme, req));
            String q = Handles.string(query, req);
            r.path = nz(Handles.string(uri, req)) + (q == null ? "" : "?" + q);
            r.version = nz(Handles.string(protocol, req));
            r.local = Request.literal(Handles.string(localAddr, req),
                    (int) Handles.number(localPort, req, 0));
            r.remote = Request.literal(Handles.string(remoteAddr, req),
                    (int) Handles.number(remotePort, req, 0));
            r.requestHeaders(Handles.headers(names, values, req));
            r.reqBytes = Handles.number(length, req, Request.UNKNOWN);
            return r;
        }

        private static String nz(String s) {
            return s == null ? "" : s;
        }
    }

    /** HttpServletResponse's getters, found in one response class. */
    record Out(MethodHandle status, MethodHandle names, MethodHandle values, MethodHandle header,
               MethodHandle committed) {
        static Out of(Class<?> c, String pkg) {
            Class<?> api = Handles.find(c, pkg + ".http.HttpServletResponse");
            if (api == null) {
                return null;
            }
            return new Out(Handles.virtual(api, "getStatus", int.class),
                    Handles.virtual(api, "getHeaderNames", Collection.class),
                    Handles.virtual(api, "getHeaders", Collection.class, String.class),
                    Handles.virtual(api, "getHeader", String.class, String.class),
                    Handles.virtual(api, "isCommitted", boolean.class));
        }

        boolean committed(Object resp) throws Throwable {
            return Boolean.TRUE.equals(Handles.call(committed, resp));
        }

        java.util.Map<String, List<String>> headers(Object resp) throws Throwable {
            return Handles.headers(names, values, resp);
        }

        /**
         * Status, headers and the reply body's size as declared: a
         * Content-Length the servlet set (containers keep it outside the
         * header map, so it is asked for by name), 0 by protocol, else
         * unknown — the container may still add one when it finishes the
         * buffered reply, after this exit.
         */
        void describe(Object resp, Request r) throws Throwable {
            r.status = (int) Handles.number(status, resp, 0);
            r.responseHeaders(headers(resp));
            Object cl = Handles.call(header, resp, "Content-Length");
            r.respBytes = Request.declaredBody(r.method, r.status, cl instanceof String v ? v : null);
        }
    }
}
