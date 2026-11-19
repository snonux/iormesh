package iormesh.agent.http;

import java.lang.invoke.MethodHandle;
import java.net.SocketAddress;
import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.WeakHashMap;
import java.util.function.BiFunction;

/**
 * Netty 4.2's HTTP/1.x codec (decision #22's v1 set, task de4): one
 * http event per request a Netty server answered or a Netty client sent
 * through netty-codec-http (HttpServerCodec, HttpClientCodec, or their
 * decoder and encoder alone), whatever the app's handlers do between.
 *
 * Netty has no call that spans a request: a request is decoded from the
 * channel on its event loop, the app's handlers answer it whenever they
 * like, and the answer is encoded on the same event loop. So two hooks,
 * paired per channel in order (HTTP/1.1 answers in request order, also
 * when pipelined):
 * <ul>
 *   <li>{@code HttpObjectDecoder.decode} — each HTTP message it decoded
 *       (in its output list at its exit) opens a pending exchange on the
 *       channel when it begins one (a server's request, stamped then: the
 *       latency's start), or completes the channel's oldest when it is
 *       the answer (a client's response);
 *   <li>{@code HttpObjectEncoder.write} — each message the app writes,
 *       likewise: a client's request opens one, a server's response
 *       completes the oldest.
 * </ul>
 * The answer's head (status, headers) is kept with its exchange, which
 * is recorded when the answer's last content passes (a full message is
 * both; a close-delimited response ends in decodeLast, hooked too), on
 * the event loop, with the channel's socket ends. An interim 1xx answer
 * (100 Continue) completes nothing. The pending exchanges are kept per
 * channel, weakly, and the locks are held only to touch an entry — never
 * across the library's code.
 *
 * Pairing in order is only sound while no message of the channel went
 * unseen (review of de4), so a channel is poisoned — nothing more is
 * recorded on it — when its codec predates the agent (a live attach: the
 * codec constructors mark the ones made since), when a probe fails, when
 * a write throws, when an answer finds no request of its own, when 64
 * requests wait unanswered, or when one call completes two exchanges
 * (Hooks writes one record per call). Each of those but the first throws:
 * Hooks counts a lost record, so the tracer numbers no more of the JVM's
 * requests. A decode that throws still passes its output on, so its
 * messages count.
 *
 * The event's stack is the event loop's: Netty's codec, and the app's
 * handler only when it wrote from inside its read. HTTP/2 (Netty's
 * Http2 codecs) is not seen.
 */
public final class Netty implements Library {
    private static final String P = "io/netty/handler/codec/http/";
    private static final String CTX = "Lio/netty/channel/ChannelHandlerContext;";
    private static final String BUF = "Lio/netty/buffer/ByteBuf;Ljava/util/List;";
    /** HttpObjectDecoder's constructors in 4.2 (the delegating ones too: marking twice is harmless). */
    private static final List<String> DECODER_INITS = List.of("()V", "(IIIZ)V", "(IIIZZ)V",
            "(IIIZZI)V", "(IIIZZIZ)V", "(IIIZZIZZ)V",
            "(Lio/netty/handler/codec/http/HttpDecoderConfig;)V");

    @Override
    public String name() {
        return "netty-http";
    }

    @Override
    public String module() {
        return null;
    }

    @Override
    public List<String> opens() {
        return List.of();
    }

    @Override
    public boolean appLoaded() {
        return true;
    }

    @Override
    public List<Target> targets() {
        Channels channels = new Channels();
        List<Target> out = new ArrayList<>(List.of(
                new Target(P + "HttpObjectDecoder", "decode", "(" + CTX + BUF + ")V", 0,
                        new Seen(channels, true, false)).withSecond(2),
                new Target(P + "HttpObjectDecoder", "decodeLast", "(" + CTX + BUF + ")V", 0,
                        new Seen(channels, true, true)).withSecond(2),
                new Target(P + "HttpObjectEncoder", "write",
                        "(" + CTX + "Ljava/lang/Object;Lio/netty/channel/ChannelPromise;)V", 0,
                        new Seen(channels, false, false)).withSecond(1),
                new Target(P + "HttpObjectEncoder", "<init>", "()V", Target.NO_ARG,
                        channels.born)));
        for (String d : DECODER_INITS) {
            out.add(new Target(P + "HttpObjectDecoder", "<init>", d, Target.NO_ARG,
                    channels.born));
        }
        return out;
    }

