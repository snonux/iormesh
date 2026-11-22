package io.netty.handler.codec.http;

import io.netty.channel.ChannelHandlerContext;
import io.netty.channel.ChannelPromise;

/**
 * A stand-in for Netty 4.2's HttpObjectEncoder (task 6j4): the codec
 * class the agent's Netty probes find their loader by, with the write the
 * agent rewrites (HttpTransformerTest). Older Netty 4.1 releases have no
 * write of their own; the tests model those by not rewriting it.
 */
public class HttpObjectEncoder {
    public HttpObjectEncoder() {}

    public void write(ChannelHandlerContext ctx, Object msg, ChannelPromise promise) {
        // nothing to encode: only the method's shape matters
    }
}
