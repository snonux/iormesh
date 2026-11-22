package io.netty.channel;

/**
 * A stand-in for Netty's ChannelHandlerContext (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty: the
 * channel, and its pipeline (task aj4: TLS when it holds an SslHandler).
 */
public interface ChannelHandlerContext {
    Channel channel();

    ChannelPipeline pipeline();
}
