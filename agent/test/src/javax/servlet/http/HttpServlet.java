package javax.servlet.http;

import javax.servlet.ServletRequest;
import javax.servlet.ServletResponse;

/**
 * A stand-in for the javax Servlet API's HttpServlet (see ServletRequest),
 * shaped like the jakarta stand-in: what the servlet does is the body it
 * is handed.
 */
public class HttpServlet {
    /** What the dispatched service does; it may throw. */
    public interface Body {
        void run(HttpServletRequest req, HttpServletResponse resp) throws Exception;
    }

    private final Body body;

    public HttpServlet(Body body) {
        this.body = body;
    }

    public void service(ServletRequest req, ServletResponse res) throws Exception {
        HttpServletRequest request = (HttpServletRequest) req;
        HttpServletResponse response = (HttpServletResponse) res;
        service(request, response);
    }

    protected void service(HttpServletRequest req, HttpServletResponse resp) throws Exception {
        body.run(req, resp);
    }
}
