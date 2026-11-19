package iormesh.agent.http;

import java.net.InetAddress;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;
import java.util.function.BiFunction;

/**
 * The JDK's name lookups (decision #8's default, task wd4): which host
 * name a JVM resolved to which addresses, so a connection's peer can be
 * named by the name the app used, not only its IP. One hook, the public
 * static InetAddress.getAllByName(String), unchanged from 17 to 27:
 * getByName, InetSocketAddress(host, port) — so Socket, HttpClient and
 * HttpURLConnection — all resolve through it.
 *
 * Its probe records a mapping only when it is new to this JVM: a host
 * seen for the first time, or resolving to another set of addresses
 * than last time (compared sorted, so a resolver's rotation is no
 * change). An app that resolves per request would otherwise write a
 * record per request. The memory holds MAX_HOSTS names; past that it is
 * cleared, and a mapping seen before is recorded once more. IP literals (which getAllByName parses without a
 * lookup), an empty host (the loopback) and a failed lookup record
 * nothing. Reverse lookups (getHostName) are not hooked.
 */
public final class JdkDns implements Library {
    /** Hosts remembered per JVM; past this the memory starts afresh. */
    static final int MAX_HOSTS = 4096;

    @Override
    public String name() {
        return "jdk-dns";
    }

    @Override
    public String module() {
        return "java.base";
    }

    @Override
    public List<String> opens() {
        return List.of();   // public API only
    }

    @Override
    public List<Target> targets() {
        return List.of(new Target("java/net/InetAddress", "getAllByName",
                "(Ljava/lang/String;)[Ljava/net/InetAddress;", 0, new Resolved()).lookup());
    }

    /** getAllByName's exit: {host, address...} when the mapping is new, else null. */
    static final class Resolved implements BiFunction<Object[], long[], String[]> {
        private final Map<String, String> seen = new ConcurrentHashMap<>();

        @Override
        public String[] apply(Object[] a, long[] meta) {
            if (!(a[0] instanceof InetAddress[] addrs) || addrs.length == 0
                    || !(a[2] instanceof String host) || host.isEmpty() || literal(host)) {
                return null;
            }
            String[] out = new String[addrs.length + 1];
            out[0] = host;
            for (int i = 0; i < addrs.length; i++) {
                out[i + 1] = address(addrs[i]);
            }
            String key = String.join(",", sorted(out));
            if (seen.size() >= MAX_HOSTS && !seen.containsKey(host)) {
                seen.clear();   // starts afresh: known mappings are recorded again
            }
            if (key.equals(seen.put(host, key))) {
                return null;   // known mapping
            }
            return out;
        }

        private static String[] sorted(String[] out) {
            String[] s = Arrays.copyOfRange(out, 1, out.length);
            Arrays.sort(s);
            return s;
        }
    }

    /** The textual address, without an IPv6 scope. */
    static String address(InetAddress ip) {
        String h = ip.getHostAddress();
        int scope = h.indexOf('%');
        return scope >= 0 ? h.substring(0, scope) : h;
    }

    /**
     * Whether host is an IP literal, which getAllByName parses without
     * asking the resolver: an IPv6 one has a colon (no name has), an IPv4
     * one only digits and dots.
     */
    static boolean literal(String host) {
        if (host.indexOf(':') >= 0) {
            return true;
        }
        for (int i = 0; i < host.length(); i++) {
            char c = host.charAt(i);
            if (c != '.' && (c < '0' || c > '9')) {
                return false;
            }
        }
        return true;
    }
}
