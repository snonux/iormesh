package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import java.io.ByteArrayOutputStream;
import java.io.PrintStream;
import java.nio.charset.StandardCharsets;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * Agent.reportUnapplied (task bj4): a row whose class the transformer
 * was handed but whose method it did not find — the table does not
 * match this JDK — gets one stderr line; matched rows and rows of a
 * class never loaded print nothing, and install plus exit name each row
 * once.
 */
final class AgentReportTest {
    private static final String FIXTURE = "iormesh/agent/Fixture";

    private AgentReportTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("agent/reports a row matching no method",
                        AgentReportTest::wrongDescriptor),
                new AgentTests.Case("agent/silent when every seen row matched",
                        AgentReportTest::allMatched),
                new AgentTests.Case("agent/reports each row once",
                        AgentReportTest::onceEach));
    }

    private static String row(int id, String owner, String method, String desc) {
        return id + "\tread\t" + owner + "\t" + method + "\t" + desc + "\tnone\tnone";
    }

    /** Rewrites Fixture with rows and returns what reportUnapplied printed. */
    private static String report(List<String> rows) throws Exception {
        HookTable table = HookTable.parse("test", rows);
        BoundaryTransformer t = new BoundaryTransformer(table);
        t.rewrite(FIXTURE, BoundaryTransformerTest.fixtureBytes());
        return report(table, t, new HashSet<>());
    }

    private static String report(HookTable table, BoundaryTransformer t, Set<Integer> reported) {
        ByteArrayOutputStream buf = new ByteArrayOutputStream();
        try (PrintStream err = new PrintStream(buf, true, StandardCharsets.UTF_8)) {
            Agent.reportUnapplied(table, t, reported, err);
        }
        return buf.toString(StandardCharsets.UTF_8);
    }

    /**
     * Install reports, then exit reports again: the second call names
     * only a row whose class was first handed over in between (a class
     * loaded after install), never one install already named.
     */
    static void onceEach() throws Exception {
        String other = "iormesh/agent/LoadedLater";
        HookTable table = HookTable.parse("test", List.of(
                row(2, FIXTURE, "value", "(J)I"),
                row(4, other, "value", "(J)I")));
        BoundaryTransformer t = new BoundaryTransformer(table);
        t.rewrite(FIXTURE, BoundaryTransformerTest.fixtureBytes());
        Set<Integer> reported = new HashSet<>();
        String install = report(table, t, reported);
        check(install.contains("hook 2 ") && !install.contains("hook 4 "), "install: " + install);
        // Fixture's bytes under another name: the class "loads" later.
        t.rewrite(other, BoundaryTransformerTest.fixtureBytes());
        String exit = report(table, t, reported);
        check(exit.contains("hook 4 ") && !exit.contains("hook 2 "), "exit: " + exit);
        equal("", report(table, t, reported), "third call");
    }

    /**
     * Row 2 names value with a descriptor Fixture lacks (the JDK-drift
     * case): one line. Row 1 matched and row 3's class never loaded:
     * neither prints.
     */
    static void wrongDescriptor() throws Exception {
        String out = report(List.of(
                row(1, FIXTURE, "value", "()I"),
                row(2, FIXTURE, "value", "(J)I"),
                row(3, "iormesh/agent/NeverLoaded", "value", "()I")));
        equal("iormesh-agent: hook 2 (read) iormesh/agent/Fixture.value(J)I matches no"
                + " method in this JDK (java " + Runtime.version().feature()
                + ", table test), no attribution for it\n", out, "stderr");
    }

    static void allMatched() throws Exception {
        String out = report(List.of(
                row(1, FIXTURE, "value", "()I"),
                row(3, "iormesh/agent/NeverLoaded", "value", "()I")));
        equal("", out, "stderr");
    }
}
