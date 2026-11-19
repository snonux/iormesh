package iormesh.workload.libs;

import iormesh.workload.scenarios.HttpCall;
import java.io.IOException;
import java.net.URI;
import java.util.List;
import java.util.Map;
import okhttp3.MediaType;
import okhttp3.OkHttpClient;
import okhttp3.Protocol;
import okhttp3.Request;
import okhttp3.RequestBody;
import okhttp3.Response;

/**
 * http-okhttp's client end: OkHttp 5 (vendored, workload/libs/
 * third_party/okhttp) speaking HTTP/1.1, the calls one after another on
 * one keep-alive connection from its pool. Each call is executed
 * synchronously from {@link #call}; the agent's event is recorded when
 * OkHttp's interceptor chain hands the response back, before the body is
 * read here.
 */
final class OkFetch {
    private static final MediaType TEXT = MediaType.get("text/plain; charset=utf-8");

    private OkFetch() {}

    /** Sends calls in order to base (http://127.0.0.1:port/). */
    static void run(URI base, List<HttpCall> calls) throws IOException {
        OkHttpClient client = new OkHttpClient.Builder()
                .protocols(List.of(Protocol.HTTP_1_1))
                .build();
        try {
            for (HttpCall c : calls) {
                call(client, base, c);
            }
        } finally {
            // Closes the pooled connection before the end marker, so the
            // client's close is in the bracket, and lets the JVM exit.
            client.connectionPool().evictAll();
            client.dispatcher().executorService().shutdown();
        }
    }

    /** One declared call: send it, then check status, body and headers. */
    static void call(OkHttpClient client, URI base, HttpCall c) throws IOException {
        Request.Builder b = new Request.Builder().url(base.resolve(c.target()).toString());
        for (Map.Entry<String, String> h : c.headers().entrySet()) {
            b.addHeader(h.getKey(), h.getValue());
        }
        b.method(c.method(), c.body().isEmpty() ? null : RequestBody.create(c.body(), TEXT));
        try (Response r = client.newCall(b.build()).execute()) {
            String body = r.body().string();
            if (r.code() != c.status() || !body.equals(c.reply())) {
                throw new IOException(c.where() + ": want " + c.status() + " " + c.reply()
                        + ", got " + r.code() + " " + body);
            }
            for (Map.Entry<String, String> h : c.replyHeaders().entrySet()) {
                if (!r.headers(h.getKey()).contains(h.getValue())) {
                    // The value may be a planted secret: name the header only.
                    throw new IOException(c.where() + ": reply header " + h.getKey()
                            + " missing or different");
                }
            }
        }
    }
}
