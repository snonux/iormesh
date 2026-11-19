package iormesh.agent.http;

import com.sun.net.httpserver.Headers;
import com.sun.net.httpserver.HttpContext;
import com.sun.net.httpserver.HttpsServer;
import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.net.InetSocketAddress;
import java.net.URI;
import java.util.List;
import java.util.Map;
import java.util.function.BiFunction;

/**
 * The JDK's built-in HttpServer (jdk.httpserver), the microservices
 * workload's REST server: one http event per exchange, recorded once,
 * at whichever comes first of
 *
 * <ul>
 *   <li>the handler closing the exchange — HttpExchange.close(), after
 *       the reply is written, on the handler's thread, so the event's
 *       stack names the handler (docs/implementation.md M3 step 7);
 *   <li>the handler returning (ServerImpl$Exchange.run): a handler may
 *       end an exchange by closing the response body alone, which never
 *       calls close(); its event is stamped when the handler is done,
 *       and its stack is the JDK's dispatch, not the handler.
 * </ul>
 *
 * Both reach the JDK's ExchangeImpl (the public HttpExchange wraps it),
 * which is package-private: the probe calls its getters by reflection,
 * with sun.net.httpserver opened to the agent. The local address of a
 * connection the close shut ("Connection: close") cannot be read any
 * more, so it is left empty; the remote address stays, and the tracer
 * joins by it.
 *
 * Exchange.run is entry-stamped (task wd4): it starts when an executor
 * thread takes up the exchange the dispatcher found readable (the wait
 * in the executor's queue is not included), so the latency runs from
 * there to the close (or the return) — reading the request, the
 * handler, the reply. A
 * close on another thread (a handler that handed its exchange off) finds
 * no stamp of its exchange and records no start. Body sizes are the
 * exchange's own: the request's Content-Length as the client declared it
 * (unknown when chunked) and
 * the length the handler passed to sendResponseHeaders (unknown when it
 * streamed the reply chunked).
 */
public final class JdkHttpServer implements Library {
    private static final String PKG = "sun.net.httpserver";

    @Override
    public String name() {
        return "jdk-httpserver";
    }

    @Override
    public String module() {
        return "jdk.httpserver";
    }

    @Override
    public List<String> opens() {
        return List.of(PKG);
    }

    @Override
    public List<Target> targets() throws ReflectiveOperationException {
        Probe p = new Probe(new Impl(cls("ExchangeImpl")), Map.of(
                cls("HttpExchangeImpl"), field(cls("HttpExchangeImpl"), "impl"),
                cls("HttpsExchangeImpl"), field(cls("HttpsExchangeImpl"), "impl"),
                cls("ServerImpl$Exchange"), field(cls("ServerImpl$Exchange"), "tx")));
        return List.of(
                new Target("sun/net/httpserver/HttpExchangeImpl", "close", "()V", Target.NO_ARG, p),
                new Target("sun/net/httpserver/HttpsExchangeImpl", "close", "()V", Target.NO_ARG, p),
                new Target("sun/net/httpserver/ServerImpl$Exchange", "run", "()V", Target.NO_ARG, p)
                        .stamped());
    }

    private static Class<?> cls(String name) throws ClassNotFoundException {
        return Class.forName(PKG + "." + name, false, ClassLoader.getPlatformClassLoader());
    }

    private static Field field(Class<?> c, String name) throws NoSuchFieldException {
        Field f = c.getDeclaredField(name);
        f.setAccessible(true);
        return f;
    }

    /**
     * Records an exchange at its first sighting; the hooked object
     * (wrapper or dispatch task) leads to the ExchangeImpl, the key.
     */
    static final class Probe implements BiFunction<Object[], long[], String[]> {
        private final Impl impl;
        private final Map<Class<?>, Field> exchange;
        private final Weak<Boolean> recorded = new Weak<>();

