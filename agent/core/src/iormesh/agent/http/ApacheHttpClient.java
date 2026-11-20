package iormesh.agent.http;

import java.lang.invoke.MethodHandle;
import java.net.SocketAddress;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.function.BiFunction;

/**
 * Apache HttpClient 5, its classic (blocking) API (decision #22's v1
 * set, task de4): one http event per request a CloseableHttpClient
 * executes, recorded on the calling thread when the client hands the
 * response back (the body still unread).
 *
 * Two hooks, both on the caller's thread — the classic client runs every
 * exchange of a request there:
 * <ul>
 *   <li>{@code InternalHttpClient.doExecute} and {@code
 *       MinimalHttpClient.doExecute} (what every execute overload
 *       reaches), entry-stamped: the request's start, and at its return
 *       the event — method, scheme, host and path from the request,
 *       status, version and headers from the response;
 *   <li>httpcore5's {@code HttpRequestExecutor.execute}, one exchange on
 *       one connection, which records nothing: it counts the request's
 *       exchanges (a followed redirect or an answered 401/407 is another
 *       — the server saw each) and keeps the connection's socket ends
 *       for the event, in the per-thread slot the request's doExecute
 *       claims and clears at its exit (Slots).
 * </ul>
 *
 * A request that failed records nothing — unless an exchange had
 * happened, which the server then counted: the probe throws, so the
 * agent counts a lost record and the tracer numbers no more of this
 * JVM's requests (never a shifted ordinal). The library's API is read
 * reflectively in the loader that defines it (Handles), never changed:
 * the request's entity and the response's headers are only looked at.
 */
public final class ApacheHttpClient implements Library {
    static final String CLIENT = "org.apache.hc.client5.http.impl.classic.CloseableHttpClient";
    private static final String DO_EXECUTE = "(Lorg/apache/hc/core5/http/HttpHost;"
            + "Lorg/apache/hc/core5/http/ClassicHttpRequest;"
            + "Lorg/apache/hc/core5/http/protocol/HttpContext;)"
            + "Lorg/apache/hc/client5/http/impl/classic/CloseableHttpResponse;";
    private static final String EXCHANGE = "(Lorg/apache/hc/core5/http/ClassicHttpRequest;"
            + "Lorg/apache/hc/core5/http/io/HttpClientConnection;"
            + "Lorg/apache/hc/core5/http/io/HttpResponseInformationCallback;"
            + "Lorg/apache/hc/core5/http/protocol/HttpContext;)"
            + "Lorg/apache/hc/core5/http/ClassicHttpResponse;";

    @Override
    public String name() {
        return "apache-httpclient5";
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
        Exchanges ex = new Exchanges();
        Executed p = new Executed(ex);
        String pkg = "org/apache/hc/client5/http/impl/classic/";
        return List.of(
                new Target(pkg + "InternalHttpClient", "doExecute", DO_EXECUTE, 0, p)
                        .withSecond(1).stamped(),
                new Target(pkg + "MinimalHttpClient", "doExecute", DO_EXECUTE, 0, p)
                        .withSecond(1).stamped(),
                new Target("org/apache/hc/core5/http/impl/io/HttpRequestExecutor", "execute",
                        EXCHANGE, 1, ex));
    }

    /** HttpRequestExecutor.execute's exit: counts the exchange; records nothing. */
    static final class Exchanges implements BiFunction<Object[], long[], String[]> {
        final Slots slots = new Slots(CLIENT);
        private final ClassValue<Api> apis = Api.values();

        @Override
        public String[] apply(Object[] a, long[] meta) {
            // a[3]: the innermost entry-stamped hook running here — the
            // request's doExecute, unless the exchange is not a client's.
            Slots.Slot s = slots.exchange(a[3], meta[Request.M_START]);
            if (s == null || a[2] == null) {
                return null;
            }
            try {
                Api api = apis.get(a[1].getClass());
                s.local = Request.addr(api.socket(api.localAddr, a[2]));
                s.remote = Request.addr(api.socket(api.remoteAddr, a[2]));
                if (a[0] != null && !(a[0] instanceof Throwable)) {
                    s.version = api.version(a[0]);
                }
            } catch (Throwable t) {
                s.local = "";   // the event goes out without its connection
                s.remote = "";
            }
            return null;
        }
    }

    /** doExecute's exit: a[2] is {target host, request}, a[0] the response or throwable. */
    static final class Executed implements BiFunction<Object[], long[], String[]> {
        private final Exchanges exchanges;
        private final ClassValue<Api> apis = Api.values();

