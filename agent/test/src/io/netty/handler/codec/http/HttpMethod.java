package io.netty.handler.codec.http;

/**
 * A stand-in for Netty's HttpMethod (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public final class HttpMethod {
    private final String name;

    public HttpMethod(String name) {
        this.name = name;
    }

    public String name() {
        return name;
    }
}
