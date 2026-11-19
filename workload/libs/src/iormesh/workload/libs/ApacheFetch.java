package iormesh.workload.libs;

import iormesh.workload.scenarios.HttpCall;
import java.io.IOException;
import java.net.URI;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import org.apache.hc.client5.http.impl.classic.CloseableHttpClient;
import org.apache.hc.client5.http.impl.classic.HttpClients;
import org.apache.hc.client5.http.impl.io.PoolingHttpClientConnectionManager;
import org.apache.hc.client5.http.impl.io.PoolingHttpClientConnectionManagerBuilder;
import org.apache.hc.core5.http.ClassicHttpRequest;
import org.apache.hc.core5.http.ContentType;
import org.apache.hc.core5.http.Header;
import org.apache.hc.core5.http.ParseException;
import org.apache.hc.core5.http.io.entity.EntityUtils;
import org.apache.hc.core5.http.io.entity.StringEntity;
import org.apache.hc.core5.http.io.support.ClassicRequestBuilder;

/**
 * http-apache's client end: Apache HttpClient 5's classic client
 * (vendored, workload/libs/third_party/httpclient5) with a pool of one
 * connection, so the calls go out one after another on one keep-alive
 * connection. Each request is executed from {@link #call} through a
 * response handler, which reads and releases the reply; the agent's
 * event is recorded when the client hands the response to the handler.
 */
final class ApacheFetch {
    private ApacheFetch() {}

    /** One reply as the handler read it. */
    private record Reply(int status, String body, Header[] headers) {}

    /** Sends calls in order to base (http://127.0.0.1:port/). */
    static void run(URI base, List<HttpCall> calls) throws IOException {
        PoolingHttpClientConnectionManager pool = PoolingHttpClientConnectionManagerBuilder.create()
                .setMaxConnTotal(1)
                .setMaxConnPerRoute(1)
                .build();
        // close() shuts the pooled connection before the end marker, so
        // the client's close is in the bracket.
        try (CloseableHttpClient client = HttpClients.custom().setConnectionManager(pool).build()) {
            for (HttpCall c : calls) {
                call(client, base, c);
            }
        }
    }

    /** One declared call: send it, then check status, body and headers. */
    static void call(CloseableHttpClient client, URI base, HttpCall c) throws IOException {
        Reply r = client.execute(request(base, c), resp -> new Reply(resp.getCode(),
                body(resp.getEntity()), resp.getHeaders()));
        if (r.status() != c.status() || !r.body().equals(c.reply())) {
            throw new IOException(c.where() + ": want " + c.status() + " " + c.reply()
                    + ", got " + r.status() + " " + r.body());
        }
        for (Map.Entry<String, String> h : c.replyHeaders().entrySet()) {
            if (!has(r.headers(), h.getKey(), h.getValue())) {
                // The value may be a planted secret: name the header only.
                throw new IOException(c.where() + ": reply header " + h.getKey()
                        + " missing or different");
            }
        }
    }

    private static ClassicHttpRequest request(URI base, HttpCall c) {
        ClassicRequestBuilder b = ClassicRequestBuilder.create(c.method())
                .setUri(base.resolve(c.target()));
        for (Map.Entry<String, String> h : c.headers().entrySet()) {
            b.addHeader(h.getKey(), h.getValue());
        }
        if (!c.body().isEmpty()) {
            b.setEntity(new StringEntity(c.body(), ContentType.TEXT_PLAIN.withCharset(
                    StandardCharsets.UTF_8)));
        }
        return b.build();
    }

    private static String body(org.apache.hc.core5.http.HttpEntity e) throws IOException {
        try {
            return e == null ? "" : EntityUtils.toString(e, StandardCharsets.UTF_8);
        } catch (ParseException ex) {
            throw new IOException(ex);
        }
    }

    private static boolean has(Header[] hs, String name, String value) {
        for (Header h : hs) {
            if (h.getName().equalsIgnoreCase(name) && h.getValue().equals(value)) {
                return true;
            }
        }
        return false;
    }
}
