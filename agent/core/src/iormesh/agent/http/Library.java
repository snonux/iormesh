package iormesh.agent.http;

import java.util.List;

/**
 * One HTTP library the agent hooks (decision #22's v1 set): its methods
 * and their probes, and what of its module the probes reflect on. One
 * class per library ({@link JdkHttpServer}, {@link JdkHttpClient},
 * {@link JdkUrlConnection}, and the name lookups of {@link JdkDns});
 * the rewriting itself is shared (iormesh.agent.HttpTransformer), as is
 * the record format ({@link Request}).
 */
public interface Library {
    /** A short name for the agent's messages and debug dump. */
    String name();

    /** The module whose classes the targets are (java.base, java.net.http, …). */
    String module();

    /**
     * The packages of {@link #module} the probes reflect on privately; the
     * agent opens them to its own module before building the probes.
     */
    List<String> opens();

    /** The hooked methods; called once the packages are open. */
    List<Target> targets() throws ReflectiveOperationException;
}
