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

    /**
     * Requests nested on one thread (review of de4) keep a slot each,
     * also on the same client: an OkHttp authenticator's own call, an
     * Apache interceptor calling its client. The outer's count survives
     * the inner's exit, and an outer exit clears an inner left behind.
     */
    static void nested(Slots slots, Object client) {
        Object other = new Sub();
        Slots.Slot outer = slots.exchange(client, 200);
        Slots.Slot same = slots.exchange(client, 300);
        Slots.Slot call = slots.exchange(other, 400);
        check(same != outer && call != outer && slots.exchange(client, 200) == outer,
                "a slot per open request");
        check(slots.take(client, 300) == same && same.exchanges == 1, "inner on the same client");
        check(slots.take(client, 200) == outer && outer.exchanges == 2, "outer keeps its count");
        check(slots.take(other, 400) == null, "the outer's exit cleared what was left above it");
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
        check(slots.exchange(null, 1) == null && slots.exchange("other", 1) == null
                && slots.exchange(client, 0) == null,
                "no request of the library open, or no stamp: nothing counted");
        Slots.Slot s = slots.exchange(client, 100);
        s.remote = "127.0.0.1:80";
        check(slots.exchange(client, 100) == s && s.exchanges == 2, "one slot, two exchanges");
        check(slots.take(new Sub(), 100) == null && slots.take(client, 99) == null,
                "another request's exit takes nothing");
        check(slots.take(client, 100) == s && slots.take(client, 100) == null, "taken once");
        nested(slots, client);
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
