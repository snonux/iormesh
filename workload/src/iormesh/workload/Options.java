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
        return v == null ? dflt : parsePort(v, 0);
    }

    /** The --port value, which must be present and connectable (1..65535). */
    public static int requiredPort(Map<String, String> opts) throws UsageException {
        String v = opts.get("port");
        if (v == null) {
            throw new UsageException("missing --port");
        }
        return parsePort(v, 1);
    }

    private static int parsePort(String v, int min) throws UsageException {
        int port;
        try {
            port = Integer.parseInt(v);
        } catch (NumberFormatException e) {
            throw new UsageException("bad --port: " + v);
        }
        if (port < min || port > MAX_PORT) {
            throw new UsageException("--port out of range [" + min + ", " + MAX_PORT + "]: " + v);
        }
        return port;
    }
}
