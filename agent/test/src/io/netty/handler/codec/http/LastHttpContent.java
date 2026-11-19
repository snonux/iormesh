package io.netty.handler.codec.http;

/**
 * A stand-in for Netty's LastHttpContent (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public interface LastHttpContent {
}
