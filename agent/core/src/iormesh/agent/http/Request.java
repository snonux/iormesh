package iormesh.agent.http;

import java.net.Inet6Address;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.net.SocketAddress;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;

/**
 * One finished request as a probe describes it to the ring
 * (iormesh.bridge.Ring.http): the fixed strings — method, scheme, host,
 * path, version, local and remote socket address — then the request and
 * the response headers, each already redacted ({@link Redaction}). The
 * socket addresses are what the tracer joins the request to its
 * connection by (conn_id); either may be empty when the library no
 * longer holds it.
 *
 * Since task wd4 also its start stamp (the latency's beginning) and body
 * sizes, each only when the library knows it: 0 and -1 mean unknown,
 * never an estimate.
 */
final class Request {
    /** Headers kept per direction: bounds the record, never the request. */
    static final int MAX_HEADERS = 32;
    /** Indices into Hooks.http's long[] meta, mirroring Ring.M_*. */
    static final int M_STATUS = 0;
    static final int M_NREQ = 1;
    static final int M_FLAGS = 2;
    static final int M_EXCHANGES = 3;
    static final int M_START = 4;
    static final int M_REQ_BYTES = 5;
    static final int M_RESP_BYTES = 6;
    /** Unknown body size. */
    static final long UNKNOWN = -1;

    String method = "";
    String scheme = "";
    String host = "";
    String path = "";
    String version = "";
    String local = "";
    String remote = "";
    int status;
    /** The request was answered here (a server's event), not sent. */
    boolean server;
    /**
     * Recorded at a stamp that does not follow the connection's request
     * order (a sendAsync completion, run after the app's own callbacks):
     * the tracer numbers no ordinal for it and stops its connection's
     * count (Ring.HTTP_UNORDERED). Such a record has no start either.
     */
    boolean unordered;
    /**
     * The HTTP exchanges the request took: more than 1 when the library
     * followed a redirect or answered an authentication challenge itself
     * — the server saw each, the caller one request.
     */
    int exchanges = 1;
    /**
     * System.nanoTime() when the library began the request, 0 unknown: a
     * probe sets it from the open stamp (see {@link #ownStart}) only when
     * that stamp is its request's.
     */
    long start;
    /**
     * Request and response body bytes, {@link #UNKNOWN} unless the
     * library knows: a declared Content-Length (on the server, the
     * client's declaration), a publisher's length or the bytes counted.
     */
    long reqBytes = UNKNOWN;
    long respBytes = UNKNOWN;
    private final List<String> request = new ArrayList<>();
    private final List<String> response = new ArrayList<>();

    /** Adds request headers, redacted, by name (one entry per value). */
    void requestHeaders(Map<String, List<String>> h) {
        add(request, h);
    }

    void responseHeaders(Map<String, List<String>> h) {
        add(response, h);
    }

    /**
     * The strings Hooks.http writes; meta gets the request's numbers
     * (M_*). A negative status (no response) is recorded as 0; the start
     * is this request's own, so a stamp Hooks offered that the probe did
     * not adopt is cleared here.
     */
    String[] toRecord(long[] meta) {
        List<String> s = new ArrayList<>(List.of(method, scheme, host, Redaction.url(path),
                version, local, remote));
        s.addAll(request);
        s.addAll(response);
        meta[M_STATUS] = Math.max(status, 0);
        meta[M_NREQ] = request.size();
        // Ring.HTTP_SERVER and Ring.HTTP_UNORDERED
        meta[M_FLAGS] = (server ? 1 : 0) | (unordered ? 2 : 0);
        meta[M_EXCHANGES] = Math.max(exchanges, 1);
        meta[M_START] = unordered ? 0 : Math.max(start, 0);
        meta[M_REQ_BYTES] = reqBytes < 0 ? UNKNOWN : reqBytes;
        meta[M_RESP_BYTES] = respBytes < 0 ? UNKNOWN : respBytes;
        return s.toArray(new String[0]);
    }

    /**
     * The open stamp Hooks offers (meta[M_START]) when the open hook is
     * the hooked method itself — an entry-stamped target's own exit, a[3]
     * == a[1] — else 0: a stamp of some other call on the thread is not
     * this request's start.
     */
    static long ownStart(Object[] a, long[] meta) {
        return a.length > 3 && a[1] != null && a[3] == a[1] ? meta[M_START] : 0;
    }

    /**
     * A response body's size as its headers declare it: 0 for a reply
     * that has none by protocol (a HEAD request's, 1xx, 204, 304), the
     * Content-Length otherwise, {@link #UNKNOWN} without one (chunked, or
     * read to the close). The libraries check a declared length against
     * the bytes they move, so a completed request's is its size.
     */
    static long declaredBody(String method, int status, String contentLength) {
        if ("HEAD".equalsIgnoreCase(method) || (status >= 100 && status < 200)
                || status == 204 || status == 304) {
            return 0;
        }
        return parseLength(contentLength);
    }

