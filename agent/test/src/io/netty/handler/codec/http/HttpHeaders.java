package io.netty.handler.codec.http;

import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/**
 * A stand-in for Netty's HttpHeaders (task de4 review): just what the
 * agent's Netty probes call, so NettyPairingTest needs no Netty.
 */
public class HttpHeaders {
    private final Map<String, List<String>> map = new TreeMap<>();

    public HttpHeaders add(String name, String value) {
        map.put(name, List.of(value));
        return this;
    }

    public Set<String> names() {
        return map.keySet();
    }

    public List<String> getAll(String name) {
        return map.getOrDefault(name, List.of());
    }
}
