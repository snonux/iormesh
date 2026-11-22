package javax.servlet.http;

import java.util.Enumeration;
import javax.servlet.ServletRequest;

/** A stand-in for the javax Servlet API's HttpServletRequest (see ServletRequest). */
public interface HttpServletRequest extends ServletRequest {
    String getMethod();

    String getRequestURI();

    String getQueryString();

    Enumeration<String> getHeaderNames();

    Enumeration<String> getHeaders(String name);

    String getHeader(String name);
}
