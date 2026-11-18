package iormesh.agent;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.bridge.StackIds;
import java.util.List;

/** StackIds: one id per call site, frames kept once, bridge frames dropped. */
final class StackIdsTest {
    private StackIdsTest() {}

    static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("stackids/same site same id", StackIdsTest::sameSite),
                new AgentTests.Case("stackids/other site other id", StackIdsTest::otherSite),
                new AgentTests.Case("stackids/frames", StackIdsTest::frames));
    }

    private static long site() {
        return StackIds.current();
    }

    static void sameSite() {
        long[] ids = new long[3];
        for (int i = 0; i < ids.length; i++) {
            ids[i] = site();
        }
        equal(ids[0], ids[1], "second call");
        equal(ids[0], ids[2], "third call");
        check(ids[0] != StackIds.NONE, "a real id");
    }

    static void otherSite() {
        long a = site();
        long b = site();
        check(a != b, "two call lines, two ids");
    }

    static void frames() {
        int before = StackIds.size();
        long id = site();
        String[] f = StackIds.frames(id);
        check(f != null, "frames recorded");
        check(f[0].startsWith("iormesh.agent.StackIdsTest#site(StackIdsTest.java:"),
                "innermost frame is the caller, got " + f[0]);
        check(f[1].startsWith("iormesh.agent.StackIdsTest#frames("), "then its caller: " + f[1]);
        check(f.length <= 10, "depth capped, got " + f.length);
        check(StackIds.size() == before + 1, "one new stack");
        site();
        equal(null, StackIds.frames(12345L), "unknown id");
    }
}
