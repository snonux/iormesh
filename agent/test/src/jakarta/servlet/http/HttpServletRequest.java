package jakarta.servlet.http;

import jakarta.servlet.ServletRequest;
import java.util.Enumeration;

/** A stand-in for the Servlet API's HttpServletRequest (see ServletRequest). */
public interface HttpServletRequest extends ServletRequest {
    String getMethod();

    String getRequestURI();

    String getQueryString();

    Enumeration<String> getHeaderNames();

    Enumeration<String> getHeaders(String name);
}
