package iormesh.workload.libs;

import io.netty.bootstrap.ServerBootstrap;
import io.netty.buffer.Unpooled;
import io.netty.channel.Channel;
import io.netty.channel.ChannelHandler;
import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelInitializer;
import io.netty.channel.EventLoopGroup;
import io.netty.channel.MultiThreadIoEventLoopGroup;
import io.netty.channel.SimpleChannelInboundHandler;
import io.netty.channel.nio.NioIoHandler;
import io.netty.channel.socket.SocketChannel;
import io.netty.channel.socket.nio.NioServerSocketChannel;
import io.netty.handler.codec.http.DefaultFullHttpResponse;
import io.netty.handler.codec.http.FullHttpRequest;
import io.netty.handler.codec.http.FullHttpResponse;
import io.netty.handler.codec.http.HttpHeaderNames;
import io.netty.handler.codec.http.HttpObjectAggregator;
import io.netty.handler.codec.http.HttpResponseStatus;
import io.netty.handler.codec.http.HttpServerCodec;
import io.netty.handler.codec.http.HttpVersion;
import iormesh.workload.scenarios.HttpCall;
import java.io.IOException;
import java.net.InetAddress;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * The server end of http-netty: Netty (vendored, workload/libs/
 * third_party/netty) with its HTTP/1.1 codec on 127.0.0.1 — one acceptor
 * event loop, one worker event loop, so the golden can name both threads
 * — and one handler that answers exactly the declared calls, in order,
 * like the core scenarios' HttpServe. The handler answers from inside its
 * read, on the worker, so the agent's event (at the encoder's write) comes
 * before the reply's write (at the flush).
 */
final class NettyServe implements AutoCloseable {
    private final EventLoopGroup boss;
    private final EventLoopGroup worker;
    private final Channel channel;
    private final Answer answer;

    private NettyServe(EventLoopGroup boss, EventLoopGroup worker, Channel channel,
                       Answer answer) {
        this.boss = boss;
        this.worker = worker;
        this.channel = channel;
        this.answer = answer;
    }

    /** Binds 127.0.0.1:port (0 = kernel-picked) and starts serving calls. */
    static NettyServe start(int port, List<HttpCall> calls) throws Exception {
        EventLoopGroup boss = new MultiThreadIoEventLoopGroup(1, NioIoHandler.newFactory());
        EventLoopGroup worker = new MultiThreadIoEventLoopGroup(1, NioIoHandler.newFactory());
        Answer answer = new Answer(calls);
        try {
            ServerBootstrap b = new ServerBootstrap().group(boss, worker)
                    .channel(NioServerSocketChannel.class)
                    .childHandler(new ChannelInitializer<SocketChannel>() {
                        @Override
                        protected void initChannel(SocketChannel ch) {
                            ch.pipeline().addLast(new HttpServerCodec(),
                                    new HttpObjectAggregator(1 << 16), answer);
                        }
                    });
            // A literal address, never a lookup: no resolver I/O in the bracket.
            InetAddress loopback = InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
            Channel ch = b.bind(new InetSocketAddress(loopback, port)).sync().channel();
            return new NettyServe(boss, worker, ch, answer);
        } catch (Exception | Error e) {
            boss.shutdownGracefully(0, 1, TimeUnit.SECONDS);
            worker.shutdownGracefully(0, 1, TimeUnit.SECONDS);
            throw e;
        }
    }

    int port() {
        return ((InetSocketAddress) channel.localAddress()).getPort();
    }

    /** Waits, without a bound, until every declared call was answered. */
    void awaitAll() throws Exception {
        answer.done.await();
        answer.check();
    }

    /** awaitAll with a bound, for --self-test where both ends share a JVM. */
    void awaitAll(long seconds) throws Exception {
        if (!answer.done.await(seconds, TimeUnit.SECONDS)) {
            throw new IOException("server answered " + answer.next.get() + " of "
                    + answer.calls.size() + " requests");
        }
        answer.check();
    }

    /** Closes the listener and stops both event loops. */
    @Override
    public void close() throws IOException {
        try {
            channel.close().sync();
            boss.shutdownGracefully(0, 1, TimeUnit.SECONDS).sync();
            worker.shutdownGracefully(0, 1, TimeUnit.SECONDS).sync();
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException("interrupted stopping the server", e);
        }
    }

    /**
     * The handler: request n gets call n, as HttpServe's. A request the
     * declaration does not expect gets 500 and fails the run; the latch is
     * then released in full. Counted down after the reply is flushed, so
     * the owner never stops the server with a reply unsent.
     */
    @ChannelHandler.Sharable
    static final class Answer extends SimpleChannelInboundHandler<FullHttpRequest> {
        final List<HttpCall> calls;
        final AtomicInteger next = new AtomicInteger();
        final CountDownLatch done;
        private volatile String failure;

        Answer(List<HttpCall> calls) {
            this.calls = calls;
            this.done = new CountDownLatch(calls.size());
        }

        @Override
        protected void channelRead0(ChannelHandlerContext ctx, FullHttpRequest req) {
            int n = next.getAndIncrement();
            HttpCall call = n < calls.size() ? calls.get(n) : null;
            String problem = call == null ? "unexpected request " + (n + 1) : mismatch(call, req);
            FullHttpResponse resp = problem == null ? reply(call) : new DefaultFullHttpResponse(
                    HttpVersion.HTTP_1_1, HttpResponseStatus.INTERNAL_SERVER_ERROR);
            resp.headers().setInt(HttpHeaderNames.CONTENT_LENGTH, resp.content().readableBytes());
            failure = problem != null ? problem : failure;
            ctx.writeAndFlush(resp).addListener(f -> {
                done.countDown();
                while (failure != null && done.getCount() > 0) {
                    done.countDown();
                }
            });
        }

        void check() throws IOException {
            if (failure != null) {
                throw new IOException("server: " + failure);
            }
        }

        /** What the request got wrong, or null; never prints a header's value. */
        private static String mismatch(HttpCall c, FullHttpRequest req) {
            if (!c.method().equals(req.method().name()) || !c.target().equals(req.uri())) {
                return "want " + c.method() + " " + c.where() + ", got " + req.method()
                        + " (another target)";
            }
            if (!c.body().equals(req.content().toString(StandardCharsets.UTF_8))) {
                return c.where() + ": body differs";
            }
            for (Map.Entry<String, String> h : c.headers().entrySet()) {
                if (!h.getValue().equals(req.headers().get(h.getKey()))) {
                    return c.where() + ": header " + h.getKey() + " missing or different";
                }
            }
            return null;
        }

        private static FullHttpResponse reply(HttpCall call) {
            FullHttpResponse resp = new DefaultFullHttpResponse(HttpVersion.HTTP_1_1,
                    HttpResponseStatus.valueOf(call.status()),
                    Unpooled.copiedBuffer(call.reply(), StandardCharsets.UTF_8));
            for (Map.Entry<String, String> h : call.replyHeaders().entrySet()) {
                resp.headers().add(h.getKey(), h.getValue());
            }
            return resp;
        }
    }
}
