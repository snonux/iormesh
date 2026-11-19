package iormesh.agent.http;

import java.lang.invoke.MethodHandle;
import java.lang.invoke.MethodHandles;
import java.lang.invoke.MethodType;
import java.util.ArrayList;
import java.util.Enumeration;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/**
 * Reflective access to a third-party library's public API (task de4):
 * the agent is built without the libraries, and one JVM may hold several
 * copies of one (a servlet container's web apps), so a probe finds the
 * API in the class of the object it was handed and keeps it per class
 * (a ClassValue — as long as the class lives, never pinning its loader).
 *
 * Public methods of public types only, through the public lookup: a
 * MethodHandle resolves just the method named (Class.getMethod would
 * resolve the types of every public method, loading classes the app may
 * never load), and nothing is made accessible. Each handle is typed with
 * Object for every reference and boxed primitive, so the probes call
 * them with invokeExact through {@link #call}.
 */
final class Handles {
    private static final MethodHandles.Lookup PUBLIC = MethodHandles.publicLookup();

    private Handles() {}

    /**
     * The type named name among c, its superclasses and every interface
     * they implement, or null: only classes c is linked against, so
     * nothing is loaded.
     */
    static Class<?> find(Class<?> c, String name) {
        for (Class<?> k = c; k != null; k = k.getSuperclass()) {
            if (k.getName().equals(name)) {
                return k;
            }
            for (Class<?> i : k.getInterfaces()) {
                Class<?> found = find(i, name);
                if (found != null) {
                    return found;
                }
            }
        }
        return null;
    }

    /**
     * The public instance method name(params) returning ret of owner, typed
     * (Object, Object...)Object; null when owner has none (an older API
     * version) or it cannot be resolved.
     */
    static MethodHandle virtual(Class<?> owner, String name, Class<?> ret, Class<?>... params) {
        if (owner == null || ret == null) {
            return null;
        }
        try {
            MethodHandle h = PUBLIC.findVirtual(owner, name, MethodType.methodType(ret, params));
            return h.asType(MethodType.genericMethodType(params.length + 1));
        } catch (ReflectiveOperationException | RuntimeException e) {
            return null;
        }
    }

    /**
     * The type named name in the loader of the API type near (an enum a
     * method returns, say): loaded, never initialized; null if absent.
     */
    static Class<?> sibling(Class<?> near, String name) {
        try {
            return near == null ? null : Class.forName(name, false, near.getClassLoader());
        } catch (ClassNotFoundException | LinkageError e) {
            return null;
        }
    }

    /** h(self), or null when h is (a method the API lacks). */
    static Object call(MethodHandle h, Object self) throws Throwable {
        return h == null ? null : (Object) h.invokeExact(self);
    }

    /** h(self, arg), or null when h is. */
    static Object call(MethodHandle h, Object self, Object arg) throws Throwable {
        return h == null ? null : (Object) h.invokeExact(self, arg);
    }

    /** A String result, or null. */
    static String string(MethodHandle h, Object self) throws Throwable {
        return call(h, self) instanceof String s ? s : null;
    }

    /** A number result, or dflt when h is null or returned none. */
    static long number(MethodHandle h, Object self, long dflt) throws Throwable {
        return call(h, self) instanceof Number n ? n.longValue() : dflt;
    }

    /**
     * Headers as name → values, read through a names() handle (an
     * Enumeration or Iterable of names) and a values(name) one (likewise):
     * the servlet API's getHeaderNames/getHeaders shapes. Loops, not
     * lambdas: a lambda's first call links classes inside the request.
     */
    static Map<String, List<String>> headers(MethodHandle names, MethodHandle values, Object self)
            throws Throwable {
        Map<String, List<String>> out = new LinkedHashMap<>();
        for (Object n : list(call(names, self))) {
            if (n instanceof String name && !out.containsKey(name)) {
                List<String> vs = new ArrayList<>();
                for (Object v : list(call(values, self, name))) {
                    if (v instanceof String s) {
                        vs.add(s);
                    }
                }
                out.put(name, vs);
            }
        }
        return out;
    }

    /** An Enumeration's or Iterable's elements, bounded; none for anything else. */
    private static List<Object> list(Object o) {
        List<Object> out = new ArrayList<>();
        if (o instanceof Enumeration<?> e) {
            while (e.hasMoreElements() && out.size() < Request.MAX_HEADERS * 4) {
                out.add(e.nextElement());
            }
        } else if (o instanceof Iterable<?> it) {
            for (Object x : it) {
                if (out.size() == Request.MAX_HEADERS * 4) {
                    break;
                }
                out.add(x);
            }
        }
        return out;
    }
}
