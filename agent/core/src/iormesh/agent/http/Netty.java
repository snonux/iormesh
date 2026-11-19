package iormesh.agent.http;

import java.lang.invoke.MethodHandle;
import java.net.SocketAddress;
import java.util.ArrayDeque;
import java.util.List;
import java.util.Map;
import java.util.WeakHashMap;
import java.util.function.BiFunction;

/**
 * Netty 4.1/4.2's HTTP/1.x codec (decision #22's v1 set, task de4): one
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
 * both), on the event loop, with the channel's socket ends. An interim
 * 1xx answer (100 Continue) completes nothing. The pending exchanges are
 * kept per channel, weakly, and the map's lock is held only to find a
 * channel's queue — never across the library's code.
 *
 * The event's stack is the event loop's: Netty's codec, and the app's
 * handler only when it wrote from inside its read. HTTP/2 (Netty's
 * Http2 codecs) is not seen. A write that throws is a lost record; the
 * channel's pending exchanges are dropped with it (their order is gone).
 * A decode that throws decoded nothing and changes nothing.
 */
public final class Netty implements Library {
    private static final String P = "io/netty/handler/codec/http/";
    private static final String CTX = "Lio/netty/channel/ChannelHandlerContext;";

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
        return List.of(
                new Target(P + "HttpObjectDecoder", "decode",
                        "(" + CTX + "Lio/netty/buffer/ByteBuf;Ljava/util/List;)V", 0,
                        new Seen(channels, true)).withSecond(2),
                new Target(P + "HttpObjectEncoder", "write",
                        "(" + CTX + "Ljava/lang/Object;Lio/netty/channel/ChannelPromise;)V", 0,
                        new Seen(channels, false)).withSecond(1));
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

    /**
     * The pending exchanges of every channel, oldest first. Locks are held
     * only to find or change a queue, never across the library's code.
     */
    static final class Channels {
        /** Exchanges kept per channel; more means answers never come — a lost record. */
        static final int MAX_PENDING = 64;
        private final Map<Object, ArrayDeque<Pending>> byChannel = new WeakHashMap<>();

        ArrayDeque<Pending> of(Object channel) {
            synchronized (byChannel) {
                return byChannel.computeIfAbsent(channel, k -> new ArrayDeque<>());
            }
        }

        void drop(Object channel) {
            synchronized (byChannel) {
                byChannel.remove(channel);
            }
        }
    }

    /**
     * decode's exit (inbound: a[2] = {ctx, out list}) or write's
     * (outbound: {ctx, message}); see the class comment.
     */
    static final class Seen implements BiFunction<Object[], long[], String[]> {
        private final Channels channels;
        private final boolean inbound;
        private final ClassValue<Api> apis = Api.perClass();

        Seen(Channels channels, boolean inbound) {
            this.channels = channels;
            this.inbound = inbound;
        }

        @Override
        public String[] apply(Object[] a, long[] meta) {
            if (!(a[2] instanceof Object[] pair) || pair[0] == null || pair[1] == null) {
                return null;
            }
            try {
                Api api = apis.get(pair[0].getClass());
                Object channel = Handles.call(api.channel(), pair[0]);
                if (channel == null) {
                    return null;
                }
                if (a[0] instanceof Throwable) {
                    return failed(channel);
                }
                String[] out = null;
                for (Object msg : inbound ? (List<?>) pair[1] : List.of(pair[1])) {
                    String[] r = msg == null ? null : message(api, channel, msg, meta);
                    out = r != null ? r : out;
                }
                return out;
            } catch (RuntimeException | Error e) {
                throw e;
            } catch (Throwable t) {
                throw new IllegalStateException(t);   // counted as a lost record by Hooks
            }
        }

        /**
         * The hooked call threw. A decode that throws decoded nothing (the
         * channel's pending exchanges still pair with their answers); a
         * write that throws may have lost an answer, so the channel's
         * exchanges are dropped and the loss counted (Hooks counts a
         * throwing probe as a lost record: numbering stops, never shifts).
         */
        private String[] failed(Object channel) {
            if (inbound) {
                return null;
            }
            channels.drop(channel);
            throw new IllegalStateException("an HTTP write failed: exchanges lost");
        }

        /**
         * One message through its channel's exchanges: a request opens one
         * (inbound: a server's, stamped now), an answer's head goes to the
         * oldest one it answers, and the answer's last content completes
         * it — the record returned. An answer travels the other way than
         * its request.
         */
        private String[] message(Api api, Object channel, Object msg, long[] meta)
                throws Throwable {
            Class<?> c = msg.getClass();
            ArrayDeque<Pending> q = channels.of(channel);
            if (api.isRequest(c)) {
                open(api.request(msg, inbound), q, channel);
            } else if (api.isResponse(c)) {
                Answer ans = api.answer(msg);
                synchronized (q) {
                    Pending p = q.peekFirst();
                    if (ans != null && p != null && p.answer == null && p.r.server != inbound) {
                        p.answer = ans;
                    }
                }
            }
            if (!api.isLast(c)) {
                return null;
            }
            Pending done;
            synchronized (q) {
                Pending p = q.peekFirst();
                done = p != null && p.answer != null && p.r.server != inbound ? q.pollFirst() : null;
            }
            return done == null ? null : api.record(done, channel, meta);
        }

        private void open(Request r, ArrayDeque<Pending> q, Object channel) {
            boolean full;
            synchronized (q) {
                full = q.size() >= Channels.MAX_PENDING;
                if (!full) {
                    q.addLast(new Pending(r));
                }
            }
            if (full) {
                channels.drop(channel);
                throw new IllegalStateException("too many unanswered HTTP requests on a channel");
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
