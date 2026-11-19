package io.netty.channel;

import java.net.SocketAddress;

/**
 * A stand-in for Netty's Channel (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public interface Channel {
    SocketAddress localAddress();

    SocketAddress remoteAddress();
}
