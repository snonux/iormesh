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
 */
final class Request {
    /** Headers kept per direction: bounds the record, never the request. */
    static final int MAX_HEADERS = 32;

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
     * The HTTP exchanges the request took: more than 1 when the library
     * followed a redirect or answered an authentication challenge itself
     * — the server saw each, the caller one request.
     */
    int exchanges = 1;
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
     * The strings Hooks.http writes; meta gets {status, request-header
     * count, flags, exchanges}. A negative status (no response) is
     * recorded as 0.
     */
    String[] toRecord(int[] meta) {
        List<String> s = new ArrayList<>(List.of(method, scheme, host, Redaction.url(path),
                version, local, remote));
        s.addAll(request);
        s.addAll(response);
        meta[0] = Math.max(status, 0);
        meta[1] = request.size();
        meta[2] = server ? 1 : 0;   // Ring.HTTP_SERVER
        meta[3] = Math.max(exchanges, 1);
        return s.toArray(new String[0]);
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
