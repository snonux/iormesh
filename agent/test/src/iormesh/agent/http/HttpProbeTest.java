package iormesh.agent.http;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import java.net.InetSocketAddress;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpHeaders;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import javax.net.ssl.SSLSession;

/**
 * The redaction policy, the address forms, and what the HttpClient probe
 * makes of a response (a fake; AgentSmoke and the integration tests run
 * every probe against the real libraries, whose internals the server and
 * HttpURLConnection probes reflect on).
 */
public final class HttpProbeTest {
    private HttpProbeTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("http/redaction masks", HttpProbeTest::redaction),
                new AgentTests.Case("http/addresses and literals", HttpProbeTest::addresses),
                new AgentTests.Case("http/client response", HttpProbeTest::client),
                new AgentTests.Case("http/declared body sizes", HttpProbeTest::sizes),
                new AgentTests.Case("http/sendAsync completion", HttpProbeTest::async),
                new AgentTests.Case("http/dns mappings", HttpProbeTest::dns));
    }

    static void redaction() {
        equal("/login?user=***&token=***", Redaction.url("/login?user=alice&token=s3cret"), "query");
        equal("/a;jsessionid=***#frag", Redaction.url("/a;jsessionid=x#frag"), "matrix, fragment");
        equal("https://bob:***@h/x?q=***", Redaction.url("https://bob:pw@h/x?q=1"), "userinfo");
        equal("https://***@h/", Redaction.url("https://TOKEN@h/"), "lone userinfo");
        equal("https://u:***@h/x", Redaction.url("https://u:p/ss@h/x"), "'/' in a password");
        equal("https://h:8080/x?mail=***", Redaction.url("https://h:8080/x?mail=a@b"),
                "a port is no password");
        equal("location: /next?token=***", Redaction.header("Location", "/next?token=x"),
                "relative Location");
        equal("/plain", Redaction.url("/plain"), "no query");
        // A query component without '=' is a value with no name (task xp4).
        equal("/x?***&b=***;***#frag", Redaction.url("/x?tok&b=1;tok#frag"), "bare query value");
        equal("/a;v1#frag?tok", Redaction.url("/a;v1#frag?tok"), "bare matrix, fragment");
        equal("authorization: ***", Redaction.header("Authorization", "Bearer x"), "denied");
        equal("x-api-token: ***", Redaction.header("X-Api-Token", "t"), "glob *token*");
        equal("set-cookie: ***", Redaction.header("Set-Cookie", "sid=1"), "set-cookie");
        equal("x_session.id: ***", Redaction.header("X_Session.Id", "1"), "separators folded");
        equal("accept: */*", Redaction.header("Accept", "*/*"), "allowed");
        equal("referer: http://h/p?a=***", Redaction.header("Referer", "http://h/p?a=1"),
                "URL in an allowed value");
        check(Redaction.glob("a*b?c", "axxbyc") && !Redaction.glob("a*b", "ac"), "glob");
        credentialHeaders();
        identityHeaders();
        credentialValues();
        urlHeaders();
    }

    /**
     * Task np4: JWT and identity-assertion headers are masked by name, a
     * JWT or auth-scheme credential value by its shape under any name, and
     * look-alike values are not (the same tables as internal/redact's
     * TestHeaderDeniesIdentityHeaders, TestHeaderMasksCredentialValues and
     * TestHeaderKeepsOrdinaryValues).
     */
    static void identityHeaders() {
        for (String n : List.of("X-JWT-Assertion", "Cf-Access-Jwt-Assertion",
                "X-Goog-IAP-JWT-Assertion", "X-Amzn-Oidc-Data", "X-Amzn-Oidc-Identity", "X-Jwt",
                "X-Auth", "X-Upstream-Auth", "Authentication", "Authentication-Info",
                "X-Userinfo", "X-Ms-Client-Principal", "X-Ms-Client-Principal-Name", "X-Csrf",
                "X-Xsrf", "X-Otp")) {
            String lower = n.toLowerCase(java.util.Locale.ROOT);
            equal(lower + ": ***", Redaction.header(n, "S3CRET"), "identity header " + n);
        }
        for (String n : List.of("WWW-Authenticate", "Accept-Encoding", "X-Forwarded-Host")) {
            String lower = n.toLowerCase(java.util.Locale.ROOT);
            equal(lower + ": plain", Redaction.header(n, "plain"), "ordinary header " + n);
        }
    }

    /** See identityHeaders. */
    static void credentialValues() {
        String jwt = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.c2lnbmF0dXJl";
        for (String v : List.of(jwt, " " + jwt + " ", "eyJhbGciOiJub25lIn0.eyJzdWIiOiIxMjMifQ.",
                "eyJhbGciOiJSU0EtT0FFUCJ9.a.b.c.d", "jwt=" + jwt + "; Path=/", "Bearer abc.def",
                "bearer abc", "Basic dXNlcjpwYXNz", "Digest username=\"u\", response=\"r\"",
                "Negotiate YIIGhgYJKoZI", "Bearer\u00a0abc", "Basic\u3000abc",
                "\u00a0Bearer abc", "Bearer\u2028abc")) {
            equal("x-custom: ***", Redaction.header("X-Custom", v), "credential value " + v);
        }
        for (String v : List.of("1.2.3", "a.b.c", "eyJ", "eyJ.x", "eyJhbGci..x", "Bearer",
                "Basic ", "Basically fine", "Negotiated rate", "text/html; q=0.9",
                "www.example.com", "eyJhbGci.eyJzdWIi.sig.x", "Bearer\u001cabc",
                "Basic\u001fabc")) {
            equal("x-custom: " + v, Redaction.header("X-Custom", v), "ordinary value " + v);
        }
    }

    /**
     * Task 0j4: prefixed and API-key style credential headers are masked,
     * ordinary headers are not (the same tables as internal/redact's
     * TestHeaderDeniesCredentialHeaders and TestHeaderKeepsOrdinaryHeaders).
     */
    static void credentialHeaders() {
        for (String n : List.of("X-Authorization", "X-Forwarded-Authorization",
                "X-Original-Authorization", "X-Amzn-Remapped-Authorization", "Cookie2",
                "X-Auth-Key", "X-Access-Key", "X-Amz-Credential", "Ocp-Apim-Subscription-Key",
                "X-Functions-Key", "Private-Key", "X-Passwd", "X-Api-Key", "Api-Key",
                "X-Goog-Api-Key", "X-Bearer", "X-Passphrase")) {
            String lower = n.toLowerCase(java.util.Locale.ROOT);
            equal(lower + ": ***", Redaction.header(n, "S3CRET"), "credential header " + n);
        }
        for (String n : List.of("Content-Type", "Accept", "User-Agent", "Host", "X-Request-Id",
                "Keep-Alive", "Cache-Control", "X-Forwarded-For", "X-Forwarded-Proto",
                "Sec-WebSocket-Key", "Content-Length")) {
            String lower = n.toLowerCase(java.util.Locale.ROOT);
            equal(lower + ": plain", Redaction.header(n, "plain"), "ordinary header " + n);
        }
    }

    /**
     * Headers holding a URI reference are masked as a URL even when
     * relative: forward-auth proxies copy the original request target,
     * query secrets and all, into X-Original-URI and the like.
     */
    static void urlHeaders() {
        equal("x-original-uri: /login?token=***",
                Redaction.header("X-Original-URI", "/login?token=s"), "x-original-uri");
        equal("x-original-url: /a?b=***", Redaction.header("X-Original-URL", "/a?b=s"),
                "x-original-url");
        equal("x-forwarded-uri: /reset?code=***&user=***",
                Redaction.header("X-Forwarded-URI", "/reset?code=s&user=bob"), "x-forwarded-uri");
        equal("x-rewrite-url: /a?b=***", Redaction.header("X-Rewrite-URL", "/a?b=s"),
                "x-rewrite-url");
        equal("x-envoy-original-path: /a?b=***",
                Redaction.header("X-Envoy-Original-Path", "/a?b=s"), "x-envoy-original-path");
        equal("x-forwarded-prefix: /api", Redaction.header("X-Forwarded-Prefix", "/api"),
                "x-forwarded-prefix stays");
    }

    static void addresses() throws Exception {
        equal("127.0.0.1:80", Request.addr(new InetSocketAddress("127.0.0.1", 80)), "v4");
        equal("[0:0:0:0:0:0:0:1]:443", Request.addr(new InetSocketAddress("::1", 443)), "v6");
        equal("", Request.addr(InetSocketAddress.createUnresolved("h", 1)), "unresolved");
        equal("", Request.addr(new InetSocketAddress(0)), "wildcard");
        equal("10.1.2.3:8080", Request.literal("10.1.2.3", 8080), "IPv4 literal");
        equal("[0:0:0:0:0:0:0:1]:8080", Request.literal("[::1]", 8080), "IPv6 literal");
        equal("", Request.literal("cafe", 80), "a hex-looking name is no literal");
        equal("", Request.literal("example.org", 80), "a name is never resolved");
        check(Request.token("x-api-token") && !Request.token("GET /a HTTP/1.1")
                && !Request.token(null), "header names");
    }

    static void sizes() {
        equal(7L, Request.declaredBody("GET", 200, " 7 "), "Content-Length");
        equal(-1L, Request.declaredBody("GET", 200, null), "none: unknown (chunked)");
        equal(-1L, Request.declaredBody("GET", 200, "7x"), "malformed: unknown");
        equal(-1L, Request.declaredBody("GET", 200, "9".repeat(19)), "too long: unknown");
        equal(0L, Request.declaredBody("HEAD", 200, "7"), "HEAD has no body");
        equal(0L, Request.declaredBody("GET", 204, null), "204 has none");
        equal(0L, Request.declaredBody("GET", 304, "7"), "304 has none");
        equal("2", Request.first(Map.of("content-LENGTH", List.of("2")), "Content-Length"),
                "header found by any case");
        long[] meta = {0, 0, 0, 0, 9, 0, 0};
        new Request().toRecord(meta);
        equal(0L, meta[4], "a start not adopted is cleared");
        equal(-1L, meta[5], "sizes unknown by default");
    }

    /**
     * sendAsync: the caller's identity is taken at the return, the request
     * described only when the future completes, through late, with its
     * hook id, marked unordered and without a start (review of wd4); a
     * failed future records nothing; one done at the return, or completing
     * inline while the completion is registered, is recorded at once.
     */
    static void async() {
        List<long[]> lates = new java.util.ArrayList<>();
        List<String[]> described = new java.util.ArrayList<>();
        long[] meta = {0, 0, 0, 0, 0, -1, -1};
        Async a = new Async(() -> new long[] {1, 2, 3, 4, 5, 6}, (c, f) -> {
            lates.add(c);
            described.add(f.apply(meta));
        });
        JdkHttpClient.SentAsync probe = new JdkHttpClient.SentAsync(new Weak<>(), a);
        Object self = new Object();
        java.util.concurrent.CompletableFuture<HttpResponse<String>> ok =
                new java.util.concurrent.CompletableFuture<>();
        check(probe.apply(new Object[] {ok, self, null, self, 7}, new long[] {0, 0, 0, 0, 42, -1, -1})
                == null, "nothing recorded at the return");
        equal(0, lates.size(), "nothing late yet");
        ok.complete(new FakeResponse(null));
        equal(List.of(1L, 2L, 3L, 4L, 5L, 6L, 7L), java.util.Arrays.stream(lates.get(0)).boxed()
                .toList(), "caller identity and hook id");
        equal("/a?k=***", described.get(0)[3], "the response described");
        equal(0L, meta[4], "no start: the stamp follows the app's callbacks");
        equal(10L, meta[2], "unordered (2), java.net.http (8), a client's");
        java.util.concurrent.CompletableFuture<HttpResponse<String>> bad =
                new java.util.concurrent.CompletableFuture<>();
        probe.apply(new Object[] {bad, self, null, self, 7}, new long[7]);
        bad.completeExceptionally(new java.io.IOException("refused"));
        equal(1, lates.size(), "a failed future records nothing");
        long[] now = {0, 0, 0, 0, 42, -1, -1};
        String[] s = probe.apply(new Object[] {java.util.concurrent.CompletableFuture
                .completedFuture(new FakeResponse(null)), self, null, self, 7}, now);
        check(s != null && now[4] == 0 && now[2] == 10, "done at the return: recorded at once");
        // Completed before whenComplete although isDone said no: the
        // completion runs inline on this thread and the probe records it.
        java.util.concurrent.CompletableFuture<HttpResponse<String>> racy =
                new java.util.concurrent.CompletableFuture<>() {
                    @Override public boolean isDone() { return false; }
                };
        racy.complete(new FakeResponse(null));
        long[] inline = new long[7];
        check(probe.apply(new Object[] {racy, self, null, self, 7}, inline) != null
                && inline[2] == 10, "inline completion recorded by the probe");
        equal(1, lates.size(), "and not handed to late");
    }

    /**
     * The DNS probe: a new mapping is recorded once, a rotation of the
     * same addresses is no change, other addresses are; literals, an
     * empty host and a failed lookup record nothing.
     */
    static void dns() throws Exception {
        JdkDns.Resolved probe = new JdkDns.Resolved();
        java.net.InetAddress a = java.net.InetAddress.getByAddress("h", new byte[] {10, 0, 0, 1});
        java.net.InetAddress b = java.net.InetAddress.getByAddress("h", new byte[] {10, 0, 0, 2});
        long[] meta = new long[7];
        equal(List.of("h", "10.0.0.1", "10.0.0.2"), List.of(probe.apply(
                new Object[] {new java.net.InetAddress[] {a, b}, null, "h"}, meta)), "new mapping");
        equal(null, probe.apply(new Object[] {new java.net.InetAddress[] {b, a}, null, "h"}, meta),
                "a rotation is no change");
        check(probe.apply(new Object[] {new java.net.InetAddress[] {b}, null, "h"}, meta) != null,
                "other addresses are");
        equal(null, probe.apply(new Object[] {null, null, "x"}, meta), "a failed lookup");
        equal(null, probe.apply(new Object[] {new java.net.InetAddress[] {a}, null, "10.0.0.1"},
                meta), "an IPv4 literal");
        check(JdkDns.literal("::1") && !JdkDns.literal("cafe") && !JdkDns.literal("a1.b"),
                "literals");
        equal(null, probe.apply(new Object[] {new java.net.InetAddress[] {a}, null, ""}, meta),
                "the empty host");
    }

    static void client() {
        long[] meta = {0, 0, 0, 0, 42, -1, -1};
        Object self = new Object();
        String[] s = new JdkHttpClient.Sent(new Weak<>()).apply(
                new Object[] {new FakeResponse(new FakeResponse(null)), self, null, self}, meta);
        equal(List.of("GET", "https", "127.0.0.1", "/a?k=***", "HTTP/2", "", "127.0.0.1:8443",
                "cookie: ***", "content-length: 5", "etag: 1"), List.of(s), "record strings");
        equal(24L, meta[2], "a client's request, java.net.http (8), held by its thread (16)");
        equal(2L, meta[3], "exchanges: the response and its previous one (a redirect)");
        equal(42L, meta[4], "send's own start kept");
        equal(0L, meta[5], "no request body");
        equal(5L, meta[6], "response Content-Length");
        long[] other = {0, 0, 0, 0, 42, -1, -1};
        new JdkHttpClient.Sent(new Weak<>()).apply(
                new Object[] {new FakeResponse(null), self, null, new Object()}, other);
        equal(0L, other[4], "another call's stamp is no start");
        check(new JdkHttpClient.Sent(new Weak<>()).apply(new Object[4], new long[7]) == null,
                "a send that threw records nothing");
    }

    /** A response of HttpClient.send over HTTP/2. */
    static final class FakeResponse implements HttpResponse<String> {
        private static final URI URI_ = URI.create("https://127.0.0.1:8443/a?k=v");
        private final HttpResponse<String> previous;

        FakeResponse(HttpResponse<String> previous) {
            this.previous = previous;
        }

        @Override public int statusCode() { return 200; }
        @Override public HttpRequest request() {
            return HttpRequest.newBuilder(URI_).header("Cookie", "c=1").build();
        }
        @Override public Optional<HttpResponse<String>> previousResponse() {
            return Optional.ofNullable(previous);
        }
        @Override public HttpHeaders headers() {
            return HttpHeaders.of(Map.of("ETag", List.of("1"), "Content-Length", List.of("5")),
                    (a, b) -> true);
        }
        @Override public String body() { return ""; }
        @Override public Optional<SSLSession> sslSession() { return Optional.empty(); }
        @Override public URI uri() { return URI_; }
        @Override public HttpClient.Version version() { return HttpClient.Version.HTTP_2; }
    }
}
