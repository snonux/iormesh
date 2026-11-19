package io.netty.handler.codec.http;

/**
 * A stand-in for Netty's HttpVersion (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public final class HttpVersion {
    public static final HttpVersion HTTP_1_1 = new HttpVersion();

    public String text() {
        return "HTTP/1.1";
    }
}