    /** One exchange on a channel: its request, then its answer's head. */
    static final class Pending {
        final Request r;
        Answer answer;

        Pending(Request r) {
            this.r = r;
        }
    }

    /** An answer's head: status, headers, declared length. */
    record Answer(int status, Map<String, List<String>> headers, String length) {}

    /** A channel's pending exchanges, oldest first, or poisoned: nothing more recorded. */
    static final class State {
        final ArrayDeque<Pending> q = new ArrayDeque<>();
        boolean poisoned;
    }

    /**
     * Every channel's State, and the codecs made since the agent was
     * installed (born). Weak both; the locks are held only to find or
     * change an entry, never across the library's code.
     */
    static final class Channels {
        /** Exchanges kept per channel; more means answers never come. */
        static final int MAX_PENDING = 64;
        private final Map<Object, State> byChannel = new WeakHashMap<>();
        private final Map<Object, Boolean> codecs = new WeakHashMap<>();
        /** A codec constructor's exit: the codec saw its channel from the start. */
        final BiFunction<Object[], long[], String[]> born = (a, meta) -> {
            if (a[1] != null) {
                synchronized (codecs) {
                    codecs.put(a[1], Boolean.TRUE);
                }
            }
            return null;
        };

        State of(Object channel) {
            synchronized (byChannel) {
                State st = byChannel.get(channel);
                if (st == null) {
                    st = new State();
                    byChannel.put(channel, st);
                }
                return st;
            }
        }

        boolean born(Object codec) {
            synchronized (codecs) {
                return codecs.containsKey(codec);
            }
        }

        /** Records nothing more on st's channel: its exchanges' order is gone. */
        static void poison(State st) {
            synchronized (st) {
                st.poisoned = true;
                st.q.clear();
            }
        }
    }

    /**
     * decode's or decodeLast's exit (inbound: a[2] = {ctx, out list}) or
     * write's (outbound: {ctx, message}); see the class comment.
     */
    static final class Seen implements BiFunction<Object[], long[], String[]> {
        private final Channels channels;
        private final boolean inbound;
        /**
         * decodeLast: its decode (hooked, already seen) ran inside it on
         * the same output list, and it may add one last content after —
         * so only the list's last element counts, and only to complete.
         * A last content seen twice completes nothing the second time: the
         * oldest exchange then has no answer yet.
         */
        private final boolean lastOnly;
        private final ClassValue<Api> apis = Api.perClass();

        Seen(Channels channels, boolean inbound, boolean lastOnly) {
            this.channels = channels;
            this.inbound = inbound;
            this.lastOnly = lastOnly;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            if (!(a[2] instanceof Object[] pair) || pair[0] == null || pair[1] == null) {
                return null;
            }
            State st = null;
            try {
                Api api = apis.get(pair[0].getClass());
                Object channel = Handles.call(api.channel(), pair[0]);
                if (channel == null) {
                    return null;
                }
                st = channels.of(channel);
                if (!channels.born(a[1])) {
                    Channels.poison(st);   // a codec older than the agent: history unknown
                }
                return st.poisoned ? null : seen(a, pair[1], api, channel, st, meta);
            } catch (Throwable t) {
                if (st != null) {
                    Channels.poison(st);
                }
                throw t instanceof RuntimeException e ? e : new IllegalStateException(t);
            }
        }

