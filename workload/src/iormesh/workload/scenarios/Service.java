package iormesh.workload.scenarios;

import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;

/**
 * The microservices scenario's service roles and the topology between
 * them (PLAN.md M0 "Microservices end-to-end scenario"). Every hop is
 * REST over one HTTP/1.1 connection of its own; a service with a TLS
 * server is reached over TLS, pinned to the certificate it published:
 *
 * <pre>
 *   gateway ──TLS──▶ orders ──plain──▶ inventory     chain gateway→orders→inventory
 *      │                └──────TLS──▶ billing        fan-out orders→{inventory, billing}
 *      └───────plain──────────────▶ inventory        fan-in {gateway, orders}→inventory
 * </pre>
 *
 * The gateway is the front door: it serves nothing and drives one
 * checkout of order 42 — place the order (orders reserves the stock and
 * charges the order before it confirms), then read the stock left. The
 * order of every hop is fixed: orders answers the gateway only after its
 * own two calls returned, so inventory always serves the reservation
 * (from orders) before the stock query (from the gateway).
 *
 * Each service also has its own file work in its own directory
 * {@code <role>.d}: the config it boots from ({@code <role>.conf}) and
 * one data record written the create-tmp-then-rename way
 * ({@link DataStore#save}).
 */
enum Service {
    GATEWAY("gateway", false, List.of(), "checkout-42.json"),
    ORDERS("orders", true, List.of(Calls.PLACE_ORDER), "order-42.json"),
    INVENTORY("inventory", false, List.of(Calls.RESERVE, Calls.STOCK), "reservation-42.json"),
    BILLING("billing", true, List.of(Calls.CHARGE), "invoice-42.json");

    /** One outgoing hop: the service called and the call it gets. */
    record Hop(Service to, HttpCall call) {}

    /** The declared REST calls; bodies and replies are fixed, run after run. */
    static final class Calls {
        static final String ORDER = "{\"order\":42,\"item\":\"widget\",\"qty\":2}";
        static final String CONFIRMED = "{\"order\":42,\"status\":\"confirmed\"}";
        static final String CHARGE_BODY = "{\"order\":42,\"amount_cents\":1998}";
        static final String STOCK_LEFT = "{\"item\":\"widget\",\"stock\":8}";

        static final HttpCall PLACE_ORDER = HttpCall.post("/orders", ORDER, 201, CONFIRMED);
        static final HttpCall RESERVE = HttpCall.post("/inventory/reserve", ORDER, 200,
                "reserved");
        static final HttpCall CHARGE = HttpCall.post("/billing/charge", CHARGE_BODY, 200,
                "charged");
        static final HttpCall STOCK = HttpCall.get("/inventory/stock/widget", 200, STOCK_LEFT);

        private Calls() {}
    }

    /** The --role value. */
    final String role;
    /** Whether the service's server speaks TLS (and so publishes a cert). */
    final boolean tls;
    /** The calls the service serves, in arrival order; none for the gateway. */
    final List<HttpCall> serves;
    /** The data record the service saves, in its directory. */
    final String dataFile;

    Service(String role, boolean tls, List<HttpCall> serves, String dataFile) {
        this.role = role;
        this.tls = tls;
        this.serves = serves;
        this.dataFile = dataFile;
    }

    /**
     * The calls this service makes, in order: the gateway's checkout, or
     * the calls orders makes while it handles the order. A switch, not a
     * constructor argument: an enum constant cannot name a later one.
     */
    List<Hop> hops() {
        return switch (this) {
            case GATEWAY -> List.of(new Hop(ORDERS, Calls.PLACE_ORDER),
                    new Hop(INVENTORY, Calls.STOCK));
            case ORDERS -> List.of(new Hop(INVENTORY, Calls.RESERVE),
                    new Hop(BILLING, Calls.CHARGE));
            case INVENTORY, BILLING -> List.of();
        };
    }

    /** The services this one calls, each once, in call order. */
    Set<Service> downstream() {
        Set<Service> out = new LinkedHashSet<>();
        hops().forEach(h -> out.add(h.to()));
        return out;
    }

    /**
     * What the record holds — what the service would persist: the body of
     * the first call it serves, or for the gateway the confirmation.
     */
    String record() {
        return serves.isEmpty() ? Calls.CONFIRMED : serves.get(0).body();
    }

    /** The config the service boots from: its name and whom it calls. */
    String config() {
        StringBuilder b = new StringBuilder("service=" + role + "\ncalls=");
        String sep = "";
        for (Service s : downstream()) {
            b.append(sep).append(s.role);
            sep = ",";
        }
        return b.append('\n').toString();
    }

    /** The option naming the port this service listens on, for callers. */
    String portOption() {
        return role + "-port";
    }

    /**
     * The option naming the address callers dial this service at: its
     * --bind, the one address its certificate names, so a TLS caller's
     * endpoint identification passes for that address only.
     */
    String hostOption() {
        return role + "-host";
    }

    /** The published certificate's file name in the shared --dir. */
    String certFile() {
        return role + ".crt";
    }

    /** The service's own directory in --dir. */
    String dirName() {
        return role + ".d";
    }

    String configFile() {
        return role + ".conf";
    }

    /** The service a --role names, or null. */
    static Service of(String role) {
        for (Service s : values()) {
            if (s.role.equals(role)) {
                return s;
            }
        }
        return null;
    }
}
