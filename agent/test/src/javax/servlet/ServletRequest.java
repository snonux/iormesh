package javax.servlet;

/**
 * A stand-in for the javax Servlet API's ServletRequest (task de4 review),
 * in its Servlet 2.5 shape — no getDispatcherType, no async — so the
 * agent's javax target and its no-dispatcher-type path are tested. The
 * same test request implements the jakarta stand-in too.
 */
public interface ServletRequest {
    String getScheme();

    String getProtocol();

    String getLocalAddr();

    int getLocalPort();

    String getRemoteAddr();

    int getRemotePort();

    long getContentLengthLong();
}