        /**
         * The messages of one call. A decode passes its output on even when
         * it throws (ByteToMessageDecoder fires it in a finally), so they
         * count either way; a write that throws may have lost an answer.
         * One call may complete one exchange: Hooks writes one record per
         * call, so a second completion is a lost record.
         */
        private String[] seen(Object[] a, Object msgs, Api api, Object channel, State st,
                              long[] meta) throws Throwable {
            if (!inbound && a[0] instanceof Throwable) {
                throw new IllegalStateException("an HTTP write failed: exchanges lost");
            }
            if (lastOnly) {
                List<?> l = (List<?>) msgs;
                Object m = l.isEmpty() ? null : l.get(l.size() - 1);
                return m == null || !api.isLast(m.getClass()) ? null
                        : complete(api, channel, st, meta);
            }
            String[] out = null;
            for (Object msg : inbound ? (List<?>) msgs : List.of(msgs)) {
                String[] r = msg == null ? null : message(api, channel, msg, st, meta);
                if (r != null && out != null) {
                    throw new IllegalStateException("two HTTP exchanges completed in one call");
                }
                out = r != null ? r : out;
            }
            return out;
        }

        /**
         * One message through its channel's exchanges: a request opens one
         * (inbound: a server's, stamped now), an answer's head goes to the
         * oldest one, and the answer's last content completes it — the
         * record returned. An answer travels the other way than its
         * request; one that finds no request of its own means a request
         * went unseen, which throws (the channel is then poisoned).
         */
        private String[] message(Api api, Object channel, Object msg, State st, long[] meta)
                throws Throwable {
            Class<?> c = msg.getClass();
            if (api.isRequest(c)) {
                open(api.request(msg, inbound), st);
            } else if (api.isResponse(c)) {
                answer(api.answer(msg), st);
            }
            return api.isLast(c) ? complete(api, channel, st, meta) : null;
        }

        /** An answer's last content: the oldest exchange's record, if it was answered. */
        private String[] complete(Api api, Object channel, State st, long[] meta)
                throws Throwable {
            Pending done;
            synchronized (st) {
                Pending p = st.q.peekFirst();
                done = p != null && p.answer != null && p.r.server != inbound ? st.q.pollFirst()
                        : null;
            }
            return done == null ? null : api.record(done, channel, meta);
        }

        private void answer(Answer ans, State st) {
            if (ans == null) {
                return;   // interim 1xx
            }
            synchronized (st) {
                Pending p = st.q.peekFirst();
                if (p == null || p.answer != null || p.r.server == inbound) {
                    throw new IllegalStateException("an HTTP answer without its request");
                }
                p.answer = ans;
            }
        }

        private static void open(Request r, State st) {
            synchronized (st) {
                if (st.q.size() >= Channels.MAX_PENDING) {
                    throw new IllegalStateException("too many unanswered HTTP requests");
                }
                st.q.addLast(new Pending(r));
            }
        }
    }