        Probe(Impl impl, Map<Class<?>, Field> exchange) {
            this.impl = impl;
            this.exchange = exchange;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            try {
                Field f = a[1] == null ? null : exchange.get(a[1].getClass());
                Object ex = f == null ? null : f.get(a[1]);
                if (ex == null || recorded.get(ex) != null) {
                    return null;   // rejected before an exchange, or done
                }
                recorded.put(ex, Boolean.TRUE);
                Request r = impl.describe(ex);
                r.start = start(a[3], ex, meta);
                return r.toRecord(meta);
            } catch (ReflectiveOperationException e) {
                return null;
            }
        }

        /**
         * The open stamp, when the open hook is the dispatch of this very
         * exchange (its tx is ex); else 0 — another exchange's dispatch on
         * the same thread is not this request's start.
         */
        private long start(Object open, Object ex, long[] meta) throws IllegalAccessException {
            Field f = open == null ? null : exchange.get(open.getClass());
            return f != null && f.get(open) == ex ? meta[Request.M_START] : 0;
        }
    }

    /** ExchangeImpl's getters, made accessible once. */
    static final class Impl {
        private final Method method;
        private final Method uri;
        private final Method protocol;
        private final Method status;
        private final Method local;
        private final Method remote;
        private final Method reqHeaders;
        private final Method respHeaders;
        private final Method context;
        private final Field reqLen;
        private final Field rspLen;

        Impl(Class<?> c) throws NoSuchMethodException, NoSuchFieldException {
            method = get(c, "getRequestMethod");
            uri = get(c, "getRequestURI");
            protocol = get(c, "getProtocol");
            status = get(c, "getResponseCode");
            local = get(c, "getLocalAddress");
            remote = get(c, "getRemoteAddress");
            reqHeaders = get(c, "getRequestHeaders");
            respHeaders = get(c, "getResponseHeaders");
            context = get(c, "getHttpContext");
            reqLen = field(c, "reqContentLen");
            rspLen = field(c, "rspContentLen");
        }

        private static Method get(Class<?> c, String name) throws NoSuchMethodException {
            Method m = c.getDeclaredMethod(name);
            m.setAccessible(true);
            return m;
        }

        @SuppressWarnings("unchecked")
        Request describe(Object ex) throws ReflectiveOperationException {
            Request r = new Request();
            r.server = true;
            r.method = (String) method.invoke(ex);
            HttpContext ctx = (HttpContext) context.invoke(ex);
            r.scheme = ctx != null && ctx.getServer() instanceof HttpsServer ? "https" : "http";
            URI u = (URI) uri.invoke(ex);
            String q = u.getRawQuery();
            r.path = u.getRawPath() + (q == null ? "" : "?" + q);
            r.version = (String) protocol.invoke(ex);
            r.status = (int) status.invoke(ex);
            r.local = address(local, ex);
            r.remote = address(remote, ex);
            r.requestHeaders((Map<String, List<String>>) reqHeaders.invoke(ex));
            Headers out = (Headers) respHeaders.invoke(ex);
            r.responseHeaders(out);
            r.reqBytes = reqLen.getLong(ex);   // -1 for a chunked request: unknown
            r.respBytes = replyBytes(r, rspLen.getLong(ex), out);
            return r;
        }

        /**
         * The reply body's size: the length the handler declared, or what
         * the headers say when it declared none — 0 by protocol or by
         * Content-length, unknown for a chunked (or HTTP/1.0 read-to-close)
         * reply, whose length the exchange does not count. rspContentLen is
         * 0 both for "no body" and "chunked", hence the headers.
         */
        private static long replyBytes(Request r, long declared, Headers out) {
            if (r.status < 0) {
                return Request.UNKNOWN;   // no reply sent
            }
            if (declared > 0) {
                return declared;
            }
            if (out == null || out.containsKey("Transfer-encoding")) {
                return Request.UNKNOWN;
            }
            return Request.declaredBody(r.method, r.status, out.getFirst("Content-length"));
        }

        /**
         * An address getter's result, or "" once the close shut the
         * connection: the JDK then builds the local one from port -1,
         * which throws.
         */
        private static String address(Method m, Object ex) {
            try {
                return Request.addr((InetSocketAddress) m.invoke(ex));
            } catch (ReflectiveOperationException | RuntimeException e) {
                return "";
            }
        }
    }
}
