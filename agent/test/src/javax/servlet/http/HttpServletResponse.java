package javax.servlet.http;

import java.util.Collection;
import javax.servlet.ServletResponse;

/** A stand-in for the javax Servlet API's HttpServletResponse (see ServletRequest). */
public interface HttpServletResponse extends ServletResponse {
    int getStatus();

    Collection<String> getHeaderNames();

    Collection<String> getHeaders(String name);

    String getHeader(String name);
}
