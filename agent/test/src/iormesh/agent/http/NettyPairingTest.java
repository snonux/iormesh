package iormesh.agent.http;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import io.netty.channel.Channel;
import io.netty.channel.ChannelHandlerContext;
import io.netty.handler.codec.http.HttpHeaders;
import io.netty.handler.codec.http.HttpMethod;
import io.netty.handler.codec.http.HttpObjectDecoder;
import io.netty.handler.codec.http.HttpObjectEncoder;
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
import java.util.function.BiFunction;

/**
 * The Netty probes' per-channel pairing (task de4 review) on stand-ins of
 * Netty's types (agent/test/src/io/netty): pipelined requests pair with
 * their answers in order, an interim 1xx completes nothing, a decode that
 * throws still counts its output, a close-delimited response completes in
 * decodeLast — and every way a message may have gone unseen (a codec older
 * than the agent, a failed write, an answer without its request, too many
 * unanswered requests, two completions in one call) poisons the channel:
 * nothing more is recorded on it. A loader whose Netty is only half
 * rewritten (older 4.1: no encoder write, task 6j4) records nothing and
 * never throws.
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

    /**
     * One channel with its codecs (the stand-ins' subclasses, as Netty's
     * HttpServerCodec has) and the probes of Netty's own targets, as the
     * agent wires them: the methods named in rewritten are reported
     * rewritten in the stand-ins' loader, as HttpTransformer does.
     */
    static final class Wire {
        final Netty netty = new Netty();
        final Object decoder = new HttpObjectDecoder() {};
        final Object encoder = new HttpObjectEncoder() {};
        final Ctx ctx = new Ctx(new Ch());
        private BiFunction<Object[], long[], String[]> decode;
        private BiFunction<Object[], long[], String[]> decodeLast;
        private BiFunction<Object[], long[], String[]> write;
        private BiFunction<Object[], long[], String[]> born;

        /** Fully hooked: decode and write rewritten (Netty 4.2). */
        Wire(boolean born) {
            this(born, "decode", "decodeLast", "write");
        }

        Wire(boolean born, String... rewritten) {
            ClassLoader loader = HttpObjectDecoder.class.getClassLoader();
            for (Target t : netty.targets()) {
                if (List.of(rewritten).contains(t.method())) {
                    netty.rewritten(loader, t);
                }
                switch (t.method()) {
                    case "decode" -> decode = t.probe();
                    case "decodeLast" -> decodeLast = t.probe();
                    case "write" -> write = t.probe();
                    default -> this.born = t.probe();
                }
            }
            if (born) {
                this.born.apply(new Object[] {null, decoder, null, null, 0, false}, null);
                this.born.apply(new Object[] {null, encoder, null, null, 0, false}, null);
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

        /** write's exit; a recorded request is never held (Ring.HTTP_HELD, task mb4 review). */
        String out(Object ret, Object msg) {
            long[] m = meta();
            String p = path(write.apply(args(ret, encoder, msg), m));
            check(p == null || (m[Request.M_FLAGS] & 16) == 0, "a Netty request is never held");
            return p;
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
                new AgentTests.Case("netty/poisoned channels", NettyPairingTest::poison),
                new AgentTests.Case("netty/half-hooked loader records nothing",
                        NettyPairingTest::halfHooked));
    }

    /**
     * Older Netty 4.1 (task 6j4: 4.1.52–4.1.79) has no
     * HttpObjectEncoder.write of its own, so only the decoder is
     * rewritten: its probes must neither throw (a lost record, which turns
     * off the JVM's request numbering) nor record. A client's response
     * then finds no request, a server's requests are never answered.
     */
    static void halfHooked() {
        Wire client = new Wire(true, "decode", "decodeLast");
        equal(null, client.in(null, new Resp(200)), "a client's response head");
        equal(null, client.last(new Resp(200), new Last()), "its last content");
        Wire server = new Wire(true, "decode", "decodeLast");
        for (int i = 0; i <= 2 * Netty.Channels.MAX_PENDING; i++) {
            equal(null, server.in(null, new Req("/" + i)), "a server's request " + i);
        }
        Wire encoderOnly = new Wire(true, "write");
        equal(null, encoderOnly.out(null, new FullResp(200)), "an answer, the decoder unhooked");
        Wire late = new Wire(true, "decode");
        late.in(null, new Req("/early"));
        late.netty.rewritten(HttpObjectDecoder.class.getClassLoader(), late.netty.targets()
                .stream().filter(t -> t.method().equals("write")).findFirst().orElseThrow());
        equal(null, late.out(null, new FullResp(200)),
                "hooked after a request went unseen: the channel stays silent");
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
