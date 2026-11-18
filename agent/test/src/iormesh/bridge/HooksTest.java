package iormesh.bridge;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import java.util.List;
import java.util.concurrent.atomic.AtomicLongArray;
import java.util.function.LongSupplier;

/**
 * Hooks' bracket bookkeeping, from its own package so the tests can fail
 * the stack walk and the counters on purpose: brackets nest, skip while
 * busy, survive overflow and mismatched exits, and never throw or leave
 * the depth unbalanced.
 */
public final class HooksTest {
    private HooksTest() {}

    public static List<AgentTests.Case> cases() {
        return List.of(
                new AgentTests.Case("hooks/nesting", HooksTest::nesting),
                new AgentTests.Case("hooks/busy skips", HooksTest::busySkip),
                new AgentTests.Case("hooks/overflow", HooksTest::overflow),
                new AgentTests.Case("hooks/mismatched exit", HooksTest::mismatched),
                new AgentTests.Case("hooks/throwing walk", HooksTest::throwingWalk),
                new AgentTests.Case("hooks/throwing exit", HooksTest::throwingExit),
                new AgentTests.Case("hooks/claim once", HooksTest::claimOnce));
    }

    /** Fresh counters for ids 1..4, stack walking on; depth must be 0. */
    private static void reset() {
        Hooks.configure(5, true, true);
        equal(0, Hooks.depth(), "depth before the test");
    }

    private static List<Long> counts() {
        long[] c = Hooks.counts();
        return List.of(c[1], c[2], c[3], c[4]);
    }

    static void nesting() {
        reset();
        Hooks.enter(1);
        Hooks.enter(2);
        Hooks.exit(2);
        Hooks.exit(1);
        equal(List.of(1L, 1L, 0L, 0L), counts(), "both brackets closed");
        equal(0, Hooks.depth(), "balanced");
    }

    /** I/O inside the stack walk (the bridge's own) is not bracketed. */
    static void busySkip() {
        reset();
        long before = Hooks.skips();
        LongSupplier saved = Hooks.stacks;
        Hooks.stacks = () -> {
            Hooks.enter(3);
            Hooks.exit(3);
            return 42;
        };
        try {
            Hooks.enter(1);
            Hooks.exit(1);
        } finally {
            Hooks.stacks = saved;
        }
        equal(1L, Hooks.skips() - before, "one skip");
        equal(List.of(1L, 0L, 0L, 0L), counts(), "outer closed, inner skipped");
        equal(0, Hooks.depth(), "balanced");
    }

    /** 17 levels: the 17th is not tracked, the 16 below still close. */
    static void overflow() {
        reset();
        int levels = Hooks.MAX_DEPTH + 1;
        for (int i = 0; i < levels; i++) {
            Hooks.enter(1);
        }
        for (int i = 0; i < levels; i++) {
            Hooks.exit(1);
        }
        equal(List.of((long) Hooks.MAX_DEPTH, 0L, 0L, 0L), counts(), "16 closed");
        equal(0, Hooks.depth(), "balanced");
    }

    static void mismatched() {
        reset();
        Hooks.enter(1);
        Hooks.exit(2);
        Hooks.exit(2);   // no open bracket: ignored
        equal(0, Hooks.depth(), "balanced");
        Hooks.enter(1);
        Hooks.exit(1);
        equal(List.of(1L, 0L, 0L, 0L), counts(), "only the matched pair counts");
    }

    /** A walk that throws drops the bracket, not the thread's depth. */
    static void throwingWalk() {
        reset();
        long before = Hooks.fails();
        LongSupplier boom = () -> {
            throw new StackOverflowError("test");
        };
        LongSupplier saved = Hooks.stacks;
        Hooks.enter(1);
        Hooks.stacks = boom;
        try {
            Hooks.enter(2);   // must not throw
            Hooks.exit(2);
        } finally {
            Hooks.stacks = saved;
        }
        Hooks.exit(1);
        equal(1L, Hooks.fails() - before, "one failure counted");
        equal(List.of(1L, 0L, 0L, 0L), counts(), "outer closed, failed one dropped");
        equal(0, Hooks.depth(), "balanced");
        Hooks.enter(1);
        Hooks.exit(1);
        equal(List.of(2L, 0L, 0L, 0L), counts(), "later brackets still close");
    }

    /** An exit that fails must not throw (the handler would exit twice). */
    static void throwingExit() {
        reset();
        long before = Hooks.fails();
        Hooks.enter(1);
        Hooks.enter(2);
        AtomicLongArray saved = Hooks.counts;
        Hooks.counts = null;
        try {
            Hooks.exit(2);    // NullPointerException inside: swallowed
        } finally {
            Hooks.counts = saved;
        }
        Hooks.exit(1);
        equal(1L, Hooks.fails() - before, "one failure counted");
        equal(List.of(1L, 0L, 0L, 0L), counts(), "the outer bracket still closes");
        equal(0, Hooks.depth(), "balanced");
    }

    static void claimOnce() {
        Hooks.claim();    // whatever ran before, the bridge is claimed now
        check(!Hooks.claim(), "a second claim is refused");
    }
}
