package iormesh.agent.http;

import java.util.Collections;
import java.util.Map;
import java.util.WeakHashMap;

/**
 * What a library's probes remember about the library's own objects — a
 * response's socket address, an exchange already recorded — without
 * keeping those objects alive. Keyed by the objects' hashCode/equals,
 * which none of the hooked classes override, so by identity.
 */
final class Weak<V> {
    private final Map<Object, V> map = Collections.synchronizedMap(new WeakHashMap<>());

    void put(Object key, V value) {
        if (key != null) {
            map.put(key, value);
        }
    }

    /** The value bound to key, removed: each binding is used once. */
    V take(Object key) {
        return key == null ? null : map.remove(key);
    }

    V get(Object key) {
        return key == null ? null : map.get(key);
    }
}
