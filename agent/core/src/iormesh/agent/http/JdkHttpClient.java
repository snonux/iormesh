package iormesh.agent.http;

import java.lang.reflect.Field;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.CompletableFuture;
import java.util.function.BiConsumer;
import java.util.function.BiFunction;
import java.util.function.Function;

/**
 * The JDK's HttpClient (java.net.http): one http event per synchronous
 * send, recorded when HttpClientImpl.send returns its response — on the
 * calling thread, so the stack names the caller (the client's socket
 * I/O itself runs on its selector thread). send is entry-stamped: the
 * latency runs from the call to its return. Body sizes are the request
 * publisher's contentLength (-1 for a streamed one: unknown) and the
 * response's Content-Length. The public HttpResponse gives
 * the request and its outcome; which connection carried it is internal:
 * the response's constructor gets the JDK's Response, whose localAddress
 * is the client end of the socket — a second hook binds it to the
 * response, and the send hook takes it from there.
 *
 * Asynchronous sends (task wd4): the public sendAsync(request, handler,
 * pushPromiseHandler) — the two-argument one and the facade both reach it,
 * send does not (it calls a private overload) — is entry-stamped; its
 * probe takes the caller's identity there (Async) and registers a
 * completion on the returned future, which records the response from the
 * completing thread under the caller's stack, thread and span, with the
 * latency from the sendAsync call. A future that fails or is cancelled
 * records nothing, as a send that throws.
 */
public final class JdkHttpClient implements Library {
    private static final String INTERNAL = "jdk.internal.net.http";
    private static final String HTTP = "Ljava/net/http/";

    /** The bridge's handles for sendAsync; null: sendAsync is not hooked (tests). */
    private final Async async;

    public JdkHttpClient(Async async) {
        this.async = async;
    }

    @Override
    public String name() {
        return "jdk-httpclient";
    }

    @Override
    public String module() {
        return "java.net.http";
    }

    @Override
    public List<String> opens() {
        return List.of(INTERNAL);
    }

    @Override
    public List<Target> targets() throws ReflectiveOperationException {
        Class<?> response = Class.forName(INTERNAL + ".Response", false,
                ClassLoader.getPlatformClassLoader());
        Field local = response.getDeclaredField("localAddress");
        local.setAccessible(true);
        Weak<String> bound = new Weak<>();
        List<Target> out = new ArrayList<>(List.of(
                new Target("jdk/internal/net/http/HttpResponseImpl", "<init>",
                        "(" + HTTP + "HttpRequest;Ljdk/internal/net/http/Response;"
                                + HTTP + "HttpResponse;Ljava/lang/Object;"
                                + "Ljdk/internal/net/http/Exchange;)V", 1, new Bind(local, bound)),
                new Target("jdk/internal/net/http/HttpClientImpl", "send",
                        "(" + HTTP + "HttpRequest;" + HTTP + "HttpResponse$BodyHandler;)"
                                + HTTP + "HttpResponse;", Target.NO_ARG, new Sent(bound))
                        .stamped()));
        if (async != null) {
            out.add(new Target("jdk/internal/net/http/HttpClientImpl", "sendAsync",
                    "(" + HTTP + "HttpRequest;" + HTTP + "HttpResponse$BodyHandler;"
                            + HTTP + "HttpResponse$PushPromiseHandler;)"
                            + "Ljava/util/concurrent/CompletableFuture;", Target.NO_ARG,
                    new SentAsync(bound, async)).stamped());
        }
        return out;
    }

    /** HttpResponseImpl's constructor: binds the response to its socket's local end. */
    static final class Bind implements BiFunction<Object[], long[], String[]> {
        private final Field local;
        private final Weak<String> bound;

        Bind(Field local, Weak<String> bound) {
            this.local = local;
            this.bound = bound;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            try {
                if (a[2] != null && local.get(a[2]) instanceof InetSocketAddress at) {
                    bound.put(a[1], Request.addr(at));
                }
            } catch (IllegalAccessException e) {
                // Opened at install; if not, the request records no local end.
            }
            return null;
        }
    }

    /** HttpClientImpl.send's return: the request, recorded once. */
    static final class Sent implements BiFunction<Object[], long[], String[]> {
        private final Weak<String> bound;

