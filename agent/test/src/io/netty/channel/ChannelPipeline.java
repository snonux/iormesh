package io.netty.channel;

import java.util.Map;

/**
 * A stand-in for Netty's ChannelPipeline (task aj4): iterable over its
 * handlers by name, as Netty's is — all the agent's Netty probes use
 * of it to find an SslHandler — so NettyPairingTest needs no Netty.
 */
public interface ChannelPipeline extends Iterable<Map.Entry<String, ChannelHandler>> {}
