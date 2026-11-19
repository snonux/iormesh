package iormesh.agent.http;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import io.netty.channel.Channel;
import io.netty.channel.ChannelHandlerContext;
import io.netty.handler.codec.http.HttpHeaders;
import io.netty.handler.codec.http.HttpMethod;
import io.netty.handler.codec.http.HttpRequest;
import io.netty.handler.codec.http.HttpResponse;
import io.netty.handler.codec.http.HttpResponseStatus;
import io.netty.handler.codec.http.HttpVersion;
import io.netty.handler.codec.http.LastHttpContent;
import iormesh.agent.AgentTests;
import java.net.InetSocketAddress;
import java.net.SocketAddress;
import java.util.ArrayList;
import java.util.List;

/**
 * The Netty probes' per-channel pairing (task de4 review) on stand-ins of
 * Netty's types (agent/test/src/io/netty): pipelined requests pair with
 * their answers in order, an interim 1xx completes nothing, a decode that
 * throws still counts its output, a close-delimited response completes in
 * decodeLast — and every way a message may have gone unseen (a codec older
 * than the agent, a failed write, an answer without its request, too many
 * unanswered requests, two completions in one call) poisons the channel:
 * nothing more is recorded on it.
 */
public final class NettyPairingTest {
    private NettyPairingTest() {}

    /** A full request (head and last content). */
    record Req(String uri) implements HttpRequest, LastHttpContent {
        public HttpMethod method() { return new HttpMethod("GET"); }
        public HttpVersion protocolVersion() { return HttpVersion.HTTP_1_1; }
        public HttpHeaders headers() { return new HttpHeaders().add("Host", "h:80"); }
    }

    /** A response head, full (with its last content) or not. */
    record Resp(int code) implements HttpResponse {
        public HttpResponseStatus status() { return new HttpResponseStatus(code); }
        public HttpVersion protocolVersion() { return HttpVersion.HTTP_1_1; }
        public HttpHeaders headers() { return new HttpHeaders().add("Content-Length", "2"); }
    }

    record FullResp(int code) implements HttpResponse, LastHttpContent {
        public HttpResponseStatus status() { return new HttpResponseStatus(code); }
        public HttpVersion protocolVersion() { return HttpVersion.HTTP_1_1; }
        public HttpHeaders headers() { return new HttpHeaders().add("Content-Length", "2"); }
    }

    record Last() implements LastHttpContent {}

    record Ch() implements Channel {
        public SocketAddress localAddress() { return new InetSocketAddress("127.0.0.1", 80); }
        public SocketAddress remoteAddress() { return new InetSocketAddress("127.0.0.1", 4000); }
    }

    record Ctx(Channel channel) implements ChannelHandlerContext {}

    /** One channel with its codecs and the probes, as the agent wires them. */
    static final class Wire {
        final Netty.Channels channels = new Netty.Channels();
        final Netty.Seen decode = new Netty.Seen(channels, true, false);
        final Netty.Seen decodeLast = new Netty.Seen(channels, true, true);
        final Netty.Seen write = new Netty.Seen(channels, false, false);
        final Object decoder = new Object();
        final Object encoder = new Object();
        final Ctx ctx = new Ctx(new Ch());

        Wire(boolean born) {
            if (born) {
                channels.born.apply(new Object[] {null, decoder, null, null, 0, false}, null);
                channels.born.apply(new Object[] {null, encoder, null, null, 0, false}, null);
            }
        }

        /** decode's exit with these messages in its output; the path recorded, or null. */
        String in(Object ret, Object... msgs) {
            return path(decode.apply(args(ret, decoder, new ArrayList<>(List.of(msgs))), meta()));
        }

        String last(Object... msgs) {
            return path(decodeLast.apply(args(null, decoder, new ArrayList<>(List.of(msgs))),
                    meta()));
        }

        String out(Object ret, Object msg) {
            return path(write.apply(args(ret, encoder, msg), meta()));
        }

        private Object[] args(Object ret, Object codec, Object second) {
            return new Object[] {ret, codec, new Object[] {ctx, second}, null, 0, false};
        }

        private static long[] meta() {
            return new long[] {0, 0, 0, 0, 0, -1, -1, 0};
        }

        private static String path(String[] s) {
            return s == null ? null : s[3];
        }
    }

    public static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("netty/pairing in order", NettyPairingTest::order),
                new AgentTests.Case("netty/poisoned channels", NettyPairingTest::poison));
    }

    /** Pipelining, 1xx, a throwing decode's output, decodeLast, the client side. */
    static void order() {
        Wire w = new Wire(true);
        equal(null, w.in(null, new Req("/a"), new Req("/b")), "two requests decoded at once");
        equal(null, w.out(null, new FullResp(100)), "100 Continue completes nothing");
        equal("/a", w.out(null, new FullResp(200)), "first answer, first request");
        equal(null, w.in(new IllegalStateException("x"), new Req("/c")),
                "a throwing decode still passes its output on");
        equal("/b", w.out(null, new FullResp(200)), "second answer");
        equal("/c", w.out(null, new FullResp(404)), "third answer");
        Wire client = new Wire(true);
        equal(null, client.out(null, new Req("/d")), "a client's request");
        equal(null, client.in(null, new Resp(200)), "its response head");
        equal("/d", client.last(new Resp(200), new Last()), "closed: decodeLast completes it");
        equal(null, client.last(new Last()), "a last content seen twice completes nothing");
    }

    /** Every unseen-message case poisons its channel; failures throw (a lost record). */
    static void poison() {
        Wire old = new Wire(false);
        old.in(null, new Req("/a"));
        equal(null, old.out(null, new FullResp(200)), "a codec older than the agent");
        throwsThenSilent(w -> w.out(new IllegalStateException("x"), new FullResp(200)),
                "a failed write");
        throwsThenSilent(w -> w.out(null, new FullResp(200)), "an answer without its request");
        throwsThenSilent(w -> {
            for (int i = 0; i <= Netty.Channels.MAX_PENDING; i++) {
                w.in(null, new Req("/" + i));
            }
            return null;
        }, "too many unanswered requests");
        Wire two = new Wire(true);
        two.out(null, new Req("/a"));
        two.out(null, new Req("/b"));
        try {
            two.in(null, new FullResp(200), new FullResp(200));
            throw new AssertionError("two completions in one call did not throw");
        } catch (IllegalStateException e) {
            check(e.getMessage().contains("two"), "two completions: " + e.getMessage());
        }
    }

    /** step throws on a fresh wire; afterwards a well-paired exchange records nothing. */
    private static void throwsThenSilent(java.util.function.Function<Wire, String> step,
                                         String what) {
        Wire w = new Wire(true);
        try {
            step.apply(w);
            throw new AssertionError(what + " did not throw");
        } catch (IllegalStateException e) {
            // expected: Hooks counts it as a lost record
        }
        w.in(null, new Req("/y"));
        equal(null, w.out(null, new FullResp(200)), what + ": the channel is poisoned");
    }
}
