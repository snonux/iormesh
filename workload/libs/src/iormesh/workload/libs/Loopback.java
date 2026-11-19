package iormesh.workload.libs;

import iormesh.workload.scenarios.HttpCall;
import iormesh.workload.scenarios.HttpServe;
import java.io.IOException;
import java.net.InetAddress;
import java.net.URI;
import java.util.List;

/**
 * What the client-library scenarios of this module share (http-apache,
 * http-okhttp): the JDK's HttpServer on 127.0.0.1 answering their calls
 * — the core scenarios' HttpServe, whose events the agent already
 * records — and the base URI their clients send to.
 */
final class Loopback {
    private Loopback() {}

    /** HttpServe on 127.0.0.1:port (0 = kernel-picked), answering calls. */
    static HttpServe serve(int port, List<HttpCall> calls) throws IOException {
        // A literal address, never a lookup: no resolver I/O in the bracket.
        InetAddress loopback = InetAddress.getByAddress(new byte[] {127, 0, 0, 1});
        return HttpServe.start(loopback, port, calls, null, (n, request) -> () -> { });
    }

    /** http://127.0.0.1:port/ */
    static URI base(int port) {
        return URI.create("http://127.0.0.1:" + port + "/");
    }
}
