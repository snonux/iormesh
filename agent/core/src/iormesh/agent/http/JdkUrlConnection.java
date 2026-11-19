package iormesh.agent.http;

import java.lang.reflect.Field;
import java.lang.reflect.Method;
import java.net.HttpURLConnection;
import java.net.Socket;
import java.net.URL;
import java.util.List;
import java.util.Map;
import java.util.function.BiFunction;

/**
 * The JDK's HttpURLConnection (java.base, sun.net.www.protocol.http; its
 * https flavor delegates to the same class): one http event per
 * connection object, recorded when its private getInputStream0 —
 * through which getInputStream, getResponseCode and the header getters
 * all read the response — first returns or throws with a response code
 * (a 404 throws FileNotFoundException after reading the response). The
 * calling thread does the socket I/O too.
 *
 * The response code, the request and response headers and the socket are
 * internal: the probes read java.net.HttpURLConnection.responseCode and
 * the MessageHeaders by reflection rather than call getResponseCode or
 * getHeaderFields, which reconnect when no response came. The socket is
 * bound when the JDK's HttpClient parses the response (parseHTTP, whose
 * last parameter is the connection — JDK 17 has a ProgressSource before
 * it, later JDKs not): getInputStream0 drops it once a body-less
 * response is done.
 */
public final class JdkUrlConnection implements Library {
    private static final String IMPL = "sun.net.www.protocol.http.HttpURLConnection";

    @Override
    public String name() {
        return "jdk-urlconnection";
    }

    @Override
    public String module() {
        return "java.base";
    }

    @Override
    public List<String> opens() {
        return List.of("java.net", "sun.net", "sun.net.www", "sun.net.www.protocol.http");
    }

    @Override
    public List<Target> targets() throws ReflectiveOperationException {
        ClassLoader boot = null;
        Field code = field(HttpURLConnection.class, "responseCode");
        Class<?> impl = Class.forName(IMPL, false, boot);
        Field requests = field(impl, "requests");
        Field responses = field(impl, "responses");
        Method headers = Class.forName("sun.net.www.MessageHeader", false, boot)
                .getMethod("getHeaders");
        headers.setAccessible(true);
        Field socket = field(Class.forName("sun.net.NetworkClient", false, boot), "serverSocket");
        Weak<Bound> bound = new Weak<>();
        Bind bind = new Bind(socket, bound);
        Done done = new Done(code, requests, responses, headers, bound);
        done.walker.walk(done.depth);   // link the walk now, not in a request
        String client = "sun/net/www/http/HttpClient";
        String huc = "Lsun/net/www/protocol/http/HttpURLConnection;";
        return List.of(
                new Target(client, "parseHTTP", "(Lsun/net/www/MessageHeader;" + huc + ")Z",
                        Target.LAST_ARG, bind),
                new Target(client, "parseHTTP",
                        "(Lsun/net/www/MessageHeader;Lsun/net/ProgressSource;" + huc + ")Z",
                        Target.LAST_ARG, bind),
                new Target("sun/net/www/protocol/http/HttpURLConnection", "getInputStream0",
                        "()Ljava/io/InputStream;", Target.NO_ARG, done));
    }

    private static Field field(Class<?> c, String name) throws NoSuchFieldException {
        Field f = c.getDeclaredField(name);
        f.setAccessible(true);
        return f;
    }

    /**
     * What parseHTTP left for a connection object: its socket's two ends
     * (the last response's) and how many responses it parsed — more than
     * one when getInputStream0 followed a redirect or answered a 401/407
     * challenge itself (an interim 100 Continue counts too: over-cautious,
     * the tracer only stops numbering).
     */
    static final class Bound {
        String local = "";
        String remote = "";
        int parses;
    }

    /** parseHTTP: binds the connection object to its socket and counts it. */
    static final class Bind implements BiFunction<Object[], int[], String[]> {
        private final Field socket;
        private final Weak<Bound> bound;

        Bind(Field socket, Weak<Bound> bound) {
            this.socket = socket;
            this.bound = bound;
        }

        @Override
        public String[] apply(Object[] a, int[] meta) {
            if (a[2] == null) {
                return null;
            }
            Bound b = bound.get(a[2]);
            if (b == null) {
                b = new Bound();
                bound.put(a[2], b);
            }
            b.parses++;
            try {
                if (socket.get(a[1]) instanceof Socket s) {
                    b.local = Request.addr(s.getLocalAddress(), s.getLocalPort());
                    b.remote = Request.addr(s.getInetAddress(), s.getPort());
                }
            } catch (IllegalAccessException e) {
                // Opened at install; if not, the request records no socket.
            }
            return null;
        }
    }

    /** getInputStream0's exit: the request, once it has a response code. */
    static final class Done implements BiFunction<Object[], int[], String[]> {
        private final Field code;
        private final Field requests;
        private final Field responses;
        private final Method headers;
        private final Weak<Bound> bound;
        private final Weak<Boolean> recorded = new Weak<>();
        /**
         * Counts getInputStream0 frames on the stack: following a redirect
         * or an authentication challenge, getInputStream0 reaches itself
         * again (getResponseCode → getInputStream), and that inner exit
         * sees the interim response (302, 401). Only the outermost exit
         * records. Built at install: a lambda's first use links classes.
         */
        private final java.util.function.Function<java.util.stream.Stream<StackWalker.StackFrame>,
                Long> depth = frames -> frames.filter(f -> f.getMethodName().equals("getInputStream0")
                        && f.getClassName().equals(IMPL)).count();
        private final StackWalker walker = StackWalker.getInstance();

        Done(Field code, Field requests, Field responses, Method headers, Weak<Bound> bound) {
            this.code = code;
            this.requests = requests;
            this.responses = responses;
            this.headers = headers;
            this.bound = bound;
        }

        @Override
        public String[] apply(Object[] a, int[] meta) {
            try {
                if (!(a[1] instanceof HttpURLConnection c) || recorded.get(c) != null
                        || code.getInt(c) < 0 || walker.walk(depth) > 1) {
                    return null;   // no response yet, done, or an inner exit
                }
                recorded.put(c, Boolean.TRUE);
                return describe(c).toRecord(meta);
            } catch (ReflectiveOperationException e) {
                return null;
            }
        }

        private Request describe(HttpURLConnection c) throws ReflectiveOperationException {
            URL u = c.getURL();
            Request r = new Request();
            r.method = c.getRequestMethod();
            r.scheme = u.getProtocol();
            r.host = u.getHost();
            r.path = u.getFile().isEmpty() ? "/" : u.getFile();
            r.version = "HTTP/1.1";
            r.status = code.getInt(c);
            Bound b = bound.take(c);
            if (b != null) {
                r.local = b.local;
                r.remote = b.remote;
                r.exchanges = b.parses;
            }
            r.requestHeaders(map(requests.get(c)));
            r.responseHeaders(map(responses.get(c)));
            return r;
        }

        @SuppressWarnings("unchecked")
        private Map<String, List<String>> map(Object messageHeader)
                throws ReflectiveOperationException {
            return messageHeader == null ? null
                    : (Map<String, List<String>>) headers.invoke(messageHeader);
        }
    }
}