    /** netty-codec-http's and netty-transport's API, in one loader. */
    record Api(MethodHandle channel, MethodHandle localAddr, MethodHandle remoteAddr,
               MethodHandle method, MethodHandle methodName, MethodHandle uri,
               MethodHandle version, MethodHandle versionText, MethodHandle headers,
               MethodHandle names, MethodHandle values, MethodHandle status,
               MethodHandle code, ClassValue<int[]> kinds) {
        private static final String H = "io.netty.handler.codec.http.";

        static ClassValue<Api> perClass() {
            return new ClassValue<>() {
                @Override
                protected Api computeValue(Class<?> c) {
                    return of(c);
                }
            };
        }

        static Api of(Class<?> near) {
            Class<?> ch = Handles.sibling(near, "io.netty.channel.Channel");
            Class<?> req = Handles.sibling(near, H + "HttpRequest");
            Class<?> msg = Handles.sibling(near, H + "HttpMessage");
            Class<?> hdrs = Handles.sibling(near, H + "HttpHeaders");
            return new Api(Handles.virtual(Handles.sibling(near,
                            "io.netty.channel.ChannelHandlerContext"), "channel", ch),
                    Handles.virtual(ch, "localAddress", SocketAddress.class),
                    Handles.virtual(ch, "remoteAddress", SocketAddress.class),
                    Handles.virtual(req, "method", H + "HttpMethod"),
                    Handles.virtual(Handles.sibling(near, H + "HttpMethod"), "name", String.class),
                    Handles.virtual(req, "uri", String.class),
                    Handles.virtual(msg, "protocolVersion", H + "HttpVersion"),
                    Handles.virtual(Handles.sibling(near, H + "HttpVersion"), "text", String.class),
                    Handles.virtual(msg, "headers", hdrs),
                    Handles.virtual(hdrs, "names", java.util.Set.class),
                    Handles.virtual(hdrs, "getAll", List.class, String.class),
                    Handles.virtual(Handles.sibling(near, H + "HttpResponse"), "status",
                            H + "HttpResponseStatus"),
                    Handles.virtual(Handles.sibling(near, H + "HttpResponseStatus"), "code",
                            int.class),
                    newKinds());
        }

        /** Per message class: is it a request, a response, a last content? */
        private static ClassValue<int[]> newKinds() {
            return new ClassValue<>() {
                @Override
                protected int[] computeValue(Class<?> c) {
                    return new int[] {Handles.find(c, H + "HttpRequest") != null ? 1 : 0,
                            Handles.find(c, H + "HttpResponse") != null ? 1 : 0,
                            Handles.find(c, H + "LastHttpContent") != null ? 1 : 0};
                }
            };
        }

        boolean isRequest(Class<?> c) {
            return kinds.get(c)[0] == 1;
        }

        boolean isResponse(Class<?> c) {
            return kinds.get(c)[1] == 1;
        }

        boolean isLast(Class<?> c) {
            return kinds.get(c)[2] == 1;
        }

        /**
         * A request's head as its exchange begins: a server's (inbound)
         * stamped now, its latency's start; a client's host from its Host
         * header.
         */
        Request request(Object msg, boolean inbound) throws Throwable {
            Request r = new Request();
            r.server = inbound;
            r.start = System.nanoTime();
            r.method = nz(Handles.string(methodName, Handles.call(method, msg)));
            r.scheme = "http";
            r.path = nz(Handles.string(uri, msg));
            r.version = nz(Handles.string(versionText, Handles.call(version, msg)));
            Map<String, List<String>> h = Handles.headers(names, values, Handles.call(headers, msg));
            r.requestHeaders(h);
            if (!inbound) {
                r.host = hostOf(Request.first(h, "Host"));
            }
            String cl = Request.first(h, "Content-Length");
            r.reqBytes = cl == null && Request.first(h, "Transfer-Encoding") == null ? 0
                    : Request.parseLength(cl);
            return r;
        }

        /** An answer's head; null for an interim 1xx (100 Continue), which completes nothing. */
        Answer answer(Object msg) throws Throwable {
            int s = (int) Handles.number(code, Handles.call(status, msg), 0);
            if (s >= 100 && s < 200 && s != 101) {
                return null;
            }
            Map<String, List<String>> h = Handles.headers(names, values, Handles.call(headers, msg));
            return new Answer(s, h, Request.first(h, "Content-Length"));
        }

        /** The completed exchange's record, with the channel's socket ends. */
        String[] record(Pending p, Object channel, long[] meta) throws Throwable {
            Request r = p.r;
            r.status = p.answer.status();
            r.responseHeaders(p.answer.headers());
            r.respBytes = Request.declaredBody(r.method, r.status, p.answer.length());
            r.local = Request.addr((SocketAddress) Handles.call(localAddr, channel));
            r.remote = Request.addr((SocketAddress) Handles.call(remoteAddr, channel));
            return r.toRecord(meta);
        }

        /** A Host header's host, without its port ("[::1]" keeps its brackets off). */
        static String hostOf(String host) {
            if (host == null) {
                return "";
            }
            if (host.startsWith("[")) {
                int end = host.indexOf(']');
                return end < 0 ? host : host.substring(1, end);
            }
            int colon = host.lastIndexOf(':');
            return colon < 0 ? host : host.substring(0, colon);
        }

        private static String nz(String s) {
            return s == null ? "" : s;
        }
    }
}
