package iormesh.agent;

/**
 * Stand-in for an HTTP library's classes in HttpTransformerTest: a
 * constructor, a method returning its response, one that throws, a
 * static lookup, and a method the transformer must refuse (a primitive
 * argument).
 */
public final class HttpFixture {
    private final String name;

    /** Hooked with argument 0: the probe gets the name. */
    public HttpFixture(String name) {
        this.name = name;
    }

    /** Hooked without an argument: the probe gets the returned value. */
    public String send(String request) {
        return name + ":" + request;
    }

    /** Hooked with its last argument, after a long (two slots). */
    public String fail(long pad, String request) {
        if (pad > 0) {
            throw new IllegalStateException(request);
        }
        return request;
    }

    /** Hooked with arguments 0 and 2 (task de4): the probe gets the pair. */
    public String pair(String first, long pad, String second) {
        if (pad > 0) {
            throw new IllegalStateException(first);
        }
        return first + "/" + second;
    }

    /** Listed with argument 0, which it overwrites: not hooked (task de4). */
    public String reassign(String request) {
        request = request.trim();
        return request;
    }

    /** Listed with argument 0, an int: refused, nothing to pass. */
    public int count(int n) {
        return n;
    }

    /**
     * Static, hooked with argument 0 as a DNS target (task wd4): the
     * probe gets null as this, the host, and the result or, on the
     * throw, null.
     */
    public static String[] lookup(String host) {
        if (host.isEmpty()) {
            throw new IllegalArgumentException("empty");
        }
        return new String[] {host};
    }
}
