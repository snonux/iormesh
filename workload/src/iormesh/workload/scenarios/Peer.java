package iormesh.workload.scenarios;

/**
 * A role running on its own platform thread inside --self-test. join()
 * rethrows the role's failure, so a broken peer fails the self-test
 * instead of dying silently on a background thread.
 */
final class Peer {
    /** A role body; may throw like Scenario.run. */
    @FunctionalInterface
    interface Body {
        void run() throws Exception;
    }

    private final Thread thread;
    private volatile Exception failure;

    private Peer(String name, Body body) {
        // A platform thread on purpose: M1's scenarios trace kernel TIDs,
        // and a virtual thread would hop carriers (that is M3's subject).
        this.thread = Thread.ofPlatform().name(name).unstarted(() -> {
            try {
                body.run();
            } catch (Exception e) {
                failure = e;
            }
        });
    }

    static Peer start(String name, Body body) {
        Peer p = new Peer(name, body);
        p.thread.start();
        return p;
    }

    void join() throws Exception {
        thread.join();
        if (failure != null) {
            throw new Exception(thread.getName() + " failed", failure);
        }
    }
}
