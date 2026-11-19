package jakarta.servlet.http;

import jakarta.servlet.ServletResponse;
import java.util.Collection;

/** A stand-in for the Servlet API's HttpServletResponse (see ServletRequest). */
public interface HttpServletResponse extends ServletResponse {
    int getStatus();

    Collection<String> getHeaderNames();

    Collection<String> getHeaders(String name);

    String getHeader(String name);
}
