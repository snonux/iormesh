package iormesh.agent.http;

import java.util.function.BiConsumer;
import java.util.function.Function;
import java.util.function.Supplier;

/**
 * The bridge's two handles for a request that completes on another
 * thread than the one that sent it (HttpClient.sendAsync, task wd4):
 * caller takes the sending thread's identity — stack id, Java thread id,
 * kernel tid, OTEL span (iormesh.bridge.Hooks.httpCaller) — inside the
 * send's probe; late, called from the completing thread with that
 * identity plus the sending hook's id, runs the describer as Hooks runs
 * a probe (busy thread, meta to fill) and writes the request it returns
 * (Hooks.httpLate). JDK types only: the bridge is on another loader.
 */
public record Async(Supplier<long[]> caller,
                    BiConsumer<long[], Function<long[], String[]>> late) {
    /** Length of caller's result (Hooks.CALLER); late takes it plus the hook id. */
    static final int CALLER = 6;
}
