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
                new AgentTests.Case("http/declared body sizes", HttpProbeTest::sizes));
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
        equal("authorization: ***", Redaction.header("Authorization", "Bearer x"), "denied");
        equal("x-api-token: ***", Redaction.header("X-Api-Token", "t"), "glob *token*");
        equal("set-cookie: ***", Redaction.header("Set-Cookie", "sid=1"), "set-cookie");
        equal("x_session.id: ***", Redaction.header("X_Session.Id", "1"), "separators folded");
        equal("accept: */*", Redaction.header("Accept", "*/*"), "allowed");
        equal("referer: http://h/p?a=***", Redaction.header("Referer", "http://h/p?a=1"),
                "URL in an allowed value");
        check(Redaction.glob("a*b?c", "axxbyc") && !Redaction.glob("a*b", "ac"), "glob");
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

    static void client() {
        long[] meta = {0, 0, 0, 0, 42, -1, -1};
        Object self = new Object();
        String[] s = new JdkHttpClient.Sent(new Weak<>()).apply(
                new Object[] {new FakeResponse(new FakeResponse(null)), self, null, self}, meta);
        equal(List.of("GET", "https", "127.0.0.1", "/a?k=***", "HTTP/2", "", "127.0.0.1:8443",
                "cookie: ***", "content-length: 5", "etag: 1"), List.of(s), "record strings");
        equal(0L, meta[2], "a client's request");
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
