package iormesh.workload;

import java.util.List;

/**
 * Runs scenarios once without a tracer — every one, or those named after
 * --self-test (the JUnit-free test mode behind {@code mage workloadTest}).
 * A scenario fails the run by throwing;
 * all scenarios still run so one failure does not hide another.
 */
final class SelfTest {
    private SelfTest() {}

    static int run(List<Scenario> scenarios) {
        int failed = 0;
        for (Scenario s : scenarios) {
            try {
                Main.bracketed(s.name(), s::selfTest);
                System.out.println("ok   " + s.name());
            } catch (ScenarioSkipped e) {
                // Not a failure: the host cannot run it (e.g. no IPv6).
                System.out.println("skip " + s.name() + ": " + e.getMessage());
            } catch (Exception e) {
                failed++;
                System.out.println("FAIL " + s.name() + ": " + e);
            }
        }
        return failed == 0 ? 0 : Main.EXIT_FAILURE;
    }
}
