package io.netty.handler.codec.http;

/**
 * A stand-in for Netty's HttpResponseStatus (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public final class HttpResponseStatus {
    private final int code;

    public HttpResponseStatus(int code) {
        this.code = code;
    }

    public int code() {
        return code;
    }
}
