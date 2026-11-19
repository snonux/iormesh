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
        Class<?> impl = Class.forName("sun.net.www.protocol.http.HttpURLConnection", false, boot);
        Field requests = field(impl, "requests");
        Field responses = field(impl, "responses");
        Method headers = Class.forName("sun.net.www.MessageHeader", false, boot)
                .getMethod("getHeaders");
        headers.setAccessible(true);
        Field socket = field(Class.forName("sun.net.NetworkClient", false, boot), "serverSocket");
        Weak<String[]> bound = new Weak<>();
        Bind bind = new Bind(socket, bound);
        Done done = new Done(code, requests, responses, headers, bound);
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

    /** parseHTTP: binds the connection object to its socket's two ends. */
    static final class Bind implements BiFunction<Object[], int[], String[]> {
        private final Field socket;
        private final Weak<String[]> bound;

        Bind(Field socket, Weak<String[]> bound) {
            this.socket = socket;
            this.bound = bound;
        }

        @Override
        public String[] apply(Object[] a, int[] meta) {
            try {
                if (a[2] != null && socket.get(a[1]) instanceof Socket s) {
                    bound.put(a[2], new String[] {Request.addr(s.getLocalAddress(), s.getLocalPort()),
                            Request.addr(s.getInetAddress(), s.getPort())});
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
        private final Weak<String[]> bound;
        private final Weak<Boolean> recorded = new Weak<>();

        Done(Field code, Field requests, Field responses, Method headers, Weak<String[]> bound) {
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
                        || code.getInt(c) < 0) {
                    return null;   // no response yet (it threw before one), or done
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
            String[] ends = bound.take(c);
            if (ends != null) {
                r.local = ends[0];
                r.remote = ends[1];
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