    /** The first value of header name in h, by case-insensitive name; null if none. */
    static String first(Map<String, List<String>> h, String name) {
        if (h == null) {
            return null;
        }
        for (Map.Entry<String, List<String>> e : h.entrySet()) {
            List<String> v = e.getValue();
            if (name.equalsIgnoreCase(e.getKey()) && v != null && !v.isEmpty()) {
                return v.get(0);
            }
        }
        return null;
    }

    /** A Content-Length value, {@link #UNKNOWN} when absent or malformed. */
    static long parseLength(String raw) {
        String v = raw == null ? null : raw.trim();
        if (v == null || v.isEmpty() || v.length() > 18) {
            return UNKNOWN;
        }
        long n = 0;
        for (int i = 0; i < v.length(); i++) {
            char c = v.charAt(i);
            if (c < '0' || c > '9') {
                return UNKNOWN;
            }
            n = n * 10 + (c - '0');
        }
        return n;
    }

    /**
     * Sorted by name so a record does not depend on a map's order; only
     * real header names are kept (see token).
     */
    private static void add(List<String> out, Map<String, List<String>> h) {
        if (h == null) {
            return;
        }
        Map<String, List<String>> sorted = new TreeMap<>(String.CASE_INSENSITIVE_ORDER);
        for (Map.Entry<String, List<String>> e : h.entrySet()) {
            if (token(e.getKey()) && e.getValue() != null) {
                // Loops, not lambdas, on this path: a lambda's first call
                // links (and spins classes) inside the target's request.
                List<String> m = sorted.get(e.getKey());
                if (m == null) {
                    m = new ArrayList<>();
                    sorted.put(e.getKey(), m);
                }
                m.addAll(e.getValue());
            }
        }
        for (Map.Entry<String, List<String>> e : sorted.entrySet()) {
            for (String v : e.getValue()) {
                if (out.size() == MAX_HEADERS) {
                    return;
                }
                out.add(Redaction.header(e.getKey(), v));
            }
        }
    }

    /**
     * Whether name is an HTTP header name (RFC 9110 token). Anything else
     * is not recorded: HttpURLConnection keeps its request line ("GET
     * /login?token=… HTTP/1.1", query and all) as a header without a
     * value, and its status line under a null name.
     */
    static boolean token(String name) {
        if (name == null || name.isEmpty()) {
            return false;
        }
        for (int i = 0; i < name.length(); i++) {
            char c = name.charAt(i);
            boolean alnum = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
                    || (c >= '0' && c <= '9');
            if (!alnum && "!#$%&'*+-.^_`|~".indexOf(c) < 0) {
                return false;
            }
        }
        return true;
    }

    /**
     * "ip:port" ("[ip]:port" for IPv6, without a scope), or "" for an
     * address that is unknown, unresolved or has no port — the tracer
     * then joins by the other end alone. Never a lookup: an unresolved
     * address stays unresolved.
     */
    static String addr(SocketAddress sa) {
        if (!(sa instanceof InetSocketAddress a) || a.isUnresolved() || a.getPort() <= 0) {
            return "";
        }
        return addr(a.getAddress(), a.getPort());
    }

    static String addr(InetAddress ip, int port) {
        if (ip == null || port <= 0 || ip.isAnyLocalAddress()) {
            return "";
        }
        String h = ip.getHostAddress();
        int scope = h.indexOf('%');
        if (scope >= 0) {
            h = h.substring(0, scope);
        }
        return ip instanceof Inet6Address ? "[" + h + "]:" + port : h + ":" + port;
    }

    /**
     * The address of an IP-literal URI host and port ("127.0.0.1",
     * "[::1]"), or "" for a host name: resolving it here would be DNS
     * inside the target's request (decision #8 hooks lookups instead).
     */
    static String literal(String host, int port) {
        if (host == null || host.isEmpty() || port <= 0) {
            return "";
        }
        String h = host.startsWith("[") && host.endsWith("]")
                ? host.substring(1, host.length() - 1) : host;
        boolean v6 = h.indexOf(':') >= 0;
        for (int i = 0; i < h.length(); i++) {
            char c = h.charAt(i);
            // IPv4: digits and dots only (a hex-looking name such as
            // "cafe" is a name); IPv6: hex digits, dots and colons.
            boolean ok = c == '.' || (v6 ? c == ':' || Character.digit(c, 16) >= 0
                    : c >= '0' && c <= '9');
            if (!ok) {
                return "";
            }
        }
        try {
            // A literal never triggers a lookup (InetAddress.getByName
            // parses it); the checks above keep names out.
            return addr(InetAddress.getByName(h), port);
        } catch (java.net.UnknownHostException e) {
            return "";
        }
    }
}
