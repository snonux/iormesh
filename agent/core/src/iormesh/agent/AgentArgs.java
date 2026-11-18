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
 *     jdk21) instead of picking it from the running JDK;</li>
 * <li>{@code debug=<path>} — at JVM exit, write the hook table with
 *     whether each row was applied and how many brackets it closed
 *     (the smoke test's evidence that the hooks fire);</li>
 * <li>{@code nostack} — skip the stack walk (S1's cost split).</li>
 * </ul>
 *
 * Unknown options are an error: the agent then disables itself rather
 * than run with a setting the caller did not mean.
 */
record AgentArgs(String bridge, String table, String debug, boolean walkStack) {
    static AgentArgs parse(String args) {
        String bridge = null;
        String table = null;
        String debug = null;
        boolean walkStack = true;
        if (args == null || args.isBlank()) {
            return new AgentArgs(null, null, null, true);
        }
        for (String opt : args.split(",")) {
            int eq = opt.indexOf('=');
            String key = eq < 0 ? opt : opt.substring(0, eq);
            String value = eq < 0 ? null : opt.substring(eq + 1);
            switch (key) {
                case "bridge" -> bridge = required(key, value);
                case "table" -> table = required(key, value);
                case "debug" -> debug = required(key, value);
                case "nostack" -> walkStack = flag(key, value);
                default -> throw new IllegalArgumentException("unknown agent option " + key);
            }
        }
        return new AgentArgs(bridge, table, debug, walkStack);
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
