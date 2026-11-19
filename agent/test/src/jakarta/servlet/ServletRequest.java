package jakarta.servlet;

/**
 * A stand-in for the Servlet API's ServletRequest (task de4), just the
 * getters the agent's servlet probe calls, so ServletHookTest needs no
 * container. The real API comes from the workload's vendored Tomcat.
 */
public interface ServletRequest {
    String getScheme();

    String getProtocol();

    String getLocalAddr();

    int getLocalPort();

    String getRemoteAddr();

    int getRemotePort();

    long getContentLengthLong();

    DispatcherType getDispatcherType();

    boolean isAsyncStarted();
}
