# Architecture

How iormesh is put together. Companion to
[PLAN.md](../PLAN.md) — the plan is the source of truth; this
doc gives the narrative.

![How iormesh works](diagrams/architecture.svg)

There is no long-lived collector and no mesh agent. Each server
runs one tracer (probes + correlator) that writes trace segments;
everything cross-host — merging, the graph, mesh expansion, the
views — happens on the controller.

## Components

- **eBPF layer (C, loaded from Go)**
  - Tracepoints for file syscalls and the socket lifecycle. TCP
    4-tuples come from the socket's state transitions
    (`sock:inet_sock_set_state`), not from syscall arguments —
    Java's non-blocking connects return before the connection
    exists — and join back to pid/fd through the sock pointer.
  - Uprobes on the JDK's native I/O functions (`libjava`,
    `libnio`, `libnet`) bracket each Java I/O call per thread
    id, so a syscall can be tied in the kernel to the native
    call that issued it — the Java↔syscall join, confirmed by
    spike S1 ([result](spikes/s1-java-join.md)) on a curated
    set of leaf I/O natives.
  - Native stack capture is opt-in and meant for non-JVM
    targets; BPF cannot unwind JIT-compiled Java frames
    reliably.
- **Correlator (Go, inside the tracer)**
  - Consumes the BPF ring buffer, resolves inode→path, keeps
    pid→fd→path and pid→fd→4-tuple, attaches the Java agent's
    stacks to the syscalls they caused (the uprobe mark names
    the native call; the agent bracket on the same thread id
    that contains it, compared in CLOCK_MONOTONIC, names the Java
    method and stack — S1), and writes the parquet segments.
