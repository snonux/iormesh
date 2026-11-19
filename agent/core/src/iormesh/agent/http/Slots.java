package iormesh.agent.http;

/**
 * What the exchanges of one client request leave for its event (task
 * de4), for a library whose request-level method and per-exchange method
 * are hooked apart (Apache HttpClient, OkHttp) and run on one thread: the
 * request-level method is entry-stamped, so an exchange's probe finds it
 * as the open hook (Hooks' a[3]) and fills that request's slot — the
 * exchanges counted, the last connection's socket ends, the version it
 * spoke — which the request's exit takes and clears, both ways out. The
 * slot is per thread and held only while the request runs, so nothing of
 * the app outlives the request here.
 */
final class Slots {
    /** One request's exchanges so far. */
    static final class Slot {
        final Object owner;
        int exchanges;
        String local = "";
        String remote = "";
        String version;

        Slot(Object owner) {
            this.owner = owner;
        }
    }

    private final ThreadLocal<Slot> slot = new ThreadLocal<>();
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
     * Counts one exchange of the request open on this thread and returns
     * its slot to fill; null when the open hook is no request of this
     * library (an exchange made without it records nothing).
     */
    Slot exchange(Object open) {
        if (open == null || !owners.get(open.getClass())) {
            return null;
        }
        Slot s = slot.get();
        if (s == null || s.owner != open) {
            s = new Slot(open);
            slot.set(s);
        }
        s.exchanges++;
        return s;
    }

    /** The slot of owner's request on this thread, removed; null if none. */
    Slot take(Object owner) {
        Slot s = slot.get();
        if (s == null || s.owner != owner) {
            return null;
        }
        slot.remove();
        return s;
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
