package io.netty.channel;

/**
 * A stand-in for Netty's ChannelHandlerContext (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public interface ChannelHandlerContext {
    Channel channel();
}
