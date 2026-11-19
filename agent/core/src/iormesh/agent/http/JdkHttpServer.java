package iormesh.agent.http;

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
                new Target("sun/net/httpserver/ServerImpl$Exchange", "run", "()V", Target.NO_ARG, p));
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
    static final class Probe implements BiFunction<Object[], int[], String[]> {
        private final Impl impl;
        private final Map<Class<?>, Field> exchange;
        private final Weak<Boolean> recorded = new Weak<>();

        Probe(Impl impl, Map<Class<?>, Field> exchange) {
            this.impl = impl;
            this.exchange = exchange;
        }

        @Override
        public String[] apply(Object[] a, int[] meta) {
            try {
                Field f = a[1] == null ? null : exchange.get(a[1].getClass());
                Object ex = f == null ? null : f.get(a[1]);
                if (ex == null || recorded.get(ex) != null) {
                    return null;   // rejected before an exchange, or done
                }
                recorded.put(ex, Boolean.TRUE);
                return impl.describe(ex).toRecord(meta);
            } catch (ReflectiveOperationException e) {
                return null;
            }
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

        Impl(Class<?> c) throws NoSuchMethodException {
            method = get(c, "getRequestMethod");
            uri = get(c, "getRequestURI");
            protocol = get(c, "getProtocol");
            status = get(c, "getResponseCode");
            local = get(c, "getLocalAddress");
            remote = get(c, "getRemoteAddress");
            reqHeaders = get(c, "getRequestHeaders");
            respHeaders = get(c, "getResponseHeaders");
            context = get(c, "getHttpContext");
        }

        private static Method get(Class<?> c, String name) throws NoSuchMethodException {
            Method m = c.getDeclaredMethod(name);
            m.setAccessible(true);
            return m;
        }

        @SuppressWarnings("unchecked")
        Request describe(Object ex) throws ReflectiveOperationException {
            Request r = new Request();
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
            r.responseHeaders((Map<String, List<String>>) respHeaders.invoke(ex));
            return r;
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
