package iormesh.agent.http;

import java.lang.invoke.MethodHandle;
import java.net.Socket;
import java.util.List;
import java.util.Map;
import java.util.function.BiFunction;

/**
 * OkHttp 4 and 5 (decision #22's v1 set, task de4): one http event per
 * call, recorded on the thread that ran the call's interceptor chain —
 * the caller's for {@code execute()}, OkHttp's dispatcher thread for
 * {@code enqueue()} — when the chain hands back the response, before
 * any callback of the app runs and before the body is read.
 *
 * Two hooks, both on that thread, the same split as Apache HttpClient's:
 * <ul>
 *   <li>{@code RealCall.getResponseWithInterceptorChain$okhttp()} (what
 *       execute and the dispatcher's AsyncCall both run), entry-stamped:
 *       the call's start, and at its return the event — method, scheme,
 *       host and path of the call's original request (redirects are
 *       followed inside), status, protocol and headers of the response;
 *   <li>{@code CallServerInterceptor.intercept(Chain)}, the one network
 *       exchange of each attempt, which records nothing: it counts the
 *       call's exchanges (a followed redirect, an answered 401/407 or a
 *       retried attempt is another — the server saw each) and keeps the
 *       connection's socket ends, read through the public
 *       {@code Chain.connection().socket()}, in the per-thread slot the
 *       call's own exit claims (Slots.Slot).
 * </ul>
 *
 * A response served from OkHttp's cache had no exchange: it gets no
 * connection and stays unnumbered. A call that failed records nothing
 * unless an exchange had happened — then the probe throws, a lost
 * record. Only OkHttp's public API is called, never anything that reads
 * or consumes the body.
 */
public final class OkHttp implements Library {
    static final String CALL = "okhttp3.internal.connection.RealCall";

    @Override
    public String name() {
        return "okhttp";
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
        Exchange ex = new Exchange();
        return List.of(
                new Target("okhttp3/internal/connection/RealCall",
                        "getResponseWithInterceptorChain$okhttp", "()Lokhttp3/Response;",
                        Target.NO_ARG, new Called(ex)).stamped(),
                new Target("okhttp3/internal/http/CallServerInterceptor", "intercept",
                        "(Lokhttp3/Interceptor$Chain;)Lokhttp3/Response;", 0, ex));
    }

    /** CallServerInterceptor.intercept's exit: counts the exchange; records nothing. */
    static final class Exchange implements BiFunction<Object[], long[], String[]> {
        final Slots slots = new Slots(CALL);
        private final ClassValue<Api> apis = Api.perClass();

        @Override
        public String[] apply(Object[] a, long[] meta) {
            Slots.Slot s = slots.exchange(a[3]);
            if (s == null || a[2] == null) {
                return null;
            }
            try {
                Api api = apis.get(a[1].getClass());
                Object conn = Handles.call(api.connection, a[2]);
                if (conn != null && Handles.call(api.socket, conn) instanceof Socket sock) {
                    s.local = Request.addr(sock.getLocalSocketAddress());
                    s.remote = Request.addr(sock.getRemoteSocketAddress());
                }
            } catch (Throwable t) {
                s.local = "";   // the event goes out without its connection
                s.remote = "";
            }
            return null;
        }
    }

    /** getResponseWithInterceptorChain's exit: a[1] is the call, a[0] the response. */
    static final class Called implements BiFunction<Object[], long[], String[]> {
        private final Exchange exchanges;
        private final ClassValue<Api> apis = Api.perClass();