        Executed(Exchanges exchanges) {
            this.exchanges = exchanges;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            Slots.Slot s = exchanges.slots.take(a[1], Request.ownStart(a, meta));
            if (a[0] == null || a[0] instanceof Throwable) {
                Slots.failed(s);
                return null;
            }
            if (!(a[2] instanceof Object[] pair) || pair.length != 2 || pair[1] == null) {
                return null;
            }
            try {
                Request r = apis.get(a[1].getClass()).describe(pair[0], pair[1], a[0]);
                Slots.apply(s, r);
                r.start = Request.ownStart(a, meta);
                return r.toRecord(meta);
            } catch (RuntimeException | Error e) {
                throw e;
            } catch (Throwable t) {
                throw new IllegalStateException(t);   // counted as a lost record by Hooks
            }
        }
    }

    /** httpcore5's API, found in the loader of one of the library's classes. */
    record Api(MethodHandle method, MethodHandle scheme, MethodHandle authority,
               MethodHandle path, MethodHandle hostName, MethodHandle hostScheme,
               MethodHandle headers, MethodHandle name, MethodHandle value,
               MethodHandle versionOf, MethodHandle code, MethodHandle entity,
               MethodHandle length, MethodHandle localAddr, MethodHandle remoteAddr) {
        private static final String H = "org.apache.hc.core5.http.";

        static ClassValue<Api> values() {
            return new ClassValue<>() {
                @Override
                protected Api computeValue(Class<?> c) {
                    return of(c);
                }
            };
        }

        static Api of(Class<?> near) {
            Class<?> req = Handles.sibling(near, H + "HttpRequest");
            Class<?> msg = Handles.sibling(near, H + "HttpMessage");
            Class<?> named = Handles.sibling(near, "org.apache.hc.core5.net.NamedEndpoint");
            Class<?> pair = Handles.sibling(near, H + "NameValuePair");
            Class<?> conn = Handles.sibling(near, H + "HttpConnection");
            return new Api(Handles.virtual(req, "getMethod", String.class),
                    Handles.virtual(req, "getScheme", String.class),
                    Handles.virtual(req, "getAuthority", "org.apache.hc.core5.net.URIAuthority"),
                    Handles.virtual(req, "getPath", String.class),
                    Handles.virtual(named, "getHostName", String.class),
                    Handles.virtual(Handles.sibling(near, H + "HttpHost"), "getSchemeName",
                            String.class),
                    Handles.virtual(msg, "getHeaders", "[L" + H + "Header;"),
                    Handles.virtual(pair, "getName", String.class),
                    Handles.virtual(pair, "getValue", String.class),
                    Handles.virtual(msg, "getVersion", H + "ProtocolVersion"),
                    Handles.virtual(Handles.sibling(near, H + "HttpResponse"), "getCode", int.class),
                    Handles.virtual(Handles.sibling(near, H + "HttpEntityContainer"), "getEntity",
                            H + "HttpEntity"),
                    Handles.virtual(Handles.sibling(near, H + "EntityDetails"), "getContentLength",
                            long.class),
                    Handles.virtual(conn, "getLocalAddress", SocketAddress.class),
                    Handles.virtual(conn, "getRemoteAddress", SocketAddress.class));
        }

        SocketAddress socket(MethodHandle h, Object conn) throws Throwable {
            return Handles.call(h, conn) instanceof SocketAddress sa ? sa : null;
        }

        String version(Object msg) throws Throwable {
            Object v = Handles.call(versionOf, msg);
            return v == null ? null : v.toString();
        }

        /** The request as the caller made it and the response it got. */
        Request describe(Object target, Object req, Object resp) throws Throwable {
            Request r = new Request();
            r.held = true; // synchronous: see Request.held
            r.method = nz(Handles.string(method, req));
            String scheme = Handles.string(this.scheme, req);
            r.scheme = nz(scheme != null ? scheme : Handles.string(hostScheme, target));
            Object auth = Handles.call(authority, req);
            r.host = nz(Handles.string(hostName, auth != null ? auth : target));
            r.path = nz(Handles.string(path, req));
            r.version = nz(version(resp));
            r.status = (int) Handles.number(code, resp, 0);
            Map<String, List<String>> out = headers(resp);
            r.requestHeaders(headers(req));
            r.responseHeaders(out);
            Object body = Handles.call(entity, req);
            r.reqBytes = body == null ? 0 : Handles.number(length, body, Request.UNKNOWN);
            r.respBytes = Request.declaredBody(r.method, r.status, Request.first(out,
                    "Content-Length"));
            return r;
        }

        private Map<String, List<String>> headers(Object msg) throws Throwable {
            Map<String, List<String>> out = new LinkedHashMap<>();
            if (Handles.call(headers, msg) instanceof Object[] hs) {
                for (Object h : hs) {
                    String n = Handles.string(name, h);
                    if (n != null && out.size() < Request.MAX_HEADERS * 4) {
                        // get/put, not computeIfAbsent: no lambda linking in a probe.
                        List<String> vs = out.get(n);
                        if (vs == null) {
                            vs = new ArrayList<>();
                            out.put(n, vs);
                        }
                        vs.add(nz(Handles.string(value, h)));
                    }
                }
            }
            return out;
        }

        private static String nz(String s) {
            return s == null ? "" : s;
        }
    }
}
