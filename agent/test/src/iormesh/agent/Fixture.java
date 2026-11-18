package iormesh.agent;

/**
 * Stand-in for a JDK I/O class in BoundaryTransformerTest: the test's
 * hook table lists some of these methods, and the test checks that
 * exactly those are rewritten and still behave the same — on normal
 * return, on a thrown exception, with their own handlers and loops.
 */
public final class Fixture {
    private int field = 7;

    /** Listed: plain return. */
    public int value() {
        return field;
    }

    /** Not listed: an overload of a listed method (descriptors differ). */
    public int value(int add) {
        return field + add;
    }

    /** Not listed at all. */
    public int unlisted() {
        return field * 2;
    }

    /** Listed: leaves by an exception, so exit runs in the handler. */
    public void throwing() {
        throw new IllegalStateException("fixture");
    }

    /** Listed: its own handler must still catch first. */
    public static int withCatch(int x) {
        try {
            return 10 / x;
        } catch (ArithmeticException e) {
            return -1;
        }
    }

    /** Listed: a loop back to an early instruction (stack map frames). */
    public static long loop(int n) {
        long sum = 0;
        for (int i = 0; i < n; i++) {
            sum += i;
        }
        return sum;
    }

    /** Listed, void with several returns. */
    public static void early(boolean stop, StringBuilder sb) {
        if (stop) {
            return;
        }
        sb.append("ran");
    }
}
