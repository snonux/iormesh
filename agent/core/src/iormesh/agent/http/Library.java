package iormesh.agent.http;

import java.util.List;

/**
 * One HTTP library the agent hooks (decision #22's v1 set): its methods
 * and their probes, and what of its module the probes reflect on. One
 * class per library ({@link JdkHttpServer}, {@link JdkHttpClient},
 * {@link JdkUrlConnection}, and the name lookups of {@link JdkDns}; the
 * third-party ones of the app's class path, {@link Servlet}, …);
 * the rewriting itself is shared (iormesh.agent.HttpTransformer), as is
 * the record format ({@link Request}).
 */
public interface Library {
    /** A short name for the agent's messages and debug dump. */
    String name();

    /**
     * The module whose classes the targets are (java.base, java.net.http,
     * …); null for a library of the app's own class path ({@link
     * #appLoaded}), which the agent finds in whatever loader defines it.
     */
    String module();

    /**
     * The packages of {@link #module} the probes reflect on privately; the
     * agent opens them to its own module before building the probes.
     */
    List<String> opens();

    /** The hooked methods; called once the packages are open. */
    List<Target> targets() throws ReflectiveOperationException;

    /**
     * Whether the targets are third-party classes an application class
     * loader defines (task de4: Servlet, Apache HttpClient, OkHttp, Netty)
     * rather than the JDK's. Such a library may be loaded by several
     * loaders, or by none, so nothing of it is resolved at install: its
     * probes find the library's API reflectively in the class loader of
     * the objects they are handed, per class, and hold it no longer than
     * that class lives.
     */
    default boolean appLoaded() {
        return false;
    }
}
