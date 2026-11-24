package iormesh.workload.scenarios;

import com.sun.net.httpserver.Headers;
import java.util.LinkedHashMap;
import java.util.Map;

/**
 * One declared HTTP request and the reply the server gives it. The HTTP
 * scenarios send their calls in list order on one HTTP/1.1 connection, so
 * the n-th call is request n of that connection on both ends (the http
 * event's req_seq, docs/milestones.md "Request pairing across the wire"). Both
 * ends check the exchange against the declaration: a request that reaches the
 * server without its planted header is a failed run, not a pass that
 * proves nothing about redaction. Public, with its factories, for the
 * OTEL flavor's separate module (workload/otel), which declares its own
 * calls with the trace headers they must carry.
 *
 * @param method  request method
 * @param target  raw path and query, e.g. "/redact/login?token=…"
 * @param body    request body, "" for none
 * @param headers request headers the client sets and the server checks
 * @param status  reply status
 * @param reply   reply body, "" for none
 * @param replyHeaders reply headers the server sets and the client checks
 */
public record HttpCall(String method, String target, String body, Map<String, String> headers,
        int status, String reply, Map<String, String> replyHeaders) {

    public static HttpCall get(String target, int status, String reply) {
        return new HttpCall("GET", target, "", Map.of(), status, reply, Map.of());
    }

    public static HttpCall post(String target, String body, int status, String reply) {
        return new HttpCall("POST", target, body, Map.of(), status, reply, Map.of());
    }

    public HttpCall withHeader(String name, String value) {
        return new HttpCall(method, target, body, plus(headers, name, value), status, reply,
                replyHeaders);
    }

    HttpCall withReplyHeader(String name, String value) {
        return new HttpCall(method, target, body, headers, status, reply,
                plus(replyHeaders, name, value));
    }

    /**
     * What the server got that this call did not declare, or null when the
     * request matches: method, raw target, body and every declared header.
     */
    String mismatch(String gotMethod, String gotTarget, Headers got, String gotBody) {
        if (!method.equals(gotMethod) || !target.equals(gotTarget)) {
            return "want " + method + " " + where() + ", got " + gotMethod + " "
                    + pathOf(gotTarget) + " (or another query)";
        }
        if (!body.equals(gotBody)) {
            return where() + ": body differs";
        }
        for (Map.Entry<String, String> h : headers.entrySet()) {
            if (!h.getValue().equals(got.getFirst(h.getKey()))) {
                // Never print the value: it may be a planted secret.
                return where() + ": header " + h.getKey() + " missing or different";
            }
        }
        return null;
    }

    /**
     * The target without its query, for messages: a query may carry a
     * planted secret (http-redaction), and stderr, though not the trace,
     * is still a log.
     */
    public String where() {
        return pathOf(target);
    }

    private static String pathOf(String target) {
        int q = target.indexOf('?');
        return q < 0 ? target : target.substring(0, q);
    }

    // Insertion order kept, so headers go out in their declared order.
    private static Map<String, String> plus(Map<String, String> m, String k, String v) {
        Map<String, String> out = new LinkedHashMap<>(m);
        out.put(k, v);
        return out;
    }
}
