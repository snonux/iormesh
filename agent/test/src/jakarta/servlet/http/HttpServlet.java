package jakarta.servlet.http;

import jakarta.servlet.ServletRequest;
import jakarta.servlet.ServletResponse;

/**
 * A stand-in for the Servlet API's HttpServlet (see ServletRequest): the
 * public service the agent hooks casts and dispatches as the real one
 * does. Concrete, so ServletHookTest can load a rewritten copy alone;
 * what the servlet does is the body it is handed.
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
