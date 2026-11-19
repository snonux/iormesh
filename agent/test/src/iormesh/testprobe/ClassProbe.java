package iormesh.testprobe;

/**
 * A frame outside the bridge's package for RingTest's class records
 * (task fe4): StackIds drops iormesh.bridge frames by name, so a probe
 * there would leave only the JDK's. Only that case calls it, so its
 * class is new to that case's ring.
 */
public final class ClassProbe {
    private ClassProbe() {}

    /** Runs body, the case's bracket, with this class's frame below it. */
    public static void run(Runnable body) {
        body.run();
    }
}
