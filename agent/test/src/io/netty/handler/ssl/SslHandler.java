package io.netty.handler.ssl;

import io.netty.channel.ChannelHandler;

/**
 * A stand-in for Netty's SslHandler (task aj4): a channel whose
 * pipeline holds one (or a subclass) speaks TLS, so its HTTP events
 * say scheme https. Does nothing.
 */
public class SslHandler implements ChannelHandler {
    public SslHandler() {}
}