        Called(Exchange exchanges) {
            this.exchanges = exchanges;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            Slots.Slot s = exchanges.slots.take(a[1]);
            if (a[0] == null || a[0] instanceof Throwable) {
                Slots.failed(s);
                return null;
            }
            try {
                Api api = apis.get(a[1].getClass());
                Request r = api.describe(Handles.call(api.callRequest, a[1]), a[0]);
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

    /** OkHttp's public API, found in the loader of one of its classes. */
    record Api(MethodHandle callRequest, MethodHandle connection, MethodHandle socket,
               MethodHandle url, MethodHandle method, MethodHandle reqHeaders,
               MethodHandle body, MethodHandle length, MethodHandle scheme, MethodHandle host,
               MethodHandle path, MethodHandle query, MethodHandle code,
               MethodHandle protocol, MethodHandle respHeaders, MethodHandle names,
               MethodHandle values) {
        static ClassValue<Api> perClass() {
            return new ClassValue<>() {
                @Override
                protected Api computeValue(Class<?> c) {
                    return of(c);
                }
            };
        }

        static Api of(Class<?> near) {
            Class<?> req = Handles.sibling(near, "okhttp3.Request");
            Class<?> url = Handles.sibling(near, "okhttp3.HttpUrl");
            Class<?> resp = Handles.sibling(near, "okhttp3.Response");
            Class<?> headers = Handles.sibling(near, "okhttp3.Headers");
            return new Api(Handles.virtual(Handles.sibling(near, "okhttp3.Call"), "request", req),
                    Handles.virtual(Handles.sibling(near, "okhttp3.Interceptor$Chain"),
                            "connection", "okhttp3.Connection"),
                    Handles.virtual(Handles.sibling(near, "okhttp3.Connection"), "socket",
                            Socket.class),
                    Handles.virtual(req, "url", url), Handles.virtual(req, "method", String.class),
                    Handles.virtual(req, "headers", headers),
                    Handles.virtual(req, "body", "okhttp3.RequestBody"),
                    Handles.virtual(Handles.sibling(near, "okhttp3.RequestBody"), "contentLength",
                            long.class),
                    Handles.virtual(url, "scheme", String.class),
                    Handles.virtual(url, "host", String.class),
                    Handles.virtual(url, "encodedPath", String.class),
                    Handles.virtual(url, "encodedQuery", String.class),
                    Handles.virtual(resp, "code", int.class),
                    Handles.virtual(resp, "protocol", "okhttp3.Protocol"),
                    Handles.virtual(resp, "headers", headers),
                    Handles.virtual(headers, "names", java.util.Set.class),
                    Handles.virtual(headers, "values", List.class, String.class));
        }

        /** The call's original request and the response the chain returned. */
        Request describe(Object req, Object resp) throws Throwable {
            Request r = new Request();
            Object u = Handles.call(url, req);
            r.method = nz(Handles.string(method, req));
            r.scheme = nz(Handles.string(scheme, u));
            r.host = nz(Handles.string(host, u));
            String q = Handles.string(query, u);
            r.path = nz(Handles.string(path, u)) + (q == null ? "" : "?" + q);
            r.version = version(Handles.call(protocol, resp));
            r.status = (int) Handles.number(code, resp, 0);
            r.requestHeaders(Handles.headers(names, values, Handles.call(reqHeaders, req)));
            Map<String, List<String>> out = Handles.headers(names, values,
                    Handles.call(respHeaders, resp));
            r.responseHeaders(out);
            Object b = Handles.call(body, req);
            r.reqBytes = b == null ? 0 : Handles.number(length, b, Request.UNKNOWN);
            r.respBytes = Request.declaredBody(r.method, r.status,
                    Request.first(out, "Content-Length"));
            return r;
        }

        /** OkHttp's protocol names (http/1.1, h2, …) as HTTP versions. */
        static String version(Object protocol) {
            String p = protocol == null ? "" : protocol.toString();
            return switch (p) {
                case "http/1.0" -> "HTTP/1.0";
                case "http/1.1" -> "HTTP/1.1";
                case "h2", "h2_prior_knowledge" -> "HTTP/2";
                default -> p.toUpperCase(java.util.Locale.ROOT);
            };
        }

        private static String nz(String s) {
            return s == null ? "" : s;
        }
    }
}
