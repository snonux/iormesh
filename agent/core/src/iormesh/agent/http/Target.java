package iormesh.agent.http;

import java.util.function.BiFunction;

/**
 * One hooked method of an HTTP library: the rewritten method calls
 * iormesh.bridge.Hooks.http before each return and on its exceptional
 * exit, handing over its return value, this, and the parameter at index
 * arg ({@link #NO_ARG}: none, {@link #LAST_ARG}: the last one, whatever
 * the descriptor's length — the JDK drops leading parameters between
 * releases). The probe turns those into a recorded request, or returns
 * null. A constructor is hooked at its returns only: on its exceptional
 * path this may not be initialized.
 *
 * A stamped target (task wd4) is the method that begins its request — a
 * client's send, a server's dispatch: its entry also calls Hooks
 * .httpEnter, so the probes of its own exit and of the hooks it reaches
 * know when the request started (the latency). A constructor is never
 * stamped: its exceptional exit, which would pop the stamp, is not hooked.
 *
 * A marked target (sendAsync) writes a numbering-off marker at its entry
 * (see {@link #marked}).
 *
 * A DNS target (task wd4, decision #8) is a name lookup, not a request:
 * its exits call Hooks.dns instead, and its probe returns the host and
 * the addresses it resolved to. It may be static (no this: null is
 * passed).
 *
 * A target may name a second argument (arg2, task de4: a servlet's
 * request and response): both are then handed over as one Object[]
 * {first, second} in the arg position. {@link #NO_ARG} for none.
 *
 * owner is an internal name (sun/net/httpserver/HttpExchangeImpl);
 * descriptors are exact, so a JDK that changes a signature leaves the
 * row unmatched instead of hooking a different method. Several rows may
 * name one method with different descriptors, one per JDK generation.
 */
public record Target(String owner, String method, String descriptor, int arg, int arg2,
                     BiFunction<Object[], long[], String[]> probe, boolean stamp, boolean dns,
                     boolean mark) {
    /** No argument is passed (null). */
    public static final int NO_ARG = -1;
    /** The method's last parameter is passed. */
    public static final int LAST_ARG = -2;

    /** An exit-only target (no entry stamp). */
    public Target(String owner, String method, String descriptor, int arg,
                  BiFunction<Object[], long[], String[]> probe) {
        this(owner, method, descriptor, arg, NO_ARG, probe, false, false, false);
    }

    /** This target handing over arg and the parameter at index second, as a pair. */
    public Target withSecond(int second) {
        return new Target(owner, method, descriptor, arg, second, probe, stamp, dns, mark);
    }

    /** This target with its entry stamped (see the class comment). */
    public Target stamped() {
        return new Target(owner, method, descriptor, arg, arg2, probe, true, dns, mark);
    }

    /** This target as a name lookup, recorded by Hooks.dns (see the class comment). */
    public Target lookup() {
        return new Target(owner, method, descriptor, arg, arg2, probe, stamp, true, mark);
    }

    /**
     * This target with a marker at its entry (Hooks.httpMark, task wd4
     * review round 2): HttpClient.sendAsync, whose requests the tracer
     * cannot place in their connection's order.
     */
    public Target marked() {
        return new Target(owner, method, descriptor, arg, arg2, probe, stamp, dns, true);
    }

    /** owner.method+descriptor, the lookup key. */
    public String key() {
        return owner + "." + method + descriptor;
    }
}
