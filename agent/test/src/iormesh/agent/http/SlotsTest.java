package iormesh.agent.http;

import static iormesh.agent.AgentTests.check;
import static iormesh.agent.AgentTests.equal;

import iormesh.agent.AgentTests;
import java.util.List;

/**
 * Slots (task de4): an exchange counts only under a request of the
 * library's own type, a request's exit takes its slot exactly once, a
 * failed request after an exchange is a lost record, and the slot's
 * findings reach the record. The real libraries run in AgentSmoke
 * (Apache HttpClient) and the integration tests.
 */
public final class SlotsTest {
    private SlotsTest() {}

    /** The owner type the slots are keyed by, and one that is not it. */
    static class Client {}

    static final class Sub extends Client {}

    public static List<AgentTests.Case> cases() {
        return List.of(new AgentTests.Case("http/exchange slots", SlotsTest::slots),
                new AgentTests.Case("http/okhttp and netty names", SlotsTest::names));
    }

    /** OkHttp's protocol names as versions; a Host header's host for Netty's client side. */
    static void names() {
        equal(List.of("HTTP/1.1", "HTTP/2", "HTTP/2", "HTTP/1.0", ""),
                List.of(OkHttp.Api.version("http/1.1"), OkHttp.Api.version("h2"),
                        OkHttp.Api.version("h2_prior_knowledge"), OkHttp.Api.version("http/1.0"),
                        OkHttp.Api.version(null)), "versions");
        equal(List.of("h", "h", "::1", "::1", ""),
                List.of(Netty.Api.hostOf("h:8080"), Netty.Api.hostOf("h"),
                        Netty.Api.hostOf("[::1]:80"), Netty.Api.hostOf("[::1]"),
                        Netty.Api.hostOf(null)), "hosts");
    }

    static void slots() {
        Slots slots = new Slots(Client.class.getName());
        Object client = new Sub();
        check(slots.exchange(null) == null && slots.exchange("other") == null,
                "no request of the library open: nothing counted");
        Slots.Slot s = slots.exchange(client);
        s.remote = "127.0.0.1:80";
        check(slots.exchange(client) == s && s.exchanges == 2, "one slot, two exchanges");
        check(slots.take(new Sub()) == null, "another request's exit takes nothing");
        check(slots.take(client) == s && slots.take(client) == null, "taken once");
        Request r = new Request();
        Slots.apply(s, r);
        equal(List.of(2, "127.0.0.1:80", ""), List.of(r.exchanges, r.remote, r.version),
                "exchanges and ends applied, version kept");
        Slots.failed(null);
        try {
            Slots.failed(s);
            throw new AssertionError("a failure after an exchange did not throw");
        } catch (IllegalStateException e) {
            check(e.getMessage().contains("2 exchange"), "lost record: " + e.getMessage());
        }
    }
}
