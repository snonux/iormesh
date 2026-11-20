package iormesh.testprobe;

import iormesh.bridge.Hooks;

/**
 * The child JVM of RingTest's SecurityManager case (review of fe4): run
 * with the bridge on the boot class path and a policy granting this
 * code base only what the ring itself needs (its spool directory, the
 * pid and tid lookups) — not getStackWalkerWithClassReference, not
 * getProtectionDomain. Opens a ring in args[0] and closes one bracket
 * below a ClassProbe frame; the parent reads the ring.
 */
public final class SecurityProbe {
    private SecurityProbe() {}

    public static void main(String[] args) throws Exception {
        Hooks.configure(5, true, false);
        Hooks.openRing(args[0], 1L << 16);
        ClassProbe.run(() -> {
            Hooks.enter(3);
            Hooks.exit(3);
        });
        System.out.println("probe fails " + Hooks.fails());
    }
}