- **Java agent**
  - Loaded via `-javaagent` (primary) or live-attached to a
    running JVM by the tracer (`run -agent`, `iormesh attach`,
    speaking HotSpot's attach protocol itself) — with JDK 21's
    dynamic-loading caveats, see PLAN.md decision #2 and
    [java-attach.md](java-attach.md).
  - Instruments only the JDK methods where Java hands I/O to the
    kernel and captures the Java stack there. Cost follows the
    I/O rate, not the call rate; blanket method entry/exit
    tracing is a non-goal.
  - Platform threads only in v1. I/O from virtual threads is
    traced but attributed to the carrier thread and flagged;
    naming the virtual thread is deferred (PLAN.md,
    "Deferred — not in v1").
  - Hands events to the tracer through a memory-mapped ring in a
    tracer-owned directory — no socket, and no syscall per
    record (decision #27). Its few own syscalls (ring setup, one
    thread-id lookup per thread, the sleeps of its watermark
    thread) are tagged and kept out of the target's story.
  - OpenTelemetry-aware: when the app runs the OTEL API (SDK
    or not), the agent reads the active span context
    reflectively and tags the I/O events joined to the agent
    inside a span with its OTEL trace/span ids (task 4b4).
  - HTTP/HTTPS instrumentation: hooks at known client/server API
    boundaries record method, URI, headers (redacted), status
    and latency — joined to the socket, the stack and the span.
  - The design was gated by spike S1 (PLAN.md), which proved
    the join and measured its cost.
- **Traceable test workload (Java)**
  - A purpose-built Java app — never a live app — that iormesh is
    developed and validated against first. It runs scripted,
    deterministic scenarios (file ops — data and meta alike —,
    loopback/TCP traffic, known method-call flows, and a
    microservices topology of several service roles talking REST
    over plain HTTP and TLS). Each scenario brackets itself with
    markers and declares the events it must produce, so every
    tracer feature can be checked end-to-end before it ever
    attaches to anything real. This is the Java sibling of ior's
    `cmd/ioworkload`. The catalogue grows milestone by
    milestone.
- **Normalizer**
  - Renumbers run-specific values (PIDs, TIDs, fds, ports,
    inodes, timestamps) to symbolic ids and cuts a trace to a
    scenario's marker bracket. Golden checks, run-to-run diffs
    and visual goldens all work on normalized output.
- **Controller (`iormeshctl`)**
  - Runs on the controller — a laptop or MacBook, Linux or macOS —
    with no eBPF at all. Keeps the server list automatically
    (`iormeshctl add|rm HOSTNAME`), verifies and pushes the
    matching Linux build (amd64/arm64) to Rocky 9 servers as
    `/opt/iormesh/bin/iormesh`, and starts it with
    `sudo iormesh start`.
  - The tracer runs as a transient systemd unit: independent of
    the SSH session, capped in CPU and memory by its cgroup,
    bounded to 4h by default and by a disk-space watchdog, and
    stopped cleanly with a flush. A unit systemd refuses to run
    fails the start with the unit's journal tail and, under
    enforcing SELinux, a hint naming the binary's label.
  - Every privileged action is a tracer subcommand (`start`,
    `stop`, `status`, `uninstall`), so one pinned sudoers rule
    covers the whole lifecycle.
  - Three commands: `collect` only syncs segments, `build` only
    builds from what was collected (offline, repeatable),
    `refresh` does both.
  - Collects the trace segments over rsync — streaming,
    unprivileged, deleting each synced segment on the server —
    and builds the mesh from them offline: both ends of a connection fuse into
    one cross-host edge (NAT-tolerant join key), and clock
    offsets between hosts are bounded from the traffic itself.
  - Additively maintains a service-interaction database of
    app-to-app network interaction; humans name services through
    the CLI via hostname-pattern + port rules (see PLAN.md, M4).
  - Project-aware: unrelated apps on the same server trace as
    separate projects — every trace lives under
    `/opt/iormesh/traces/<project>/`. Build can merge
    projects, and when one app talks to another over TCP, the
    mesh fuses both sides into one cross-project picture.
  - Every tracer flag and feature is controllable through
    iormeshctl — composed over plain SSH (and `kubectl exec` in
    the k8s phase), never over network sockets.
  - `rm` also cleans up the server: it stops the tracer and
    removes `/opt/iormesh` — trace files are synced off first,
    so nothing is lost.
- **Mesh expansion (in the controller)**
  - After a build, the merged graph holds unresolved peers:
    connections whose far end has no tracer. `iormeshctl expand`
    lists them, and with `--apply` deploys tracers to the ones
    matching the expansion allowlist, bound to the process
    listening on the observed port — round by round, under a hop
    limit and a node cap.
  - Tracers never deploy anything and never connect anywhere;
    servers hold no credentials. The mesh grows only through the
    operator's SSH key.
- **Views, query and the LLM eval**
  - iormesh itself builds SVG diagrams of the mesh it generates —
    deterministic for a given graph, so diagrams diff and golden
    like data exports — plus deterministic sequence diagrams:
    lifelines are services, arrows are requests in
    happens-before order, each hop attributed to the Java thread
    that caused it, interleaved with that thread's local file
    I/O.
  - Every `build` ends in something to explore: a local
    static site, a Bubble Tea terminal UI and plain text, all
    rendered from one view model — see
    [visualization.md](visualization.md).
  - `iormeshctl query` slices the merged trace into size-capped
    NDJSON — what an LLM or a human actually reads — and an eval
    with known answers tests whether the outputs really are
    LLM-introspectable.
- **Kubernetes sidecar / debug container**
  - Runs as a sidecar (or `kubectl debug` ephemeral container) in
    the target pod with a minimal capability set, privileged
    only as the fallback (#13);
    identifies each pod as its own host (`host_id` `pod-<uid>`,
    [schema.md](schema.md#pods-as-hosts-m7)) and stamps every
    event with its namespace, pod name and container id (task
    sh4);
    live-attaches the Java agent through the shared process
    namespace.
  - Accepted on kind and el9 (M7), launched by `iormeshctl` with
    the kubectl context and the debug container image
    configurable.
- **Route map: proxy-hop resolution**
  - A DNS name often terminates on an ingress (e.g. Traefik)
    behind a managed LB — no tracer can run there, so the far
    end of the edge is unidentifiable. iormesh takes a mapping
    FQDN → ingress → service → deployment/statefulset — a
    static map, then k8s API discovery (#15) — and completes
    the edge: app →
    FQDN → ingress → workload, with no tracer on the ingress.

## Operator workflow

![Operator workflow](diagrams/workflow.svg)

Everything is driven from the laptop. `collect` syncs and
`build` merges and renders; `refresh` does both and repeats
while a session runs; `expand` turns the unresolved edges of one round
into the newly enrolled hosts of the next; `rm` stops, syncs and
removes. See PLAN.md, M4 and M6.

## Tracing for days

A session ends after its `-duration` (4h by default, 7 days at
most), and its transient unit has no `Restart=`: a reboot, an
OOM kill at `MemoryMax` or a disk-limit trip ends it for good.
`iormeshctl ensure [-project P]... [HOST|POD...]` keeps it going
(PLAN.md M4, "No management plane"; task zh4). Per host — every
registered host and pod entry without HOST|POD; pods are below
— it reads the tracer's `status -json`, then:

| The session | ensure |
|---|---|
| running, or its unit is live | nothing |
| ended `duration`, `targets_exited`, `error` | starts it again |
| ended `stop` with `operator_stop: false` (a reboot's SIGTERM) | starts it again |
| `dead` (crash, OOM kill, power loss), unless `operator_stop` | starts it again |
| stopped with `iormesh stop` (`operator_stop: true`) | leaves it stopped |
| ended `stop` on a tracer from before `operator_stop` | leaves it stopped: it cannot tell |
| ended `disk_limit` | collects first; starts it only once `ready/` is empty |
| never ran on the host | nothing: `deploy` starts new projects |

The END-REASON column reads `stop` for an operator's stop and a
reboot's alike, so `iormeshctl status` tells them apart in its
NOTE column (task 0l4): `stopped by operator: ensure leaves it`,
or `stop origin unknown (tracer predates operator_stop): ensure
leaves it` for an ended `stop` from an older tracer; a row
without either is one ensure starts again. The host's `sudo
iormesh status` prints the same as a line under the session, and
`status -json` carries `operator_stop` itself.

then collects every selected project of the host. A start uses
deploy's argv for the registry entry and goes through the same
sudoers rule (`status`, `start`); ensure never pushes or
upgrades. A `disk_limit` end whose disk is still over the
limit is refused by the new run's own watchdog at its start,
so a timer never loops a full disk. To keep a session ended,
stop it while it runs (`sudo iormesh stop -project P` on the
host) or remove it (`iormeshctl rm`); a session that already
ended cannot be stopped, so narrow ensure with `-project` or
HOST instead. A `deploy` starts a stopped session again; but a
deploy restart whose start fails after its stop leaves the
project marked stopped, so ensure will not recover it — deploy
reports "session stopped, not restarted", and the next deploy
starts it.

A pod entry (task pk4) has no unit, and a stopped tracer
container cannot be entered to read its `state.json`, so ensure
decides each of the entry's pods by its newest tracer container
as the API server reports it:

| The pod's tracer | ensure |
|---|---|
| debug, terminated non-zero (killed with its app container, OOM, crash) | starts `iormesh-N+1` as deploy does, waits for it to attach |
| debug, exited 0 (a SIGTERM: a `-linger` run never exits on its own) | leaves it stopped, skips its collect with a note |
| the pod is terminating (a `pod:` entry's, in its grace period) | nothing: its replacement is `deploy`'s |
| debug, running, session running or attaching | nothing |
| debug, running, session ended (`-duration`, `disk_limit`) | nothing it can do: says a pod restart is needed |
| no tracer container (never deployed, a restarted pod) | nothing: `deploy` starts it |
| sidecar | nothing: the kubelet restarts it |

then reads the pods again and collects each as collect does. A
`pod:` entry whose pod is gone, or a selector matching no
running pod, fails the entry as collect does: there is nothing
to restart or collect, and `deploy` starts a tracer in the pod
that replaces it. A debug tracer ensure restarts counts toward
the pod's 8 ([kubernetes.md](kubernetes.md#limits-you-will-hit)).

"Exit 0 means stopped on purpose" holds for a session that ran
without error: a session that failed lingers too, and the
signal that ends that linger exits 1, so ensure restarts it. To
stop a debug tracer deliberately, send its run SIGTERM through
the PID namespace it shares with the app container, e.g.
`kubectl exec <pod> -c <app> -- kill <pid of iormesh run>`
(the app image needs a `kill`); a SIGKILL exits 137 and is
restarted like a crash.

Run again, ensure finds what it started running and only
collects, so a timer may drive it. A systemd user timer on the
laptop (it needs the SSH key without a prompt: an agent socket
in the user manager's environment, `systemctl --user
import-environment SSH_AUTH_SOCK`, or a key for this alone;
with pod entries registered, it also needs kubectl, the
cluster's credential plugins and their kubeconfig:
`systemctl --user import-environment KUBECONFIG PATH`, or
`Environment=` lines in the service):

```ini
# ~/.config/systemd/user/iormesh-ensure.service
[Unit]
Description=iormeshctl ensure: restart ended iormesh sessions, collect

[Service]
Type=oneshot
ExecStart=%h/bin/iormeshctl ensure

# ~/.config/systemd/user/iormesh-ensure.timer
[Unit]
Description=Run iormeshctl ensure every 15 minutes

[Timer]
OnCalendar=*:0/15
Persistent=true

[Install]
WantedBy=timers.target
```

```sh
systemctl --user daemon-reload
systemctl --user enable --now iormesh-ensure.timer
journalctl --user -u iormesh-ensure.service   # what each run did
```

A timer never starts a run while the last one is still going
(a slow collect). With cron, `flock` gives the same:

```cron
*/15 * * * * flock -n ~/.cache/iormesh-ensure.lock ~/bin/iormeshctl ensure >>~/.cache/iormesh-ensure.log 2>&1
```

ensure exits 1 when a host or pod entry could not be reached or
a start or collect failed, after trying every target; the next
run retries.
Run `iormeshctl build` (or `refresh`) when you want the picture;
ensure only keeps the segments coming.

## The two joins

![Two joins make one story](diagrams/join.svg)

Two joins turn raw events into the end-goal picture. On each
host, a syscall is joined to the Java stack of the thread that
issued it. On the controller, the two hosts' views of one TCP
connection are fused into a single edge. Order across hosts is
happens-before — each thread's own order plus send→receive —
never a comparison of clocks.

## Roadmap

![Roadmap](diagrams/roadmap.svg)

The step-by-step version, with code sketches, is the
[implementation guide](implementation.md).

## See also

- [Trace scope](trace-scope.md) — what is traced, and why each
  family is in the model.
- [Output contract](output-contract.md) — the trace format.
- [Implementation guide](implementation.md) — work packages
  with code sketches.
- [PLAN.md](../PLAN.md) — milestones, spikes and decisions.

The diagrams are generated: edit `diagrams/gen.py` and run
`python3 docs/diagrams/gen.py docs/diagrams`.
