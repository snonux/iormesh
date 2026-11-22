package iormesh.agent;

/**
 * The agent's options, from {@code -javaagent:iormesh-agent.jar=<args>}
 * or the live-attach argument string: comma-separated, each either
 * {@code key=value} or a bare flag.
 *
 * <ul>
 * <li>{@code bridge=<path>} — use this bridge jar instead of extracting
 *     the one nested in the agent jar;</li>
 * <li>{@code table=<variant>} — force a hook-table variant (jdk17,
 *     jdk21, jdk25) instead of picking it from the running JDK;</li>
 * <li>{@code debug=<path>} — at JVM exit, write the hook table with
 *     whether each row was applied and how many brackets it closed
 *     (the smoke test's evidence that the hooks fire);</li>
 * <li>{@code nostack} — skip the stack walk (S1's cost split);</li>
 * <li>{@code spool=<dir>} — the tracer-owned spool directory: the agent
 *     writes its records to the ring {@code <dir>/<pid>.ring}, or
 *     {@code <pid>.<hex>.ring} when another user holds that name
 *     (decision #27, task 9j4). Without it brackets go nowhere (counted for debug only);</li>
 * <li>{@code ringsize=<bytes>} — the ring's data size, a power of two
 *     from 64 KiB to 64 MiB (default 8 MiB; the tracer refuses more);</li>
 * <li>{@code watermark=<ms>} — how often the watermark thread writes a
 *     WATERMARK (default 10; 0 disables it, leaving only the install's
 *     and the shutdown's).</li>
 * </ul>
 *
 * Unknown options are an error: the agent then disables itself rather
 * than run with a setting the caller did not mean.
 */
record AgentArgs(String bridge, String table, String debug, boolean walkStack,
                 String spool, long ringSize, long watermarkMs) {
    /** 8 MiB: S1's 64 KiB ring dropped most of a burst without a reader. */
    static final long DEFAULT_RING_SIZE = 8L << 20;
    static final long DEFAULT_WATERMARK_MS = 10;

    static AgentArgs parse(String args) {
        String bridge = null;
        String table = null;
        String debug = null;
        boolean walkStack = true;
        String spool = null;
        long ringSize = DEFAULT_RING_SIZE;
        long watermarkMs = DEFAULT_WATERMARK_MS;
        for (String opt : args == null || args.isBlank() ? new String[0] : args.split(",")) {
            int eq = opt.indexOf('=');
            String key = eq < 0 ? opt : opt.substring(0, eq);
            String value = eq < 0 ? null : opt.substring(eq + 1);
            switch (key) {
                case "bridge" -> bridge = required(key, value);
                case "table" -> table = required(key, value);
                case "debug" -> debug = required(key, value);
                case "nostack" -> walkStack = flag(key, value);
                case "spool" -> spool = required(key, value);
                case "ringsize" -> ringSize = number(key, value);
                case "watermark" -> watermarkMs = number(key, value);
                default -> throw new IllegalArgumentException("unknown agent option " + key);
            }
        }
        return new AgentArgs(bridge, table, debug, walkStack, spool, ringSize, watermarkMs);
    }

    /** A non-negative decimal; the ring checks its own bounds. */
    private static long number(String key, String value) {
        try {
            long n = Long.parseLong(required(key, value));
            if (n >= 0) {
                return n;
            }
        } catch (NumberFormatException e) {
            // reported below, with the option's name
        }
        throw new IllegalArgumentException("agent option " + key + " needs a number >= 0, got "
                + value);
    }

    private static String required(String key, String value) {
        if (value == null || value.isEmpty()) {
            throw new IllegalArgumentException("agent option " + key + " needs a value");
        }
        return value;
    }

    /** A bare flag; nostack=… is rejected rather than half-understood. */
    private static boolean flag(String key, String value) {
        if (value != null) {
            throw new IllegalArgumentException("agent option " + key + " takes no value");
        }
        return false;
    }
}
