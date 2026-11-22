package io.netty.handler.codec.http;

import io.netty.buffer.ByteBuf;
import io.netty.channel.ChannelHandlerContext;
import java.util.List;

/**
 * A stand-in for Netty's HttpObjectDecoder (task 6j4): the codec class
 * the agent's Netty probes find their loader by, with the decode the
 * agent rewrites (HttpTransformerTest). Decodes nothing.
 */
public class HttpObjectDecoder {
    public HttpObjectDecoder() {}

    protected void decode(ChannelHandlerContext ctx, ByteBuf buffer, List<Object> out) {
        // nothing to decode: only the method's shape matters
    }
}