        Sent(Weak<String> bound) {
            this.bound = bound;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            if (!(a[0] instanceof HttpResponse<?> resp)) {
                return null;   // send threw: no response to record
            }
            Request r = describe(resp);
            String local = bound.take(resp);
            r.local = local == null ? "" : local;
            r.start = Request.ownStart(a, meta);   // send's entry
            return r.toRecord(meta);
        }
    }

    /**
     * sendAsync's return: the caller's identity now, the request when the
     * future completes. Already done at the return (a request refused at
     * once): recorded right here, like a send.
     */
    static final class SentAsync implements BiFunction<Object[], long[], String[]> {
        private final Weak<String> bound;
        private final Async async;

        SentAsync(Weak<String> bound, Async async) {
            this.bound = bound;
            this.async = async;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            if (!(a[0] instanceof CompletableFuture<?> cf)) {
                return null;   // sendAsync threw: no request to record
            }
            long start = Request.ownStart(a, meta);
            if (cf.isDone()) {
                return cf.isCompletedExceptionally()
                        || !(cf.getNow(null) instanceof HttpResponse<?> resp)
                        ? null : new Completion(null, start, bound, null).record(resp, meta);
            }
            long[] caller = java.util.Arrays.copyOf(async.caller().get(), Async.CALLER + 1);
            caller[Async.CALLER] = a.length > 4 && a[4] instanceof Integer id ? id : 0;
            cf.whenComplete(new Completion(caller, start, bound, async.late()));
            return null;
        }
    }

    /**
     * One sendAsync's completion: on success it asks the bridge to record
     * the response (late), then describes it when the bridge runs it
     * (apply), the thread busy. Never throws into the app's future chain.
     */
    static final class Completion implements BiConsumer<Object, Throwable>,
            Function<long[], String[]> {
        private final long[] caller;
        private final long start;
        private final Weak<String> bound;
        private final BiConsumer<long[], Function<long[], String[]>> late;
        private HttpResponse<?> resp;

        Completion(long[] caller, long start, Weak<String> bound,
                   BiConsumer<long[], Function<long[], String[]>> late) {
            this.caller = caller;
            this.start = start;
            this.bound = bound;
            this.late = late;
        }

        @Override
        public void accept(Object value, Throwable failure) {
            if (value instanceof HttpResponse<?> r) {
                resp = r;   // read by apply, which late runs on this thread
                late.accept(caller, this);
            }
        }

        @Override
        public String[] apply(long[] meta) {
            return resp == null ? null : record(resp, meta);
        }

        String[] record(HttpResponse<?> r, long[] meta) {
            Request q = describe(r);
            String local = bound.take(r);
            q.local = local == null ? "" : local;
            q.start = start;
            return q.toRecord(meta);
        }
    }

    static Request describe(HttpResponse<?> resp) {
        HttpRequest req = resp.request();
        URI u = resp.uri();
        Request r = new Request();
        r.method = req.method();
        r.scheme = u.getScheme() == null ? "" : u.getScheme();
        r.host = u.getHost() == null ? "" : u.getHost();
        String q = u.getRawQuery();
        r.path = (u.getRawPath() == null || u.getRawPath().isEmpty() ? "/" : u.getRawPath())
                + (q == null ? "" : "?" + q);
        r.version = resp.version() == HttpClient.Version.HTTP_2 ? "HTTP/2" : "HTTP/1.1";
        r.status = resp.statusCode();
        r.remote = Request.literal(r.host, port(u));
        r.exchanges = exchanges(resp);
        r.requestHeaders(req.headers().map());
        r.responseHeaders(resp.headers().map());
        // No lambda here: its first call would link classes inside the request.
        HttpRequest.BodyPublisher body = req.bodyPublisher().orElse(null);
        r.reqBytes = body == null ? 0 : body.contentLength();
        r.respBytes = Request.declaredBody(r.method, r.status,
                resp.headers().firstValue("content-length").orElse(null));
        return r;
    }

    /**
     * The exchanges behind a response: it and its previous responses —
     * the redirects HttpClient followed and the 401/407 challenges its
     * authenticator answered, each one request the server saw.
     */
    static int exchanges(HttpResponse<?> resp) {
        int n = 1;
        for (HttpResponse<?> p = resp.previousResponse().orElse(null); p != null && n < 1000;
                p = p.previousResponse().orElse(null)) {
            n++;
        }
        return n;
    }

    private static int port(URI u) {
        if (u.getPort() > 0) {
            return u.getPort();
        }
        return "https".equalsIgnoreCase(u.getScheme()) ? 443 : 80;
    }
}
