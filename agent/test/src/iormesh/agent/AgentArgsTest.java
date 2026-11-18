package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;
import static iormesh.agent.AgentTests.rejects;

import iormesh.bridge.Hooks;
import java.nio.file.Files;
import java.nio.file.Path;
import java.util.List;

/**
 * AgentArgs' ring options, and an install that fails before the first
 * rewrite (a missing spool directory) releasing the bridge's claim.
 */
final class AgentArgsTest {
    private AgentArgsTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("agentargs/defaults", AgentArgsTest::defaults),
                new AgentTests.Case("agentargs/ring options", AgentArgsTest::ringOptions),
                new AgentTests.Case("agentargs/rejects", AgentArgsTest::rejections),
                new AgentTests.Case("agent/failed prepare releases claim",
                        AgentArgsTest::failedPrepare));
    }

    static void defaults() {
        AgentArgs a = AgentArgs.parse(null);
        equal(null, a.spool(), "no spool");
        equal(AgentArgs.DEFAULT_RING_SIZE, a.ringSize(), "ring size");
        equal(AgentArgs.DEFAULT_WATERMARK_MS, a.watermarkMs(), "watermark");
        check(a.walkStack(), "stack walk on");
    }

    static void ringOptions() {
        AgentArgs a = AgentArgs.parse("spool=/run/iormesh,ringsize=65536,watermark=0,nostack");
        equal("/run/iormesh", a.spool(), "spool");
        equal(65536L, a.ringSize(), "ring size");
        equal(0L, a.watermarkMs(), "watermark");
        check(!a.walkStack(), "nostack");
    }

    static void rejections() throws Exception {
        rejects("spool needs a value", () -> AgentArgs.parse("spool"));
        rejects("spool needs a value", () -> AgentArgs.parse("spool="));
        rejects("ringsize needs a number", () -> AgentArgs.parse("ringsize=-1"));
        rejects("ringsize needs a number", () -> AgentArgs.parse("ringsize=8M"));
        rejects("watermark needs a number", () -> AgentArgs.parse("watermark=x"));
        rejects("watermark needs a value", () -> AgentArgs.parse("watermark"));
        rejects("unknown agent option ring", () -> AgentArgs.parse("ring=1"));
    }

    /**
     * The spool directory is missing, so prepare fails at the ring —
     * before any rewrite — and must leave the bridge unclaimed for a
     * later attach.
     */
    static void failedPrepare() throws Exception {
        Path dir = Files.createTempDirectory("iormesh-agent-test");
        Files.delete(dir);
        Hooks.claim();
        HookTable table = HookTable.load("jdk21");
        try {
            Agent.prepare(AgentArgs.parse("spool=" + dir), table, Bridge.of(Hooks.class));
            throw new AssertionError("prepare succeeded without a spool directory");
        } catch (java.io.IOException expected) {
            check(Hooks.claim(), "claim released after the failed install");
        }
    }
}
