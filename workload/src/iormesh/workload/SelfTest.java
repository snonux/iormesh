package iormesh.workload;

/**
 * Runs every scenario once without a tracer (the JUnit-free test mode
 * behind {@code mage workloadTest}). A scenario fails the run by throwing;
 * all scenarios still run so one failure does not hide another.
 */
final class SelfTest {
    private SelfTest() {}

    static int run(Registry registry) {
        int failed = 0;
        for (Scenario s : registry.all()) {
            try {
                Main.bracketed(s.name(), s::selfTest);
                System.out.println("ok   " + s.name());
            } catch (Exception e) {
                failed++;
                System.out.println("FAIL " + s.name() + ": " + e);
            }
        }
        return failed == 0 ? 0 : Main.EXIT_FAILURE;
    }
}
