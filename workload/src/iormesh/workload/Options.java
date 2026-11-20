package iormesh.workload;

import java.util.Map;
import java.util.Set;
import java.util.TreeSet;

/**
 * Validation helpers for scenario options ({@code --key value}). They
 * throw {@link UsageException}, so a typo'd or out-of-range option exits
 * with the usage code before the scenario does any I/O.
 */
public final class Options {
    static final int MAX_PORT = 65535;

    private Options() {}

    /** Rejects any option key outside known; "role" is always allowed. */
    public static void requireKnown(Map<String, String> opts, Set<String> known)
            throws UsageException {
        // TreeSet: report unknown keys in a stable order.
        Set<String> unknown = new TreeSet<>(opts.keySet());
        unknown.remove("role");
        unknown.removeAll(known);
        if (!unknown.isEmpty()) {
            throw new UsageException("unknown option(s): " + unknown);
        }
    }

    /** Rejects a role the scenario does not implement. */
    public static void requireRole(String scenario, String role, Set<String> allowed)
            throws UsageException {
        if (!allowed.contains(role)) {
            throw new UsageException(scenario + " does not support role " + role
                    + "; use one of " + new TreeSet<>(allowed));
        }
    }

    /** The --port value, or dflt when absent; 0 means "any free port". */
    public static int port(Map<String, String> opts, int dflt) throws UsageException {
        String v = opts.get("port");
        return v == null ? dflt : parsePort("port", v, 0);
    }

    /** The --port value, which must be present and connectable (1..65535). */
    public static int requiredPort(Map<String, String> opts) throws UsageException {
        return requiredPort(opts, "port");
    }

    /**
     * The {@code --key} port, which must be present and connectable: a
     * scenario that dials several servers names each port by its own key
     * (microservices' {@code --orders-port}, {@code --billing-port}, …).
     */
    public static int requiredPort(Map<String, String> opts, String key) throws UsageException {
        String v = opts.get(key);
        if (v == null) {
            throw new UsageException("missing --" + key);
        }
        return parsePort(key, v, 1);
    }

    /**
     * The {@code --key} value as an int in [min, max], or dflt when
     * absent. Load scenarios size their work with these (syscall-burst's
     * --ops and --threads); a typo must exit 2 before any I/O, not run a
     * default-sized burst.
     */
    public static int intInRange(Map<String, String> opts, String key, int dflt, int min,
            int max) throws UsageException {
        String v = opts.get(key);
        if (v == null) {
            return dflt;
        }
        int n;
        try {
            n = Integer.parseInt(v);
        } catch (NumberFormatException e) {
            throw new UsageException("bad --" + key + ": " + v);
        }
        if (n < min || n > max) {
            throw new UsageException("--" + key + " out of range [" + min + ", " + max + "]: " + v);
        }
        return n;
    }

    private static int parsePort(String key, String v, int min) throws UsageException {
        int port;
        try {
            port = Integer.parseInt(v);
        } catch (NumberFormatException e) {
            throw new UsageException("bad --" + key + ": " + v);
        }
        if (port < min || port > MAX_PORT) {
            throw new UsageException("--" + key + " out of range [" + min + ", " + MAX_PORT + "]: "
                    + v);
        }
        return port;
    }
}
