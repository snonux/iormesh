package iormesh.agent;

import java.util.ArrayList;
import java.util.List;

/**
 * The agent's unit tests, run by `mage agentTest` as a plain main (the
 * agent build is JDK-only: no JUnit). Each test is a named check; the
 * run prints one line per test and exits non-zero if any failed, so mage
 * fails too. Compiled for 17 like the agent, so the same tests also run
 * on every JDK listed in IORMESH_AGENT_JDKS. Bridge internals are tested
 * from the bridge's package (iormesh.bridge.HooksTest).
 */
public final class AgentTests {
    /** One test: a name and a body that throws on failure. */
    public record Case(String name, Body body) {}

    @FunctionalInterface
    public interface Body {
        void run() throws Exception;
    }

    private AgentTests() {}

    public static void main(String[] args) {
        List<Case> cases = new ArrayList<>();
        cases.addAll(HookTableTest.cases());
        cases.addAll(BoundaryTransformerTest.cases());
        cases.addAll(HttpTransformerTest.cases());
        cases.addAll(ServletHookTest.cases());
        cases.addAll(iormesh.agent.http.HttpProbeTest.cases());
        cases.addAll(iormesh.agent.http.SlotsTest.cases());
        cases.addAll(StackIdsTest.cases());
        cases.addAll(AgentArgsTest.cases());
        cases.addAll(iormesh.bridge.HooksTest.cases());
        cases.addAll(iormesh.bridge.RingTest.cases());
        cases.addAll(iormesh.bridge.HttpHooksTest.cases());
        cases.addAll(iormesh.bridge.OtelTest.cases());
        cases.addAll(OtelTransformerTest.cases());
        cases.addAll(OtelAttachRaceTest.cases());
        int failed = 0;
        for (Case c : cases) {
            try {
                c.body().run();
                System.out.println("ok   " + c.name());
            } catch (Throwable t) {
                failed++;
                System.out.println("FAIL " + c.name() + ": " + t);
                t.printStackTrace(System.out);
            }
        }
        System.out.printf("%d tests, %d failed (java %d)%n", cases.size(), failed,
                Runtime.version().feature());
        System.exit(failed == 0 ? 0 : 1);
    }

    public static void check(boolean ok, String what) {
        if (!ok) {
            throw new AssertionError(what);
        }
    }

    public static void equal(Object want, Object got, String what) {
        if (!java.util.Objects.equals(want, got)) {
            throw new AssertionError(what + ": want " + want + ", got " + got);
        }
    }

    /** Asserts body throws an IllegalArgumentException mentioning msg. */
    static void rejects(String msg, Body body) throws Exception {
        try {
            body.run();
        } catch (IllegalArgumentException e) {
            check(e.getMessage().contains(msg), "error \"" + e.getMessage()
                    + "\" does not mention \"" + msg + "\"");
            return;
        }
        throw new AssertionError("accepted; want an error mentioning " + msg);
    }
}
