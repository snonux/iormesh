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
 * owner is an internal name (sun/net/httpserver/HttpExchangeImpl);
 * descriptors are exact, so a JDK that changes a signature leaves the
 * row unmatched instead of hooking a different method. Several rows may
 * name one method with different descriptors, one per JDK generation.
 */
public record Target(String owner, String method, String descriptor, int arg,
                     BiFunction<Object[], int[], String[]> probe) {
    /** No argument is passed (null). */
    public static final int NO_ARG = -1;
    /** The method's last parameter is passed. */
    public static final int LAST_ARG = -2;

    /** owner.method+descriptor, the lookup key. */
    public String key() {
        return owner + "." + method + descriptor;
    }
}
