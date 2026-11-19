package iormesh.agent.http;

import java.util.ArrayList;

/**
 * What the exchanges of one client request leave for its event (task
 * de4), for a library whose request-level method and per-exchange method
 * are hooked apart (Apache HttpClient, OkHttp) and run on one thread: the
 * request-level method is entry-stamped, so an exchange's probe finds it
 * as the open hook (Hooks' a[3]) and fills that request's slot — the
 * exchanges counted, the last connection's socket ends, the version it
 * spoke — which the request's exit takes and clears, both ways out.
 *
 * A request is its owner (the client, the call) and its start stamp
 * together (review of de4): requests nest on one thread — an OkHttp
 * authenticator or interceptor making its own call, an Apache interceptor
 * calling the same client — and each open one keeps its own slot, the
 * innermost found by the stamp Hooks offers with the open hook. The slots
 * are per thread, at most MAX per thread, and a request's exit clears its
 * own and any left above it, so nothing of the app outlives the request.
 */
final class Slots {
    /** Requests open at once on a thread (Hooks' stamp depth). */
    static final int MAX = 8;

    /** One request's exchanges so far. */
    static final class Slot {
        final Object owner;
        final long start;
        int exchanges;
        String local = "";
        String remote = "";
        String version;

        Slot(Object owner, long start) {
            this.owner = owner;
            this.start = start;
        }
    }

    private final ThreadLocal<ArrayList<Slot>> slots = new ThreadLocal<>();
    private final ClassValue<Boolean> owners;

    /** Slots whose owners are instances of the class (or interface) named owner. */
    Slots(String owner) {
        owners = new ClassValue<>() {
            @Override
            protected Boolean computeValue(Class<?> c) {
                return Handles.find(c, owner) != null;
            }
        };
    }

    /**
     * Counts one exchange of the request open on this thread — open the
     * innermost entry-stamped hook's this, start its stamp — and returns
     * its slot to fill; null when the open hook is no request of this
     * library (an exchange made without it records nothing) or its stamp
     * is unknown.
     */
    Slot exchange(Object open, long start) {
        if (open == null || start == 0 || !owners.get(open.getClass())) {
            return null;
        }
        ArrayList<Slot> list = slots.get();
        if (list == null) {
            list = new ArrayList<>();
            slots.set(list);
        }
        Slot s = find(list, open, start);
        if (s == null) {
            if (list.size() == MAX) {
                return null;
            }
            s = new Slot(open, start);
            list.add(s);
        }
        s.exchanges++;
        return s;
    }

    /**
     * The slot of the request owner began at start on this thread, removed
     * with any left above it; null if none.
     */
    Slot take(Object owner, long start) {
        ArrayList<Slot> list = slots.get();
        Slot s = list == null ? null : find(list, owner, start);
        if (s != null) {
            list.subList(list.indexOf(s), list.size()).clear();
            if (list.isEmpty()) {
                slots.remove();
            }
        }
        return s;
    }

    private static Slot find(ArrayList<Slot> list, Object owner, long start) {
        for (Slot s : list) {
            if (s.owner == owner && s.start == start) {
                return s;
            }
        }
        return null;
    }

    /**
     * A request that failed records nothing — but if an exchange had
     * happened the server counted it, so this throws: the agent counts a
     * lost record and the tracer numbers no more of the JVM's requests
     * (never a shifted ordinal).
     */
    static void failed(Slot s) {
        if (s != null && s.exchanges > 0) {
            throw new IllegalStateException("request failed after " + s.exchanges
                    + " exchange(s)");
        }
    }

    /** Puts what the exchanges saw into r; without a slot r keeps no connection. */
    static void apply(Slot s, Request r) {
        if (s != null) {
            r.exchanges = s.exchanges;
            r.local = s.local;
            r.remote = s.remote;
            if (s.version != null) {
                r.version = s.version;
            }
        }
    }
}
