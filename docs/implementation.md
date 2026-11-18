# Implementation guide

Step-by-step work packages with code sketches, for whoever — human
or agent — picks up a spike or milestone. Companion to
[PLAN.md](../PLAN.md): the plan decides *what* and *in which
order*; this guide shows *how*.

Read this first:

- **The plan wins.** Where a sketch here disagrees with PLAN.md,
  PLAN.md is right and this file needs fixing in the same commit.
- **ior wins on mechanics.** Sketches are written against `../ior`
  as it looked on 2026-11-17. ior moves; when a sketch and ior's
  current code disagree, follow ior and update the sketch.
- **Sketches are sketches.** They fix names, shapes and the order
  of work so two agents build the same thing. They are not
  compiled, not complete, and items marked *hypothesis* are
  exactly that — a spike confirms or replaces them.
- **Nothing is scaffolded ahead of time** (AGENTS.md). Create a
  package when its work package starts, not before.

![Roadmap: spikes gate the milestones](diagrams/roadmap.svg)

## Contents

- [Ground rules](#ground-rules)
- [Shared building blocks](#shared-building-blocks)
- [M0 — workload framework](#m0--workload-framework)
- [M1 — walking skeleton](#m1--walking-skeleton)
- [S3 — overhead and volume budget](#s3--overhead-and-volume-budget)
- [M2 — full trace surface](#m2--full-trace-surface)
- [S1 — Java↔syscall join](#s1--javasyscall-join)
- [M3 — Java agent](#m3--java-agent)
- [S2 — cross-host join](#s2--cross-host-join)
- [M4 — controller](#m4--controller)
- [M5 — views, query, eval](#m5--views-query-eval)
- [M6 — mesh expansion](#m6--mesh-expansion)
- [M7 — Kubernetes](#m7--kubernetes)

## Ground rules

Work order: M0 → M1 → S3 → M2 → S1 → M3 → S2 → M4 → M5 → M6 → M7.
S1 and S2 touch nothing the earlier packages own, so they can start
any time after M1 if a second agent is available.

Before the first line of code, read in `../ior`: `AGENTS.md`
("Build/Test Commands", "Code Generation", "Generation host /
kernel", "Code Style") and `integrationtests/README.md`.

Conventions (ior's, kept):

- **Module**: `module iormesh`, imports as
  `"iormesh/internal/<pkg>"` — ior uses the bare name `ior` the
  same way. Go version: whatever ior's `go.mod` declares.
- **Build through Mage**: `mage build`, `mage test`, `mage vet`,
  `mage lint`, `mage generate`, `mage integrationTest`. Bare
  `go test ./...` fails on packages that import libbpfgo, because
  only Mage wires `CGO_CFLAGS`/`CGO_LDFLAGS`/`LIBBPFGO`.
- **Functions stay under 50 lines**; split into helpers of about
  30. `main.go` files stay thin — flag parsing and one call into
  `internal/`.
- **Comments say why**, and are updated with the code they
  describe.
- **Errors are returned**, compared with `errors.Is`; a
  deliberately dropped error is written `_ = f.Close()`.
  `//nolint` is banned (ior enforces this with a test — copy it).
- **Provenance header** on every file copied or adapted from ior:

  ```go
  // Adapted from ior internal/generate/codegen.go @ <ior commit>.
  // Changes: <one line>.
  ```

- **Docs** wrap at 79 columns; commits are `area: summary`,
  pushed after each change-set.

### The cgo boundary

The tracer needs cgo (libbpfgo). The controller must build with
`CGO_ENABLED=0` for darwin and linux. That splits the tree in two,
and a test keeps it split:

| cgo allowed (tracer only)        | cgo-free (shared)              |
|----------------------------------|--------------------------------|
| `internal/bpf` (loader, ringbuf) | `internal/schema`              |
| `internal/correlate`             | `internal/normalize`           |
| `cmd/iormesh`                    | `internal/federate`, `expand`  |
|                                  | `internal/servicedb`, `query`  |
|                                  | `internal/render`              |
|                                  | `cmd/iormeshctl`               |

```go
// internal/buildgate/cgofree_test.go
// TestControllerIsCgoFree fails when anything the controller
// imports pulls in cgo — the darwin cross-build would break long
// after the offending import was added.
func TestControllerIsCgoFree(t *testing.T) {
    out, err := exec.Command("go", "list", "-deps",
        "-f", "{{if .CgoFiles}}{{.ImportPath}}{{end}}",
        "./cmd/iormeshctl").Output()
    if err != nil {
        t.Fatal(err)
    }
    if pkgs := strings.Fields(string(out)); len(pkgs) > 0 {
        t.Fatalf("cgo packages in iormeshctl: %v", pkgs)
    }
}
```

## Shared building blocks

These types are used by every package after M1. They live in
`internal/schema` (cgo-free) and are the Go form of PLAN.md's
"Trace output contract".

### The event envelope

```go
// Package schema is the trace output contract: one Event type,
// two serializations (NDJSON and parquet). Fields are only ever
// appended — a reader of an older file sees the new ones as
// absent, never as a different meaning (ior's parquet rule).
package schema

const Version = 1

type Event struct {
    SchemaVersion uint32 `json:"schema_version" parquet:"schema_version"`
    Project       string `json:"project" parquet:"project,dict"`
    Seq           uint64 `json:"seq" parquet:"seq"`
    BootNS        uint64 `json:"boot_ns" parquet:"boot_ns"`
    WallNS        uint64 `json:"wall_ns" parquet:"wall_ns"`
    HostID        string `json:"host_id" parquet:"host_id,dict"`
    PID           uint32 `json:"pid" parquet:"pid"`
    PIDInc        uint64 `json:"pid_incarnation" parquet:"pid_incarnation"`
    TID           uint32 `json:"tid" parquet:"tid"`
    Comm          string `json:"comm" parquet:"comm,dict"`
    Event         string `json:"event" parquet:"event,dict"`
    FD            int32  `json:"fd" parquet:"fd"`
    FDGen         uint32 `json:"fd_gen" parquet:"fd_gen"`
    Ret           int64  `json:"ret" parquet:"ret"`
    Err           string `json:"err,omitempty" parquet:"err,dict"`
    LatencyNS     uint64 `json:"latency_ns" parquet:"latency_ns"`
    Bytes         uint64 `json:"bytes" parquet:"bytes"`
    Args          Args   `json:"args" parquet:"args"`
    ConnID        string `json:"conn_id,omitempty" parquet:"conn_id,optional"`
    Attribution   string `json:"attribution" parquet:"attribution,dict"`
    Java          *Java  `json:"java,omitempty" parquet:"java,optional"`
    HTTP          *HTTP  `json:"http,omitempty" parquet:"http,optional"`
}

// Args holds every syscall-specific argument as an optional
// field: one nullable parquet column per field, never a JSON
// blob, so queries can filter on any of them.
type Args struct {
    Path       *string `json:"path,omitempty" parquet:"path,optional"`
    NewPath    *string `json:"new_path,omitempty" parquet:"new_path,optional"`
    Flags      *uint32 `json:"flags,omitempty" parquet:"flags,optional"`
    Offset     *int64  `json:"offset,omitempty" parquet:"offset,optional"`
    LocalAddr  *string `json:"local_addr,omitempty" parquet:"local_addr,optional"`
    LocalPort  *uint16 `json:"local_port,omitempty" parquet:"local_port,optional"`
    RemoteAddr *string `json:"remote_addr,omitempty" parquet:"remote_addr,optional"`
    RemotePort *uint16 `json:"remote_port,omitempty" parquet:"remote_port,optional"`
    SockState  *string `json:"sock_state,omitempty" parquet:"sock_state,optional"`
    Marker     *string `json:"marker,omitempty" parquet:"marker,optional"`
}
```

Field notes:

- `host_id` is `/etc/machine-id`; the session header record adds
  boot id, FQDN, kernel release and arch.
- `pid_incarnation` is the task's start time on the boot clock
  (`task->start_boottime` via CO-RE) — unique per PID reuse.
- `fd_gen` counts how often this fd number was (re)created in this
  process incarnation: +1 on every open/accept/dup landing on it.
- `event` is the syscall name, or one of the synthetic kinds:
  `tcp_state`, `marker`, `drop` (ring-buffer loss), `session`
  (header), `end` (why the trace ended).
- `attribution` is `"java"` or `"none"` — never empty. A
  `carrier` flag (added with M3) marks I/O that ran for a
  virtual thread, which v1 attributes to the carrier.
- `Java` and `HTTP` are provisional until S1 reports.

One NDJSON line, as M1 emits it:

```json
{"schema_version":1,"project":"demo","seq":412,"boot_ns":91822003417,
 "wall_ns":1794953012000112233,"host_id":"3f2a…","pid":4711,
 "pid_incarnation":91700000000,"tid":4719,"comm":"java",
 "event":"openat","fd":7,"fd_gen":1,"ret":7,"latency_ns":18211,
 "bytes":0,"args":{"path":"/etc/orders.conf","flags":0},
 "attribution":"none"}
```

### The graph

```go
// Graph is the merged, queryable form: built per host by the
// correlator (M1), fused across hosts by the merge (M4).
type Graph struct {
    SchemaVersion uint32 `json:"schema_version"`
    Nodes         []Node `json:"nodes"` // sorted by (Type, ID)
    Edges         []Edge `json:"edges"` // sorted by (Type, From, To)
}

type Node struct {
    Type  string            `json:"type"` // host|process|app|file|socket|service
    ID    string            `json:"id"`   // stable, see below
    Attrs map[string]string `json:"attrs,omitempty"`
}

type Edge struct {
    Type  string            `json:"type"` // runs_on|opened|connected|…
    From  string            `json:"from"`
    To    string            `json:"to"`
    State string            `json:"state,omitempty"` // fused|unresolved
    Attrs map[string]string `json:"attrs,omitempty"`
}
```

Node ids are derived, never counters: `host:<host_id>`,
`proc:<host_id>/<pid>@<pid_incarnation>`, `file:<host_id>/<path>`,
`conn:<conn_id>`. Slices are sorted before every export; Go map
iteration order must never reach an output file.

### Expectations (the golden format)

A scenario's expectation file sits next to its source in
`workload/scenarios/<name>/expect.json`. Symbolic ids (`$p1`,
`$t1`, `$fd1`, `$port1`) bind on first match; the checker then
requires consistency.

```json
{"scenario": "file-basic",
 "schema_version": 1,
 "events": [
   {"thread": "$t1", "event": "openat",
    "args": {"path": "$tmp/file-basic.dat"}, "fd": "$fd1"},
   {"thread": "$t1", "event": "write", "fd": "$fd1", "bytes": 4096},
   {"thread": "$t1", "event": "read",  "fd": "$fd1", "bytes": 4096},
   {"thread": "$t1", "event": "close", "fd": "$fd1"}
 ],
 "edges": [
   {"type": "opened", "from": "$p1", "to": "file:$tmp/file-basic.dat"}
 ],
 "must_not_appear": []}
```

Events of one `thread` must appear in the listed order; events of
different threads are unordered against each other.

Clarifications the M0 goldens rely on (file-basic and
tcp-loopback ship real ones):

- `fd` is the fd the event acts on: the argument, or for
  `openat` and `accept` the returned one. An fd symbol binds to
  the fd *instance* (pid, fd, `fd_gen`), so two opens that reuse
  one number — as FileOutputStream then FileInputStream do —
  take two symbols (`$fd1`, `$fd2`).
- Multi-process scenarios add `"process": "$p1"` next to
  `thread`, binding the same `$p` symbols the edges use.
- Declare only syscalls the target milestone traces (M1: open,
  read, write, close, connect, accept) and ports rather than
  addresses: the JVM's dual-stack sockets show 127.0.0.1 as
  `::ffff:127.0.0.1` unless the host lacks IPv6.

## M0 — workload framework

**Goal**: a JDK-only Java 21 app that runs one named scenario,
brackets it with markers, and ships the scenario's expectations.
PLAN.md: "M0 — Traceable test workload".

**Files** (the framework landed with M0 and its code is now the
reference — the sketches below only record intent)

```
workload/
  src/iormesh/workload/Main.java        # arg parsing, gating, run
  src/iormesh/workload/Scenario.java    # the interface
  src/iormesh/workload/Marker.java      # begin/end markers
  src/iormesh/workload/Registry.java    # name → Scenario
  src/iormesh/workload/SelfTest.java    # --self-test runner
  src/iormesh/workload/scenarios/Noop.java      # framework check
  src/iormesh/workload/scenarios/FileBasic.java   # M1 scenario
  src/iormesh/workload/scenarios/TcpLoopback.java # M1 scenario
  src/iormesh/workload/scenarios/FileWork.java    # their file work
  scenarios/<name>/expect.json          # bundled into the jar
```

No Maven, no Gradle: `mage workloadBuild` (in `Magefile.go`)
walks `workload/src` for `.java` files, runs `javac --release 21
-Xlint:all -Werror` and `jar`, and bundles `workload/scenarios/`
so `java -jar workload.jar --expect <name>` prints a scenario's
declared expectations. The workload has no build-time
dependencies either.

**Steps**

1. `Scenario` and `Registry`:

   ```java
   /** One deterministic, declared behavior. */
   public interface Scenario {
       String name();
       /** role is "client", "server" or "solo". */
       void run(String role, Map<String, String> opts) throws Exception;
   }
   ```

   Multi-role scenarios override `default void selfTest()` to drive
   every role inside one process, so `--self-test` needs no second
   process and no fixed port.

2. `Marker` — the bracket every golden check cuts to. It is an
   `open(2)` of a reserved path that never exists, so it costs one
   failed syscall and needs no tracer cooperation:

   ```java
   /** Emits scenario markers as recognizable failed opens. */
   final class Marker {
       static final String ROOT = "/iormesh-marker/";

       static void begin(String scenario) { touch(scenario, "begin"); }
       static void end(String scenario)   { touch(scenario, "end"); }

       private static void touch(String scenario, String edge) {
           try {
               new FileInputStream(ROOT + scenario + "/" + edge).close();
           } catch (IOException expected) {
               // ENOENT is the point: the tracer sees the openat
               // and its path; nothing is ever created.
           }
       }
   }
   ```

   The correlator turns `openat` on `/iormesh-marker/<s>/<edge>`
   into a synthetic `marker` event (`args.marker = "<s>/<edge>"`).

3. `Main` — mirror ior's `cmd/ioworkload` gating, which exists so
   the tracer is attached before the first scenario syscall:
   - print the PID as the first stdout line (the harness reads
     it);
   - if `IORMESH_WORKLOAD_STARTUP_FILE` is set, wait until that
     file exists (30s timeout) before running;
   - if `IORMESH_WORKLOAD_HOLD_FILE` is set, stay alive after the
     scenario until that file exists (120s safety timeout) — the
     harness uses it to keep the target alive past its I/O;
   - `Marker.begin` → `scenario.run` → `Marker.end`.

4. The two M1 scenarios:
   - `file-basic`: create a temp file, write 4096 bytes, read them
     back, close. Uses `FileOutputStream`/`FileInputStream`.
   - `tcp-loopback`: `--role server` binds `127.0.0.1:<port>`,
     accepts one connection, reads 5 bytes, writes 5 bytes;
     `--role client` connects, writes, reads. Each side also runs
     `file-basic`'s file work, so both PIDs have file edges.
     Use `java.net.Socket` on a platform thread — virtual threads
     come with M3's scenarios.

   Both take `--dir D` (the harness passes its tmp dir, which the
   normalizer maps to `$tmp`; default: a fresh temp dir, removed
   after) and write `<scenario>[-<role>].dat` there. The server's
   `--port` defaults to 0 (kernel-picked) and it prints
   `listening <port>` as the stdout line after the PID; the
   harness starts or releases the client (`--port` required) only
   after that line. Bad options or roles exit 2.

5. `expect.json` per scenario, in the format above.

**Tests**: a JUnit-free self-test mode — `java -jar workload.jar
--self-test` runs every scenario without a tracer and exits
non-zero on an exception; `mage workloadTest` wraps it, then
checks a gated run (PID first on stdout; no marker before the
startup file appears; still alive after the scenario until the
hold file appears), that `--expect noop` prints valid JSON, and
that malformed invocations — unknown scenario or role, a missing
or duplicate option, a stray argument — exit with the usage
code 2. `--list` prints the registered scenario names.

**Done when**: PLAN.md's M0 acceptance holds — the M1 scenarios
run standalone and `strace -f -e trace=openat` shows the two
marker opens around each scenario.

## M1 — walking skeleton

**Goal**: every layer, once: probes → correlator → NDJSON →
normalizer → golden check → integration harness, on `file-basic`
and `tcp-loopback`. PLAN.md: "M1 — Walking skeleton".

**Step 1 — lift the build and generator from ior.** Copy with
provenance headers: `Magefile.go` (keep `Build`, `Generate*`,
`Test`, `Vet`, `Lint`, `Fmt*`, `IntegrationTest*`, `BpfBuild`;
drop TUI/demo/bench targets), `internal/generate/`,
`internal/tracepoints/`, `internal/types/`, `internal/bpfembed.go`
(rename the env override to `IORMESH_BPF_OBJECT`),
`internal/c/{maps.h,types.h,filter.c,fdname.c,fileident.c}`,
`internal/c/vmlinux.h`, `scripts/build-with-docker.sh`. First
commit: the copied tree builds and `mage generate` passes its diff
gate with ior's full syscall set.

**Step 2 — narrow the attach set, not the generator.** Keep the
generated handlers for all syscalls; M1 attaches only
`open`/`openat`/`openat2`, `read`, `write`, `close`, `connect`,
`accept`/`accept4`. ior already selects tracepoints at runtime
(`internal/tracepoints/selector.go`, `-trace-syscalls`) — reuse
that, with M1's set as the default.

**Step 3 — the target filter becomes a map.** ior filters on one
TGID baked into rodata (`PID_FILTER`, `internal/c/filter.c`).
iormesh needs several targets now and a process tree in M2, so
the gate is a hash map userspace fills:

```c
// maps.h — tgids being traced. Userspace inserts the session's
// targets; M2's fork handler inserts their children.
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u32);   // tgid
    __type(value, __u8);
} target_map SEC(".maps");

// filter.c — returns FILTER for tasks outside the traced set.
static __always_inline int filter(__u32 *pid, __u32 *tid) {
    __u64 id = bpf_get_current_pid_tgid();
    *pid = id >> 32;
    *tid = (__u32)id;
    return bpf_map_lookup_elem(&target_map, pid) ? ACCEPT : FILTER;
}
```

Keep ior's own-PID exclusion (`IOR_PID_FILTER`) so the tracer
never traces itself.

**Step 4 — TCP 4-tuples from the socket.** New file
`internal/c/sock.c`. Three pieces:

```c
// sock_owner_map: which traced process and fd a socket belongs
// to. Filled at connect-enter and accept-exit; the state
// tracepoint only reports sockets found here.
struct sock_owner { __u32 tgid; __s32 fd; };
struct {
    __uint(type, BPF_MAP_TYPE_LRU_HASH);
    __uint(max_entries, 65536);
    __type(key, __u64);                 // struct sock *
    __type(value, struct sock_owner);
} sock_owner_map SEC(".maps");

struct tcp_state_event {
    __u32 event_type;       // TCP_STATE_EVENT
    __u32 schema_version;
    __u64 time;             // bpf_ktime_get_boot_ns()
    __u64 sk;               // join key back to pid/fd
    __u32 tgid;
    __s32 fd;
    __s32 oldstate, newstate;
    __u16 family, sport, dport, _pad;
    __u8  saddr[16], daddr[16];   // v4 in the first 4 bytes
};

SEC("tracepoint/sock/inet_sock_set_state")
int handle_inet_sock_set_state(
        struct trace_event_raw_inet_sock_set_state *ctx) {
    if (ctx->protocol != IPPROTO_TCP)
        return 0;
    // This tracepoint also fires in softirq context, where
    // "current" is whichever task was interrupted. Never filter
    // or attribute by current here — only by the socket.
    __u64 sk = (__u64)ctx->skaddr;
    struct sock_owner *owner = bpf_map_lookup_elem(&sock_owner_map, &sk);
    if (!owner)
        return 0;
    struct tcp_state_event *ev =
        bpf_ringbuf_reserve(&event_map, sizeof(*ev), 0);
    if (!ev) {
        ior_count_ringbuf_drop();
        return 0;
    }
    ev->event_type = TCP_STATE_EVENT;
    ev->schema_version = TCP_STATE_EVENT_SCHEMA_VERSION;
    ev->time = bpf_ktime_get_boot_ns();
    ev->sk = sk;
    ev->tgid = owner->tgid;
    ev->fd = owner->fd;
    ev->oldstate = ctx->oldstate;
    ev->newstate = ctx->newstate;
    ev->family = ctx->family;
    ev->sport = ctx->sport;
    ev->dport = ctx->dport;
    iormesh_copy_addrs(ev, ctx);     // v4 or v6 by family
    bpf_ringbuf_submit(ev, 0);
    if (ctx->newstate == TCP_CLOSE)
        bpf_map_delete_elem(&sock_owner_map, &sk);
    return 0;
}
```

- **connect (active side)**: in the `connect` enter hook, resolve
  fd → `struct file` → `struct socket` → `struct sock` (ior's
  `fdname.c` already walks fd → file and recognizes sockets;
  extend it with `iormesh_fd_to_sock`) and insert into
  `sock_owner_map`. The `SYN_SENT → ESTABLISHED` transition then
  delivers the full 4-tuple, including the local port the syscall
  never sees.
- **accept (passive side)**: the child socket reaches
  `ESTABLISHED` in softirq *before* `accept()` returns, so no
  owner is registered yet and the transition is not reported. In
  the `accept`/`accept4` exit hook, resolve the returned fd to its
  sock, insert the owner, and read the tuple straight from
  `sk->__sk_common` (`skc_rcv_saddr`, `skc_daddr`, `skc_num`,
  `skc_dport`, v6 equivalents) into a `tcp_state_event` with
  `newstate = TCP_ESTABLISHED`.

**Step 5 — loader and ring buffer** (`internal/bpf`, cgo): lift
ior's module load, attach and ring-buffer poll
(`internal/bpfsetup.go`, the event loop's raw channel). Output of
this package is a channel of decoded raw records
(`internal/types`), nothing more.

**Step 6 — the correlator** (`internal/correlate`):

```go
// Correlator turns raw BPF records into schema events and a
// per-host graph. One goroutine owns all of its state.
type Correlator struct {
    host   schema.HostInfo
    procs  map[uint32]*procState // by tgid
    socks  map[uint64]*sockState // by struct sock address
    seq    uint64
    out    Sink
}

type procState struct {
    inc uint64               // pid incarnation (start_boottime)
    fds map[int32]*fdState
}

type fdState struct {
    gen  uint32              // bumped on every (re)creation
    path string              // files
    sk   uint64              // sockets: key into Correlator.socks
}

type sockState struct {
    local, remote netip.AddrPort
    established   uint64     // boot ns
    role          string     // "client" | "server"
}

// Sink receives finished events in seq order.
type Sink interface {
    Event(schema.Event) error
    Close() error
}
```

Rules to implement, each with a unit test:

- syscall enter/exit pairing — lift ior's pair tracker, including
  its restart handling, rather than rewriting it;
- `openat` exit with `ret >= 0` creates `fds[ret]` with
  `gen = previous + 1`; `close` removes it;
- `connect` enter and `accept` exit bind fd → `sk`;
  `tcp_state` events fill `sockState`;
- an `openat` on `/iormesh-marker/…` becomes a `marker` event;
- **loopback join**: two `sockState`s whose `(local, remote)` are
  each other's mirror, established within the same 5s, become one
  `conn:<id>` — `conn_id` is the hex SHA-256 (first 16 chars) of
  the sorted address pair plus the earlier establish time. Both
  processes get a `connected` edge to it.

**Step 7 — NDJSON sink and graph export**
(`internal/schema/ndjson.go`): one event per line, keys in struct
order (encoding/json already does this), then `graph.json` at
exit with sorted nodes and edges.

**Step 8 — the normalizer** (`internal/normalize`, cgo-free):

```go
// Normalize rewrites run-specific values to symbolic ids in
// order of first appearance, so two runs of one scenario compare
// equal. It never reorders events.
func Normalize(in []schema.Event, opt Options) []schema.Event

type Options struct {
    // Bracket cuts the output to the events between the begin
    // and end markers of this scenario (inclusive); empty keeps
    // everything.
    Bracket string
    // TmpPrefix is replaced by "$tmp" in every path.
    TmpPrefix string
}

// symbols hands out $p1, $t1, $fd1, $port1, … per kind.
type symbols struct {
    next map[string]int
    seen map[string]string // "pid:4711" → "$p1"
}
```

Normalized: `pid`, `tid`, `pid_incarnation`, `fd` (≥ 3 only —
0/1/2 are stable), ephemeral ports, inode-derived ids, `conn_id`,
`seq` (renumbered from 1), all times (replaced by the event's
rank). Not normalized: event names, paths under known prefixes,
byte counts, return values, well-known ports.

**Step 9 — the golden check** (same package):

```go
// Check asserts declared ⊆ collected on a normalized, bracketed
// trace. It returns every violation, not just the first.
func Check(got []schema.Event, graph schema.Graph,
    want Expectation) []Violation
```

Algorithm: per declared thread, walk the collected events of each
candidate thread in order and match greedily; bind symbols on
first use; backtrack over candidate threads only. Then check
edges, then scan every string field of every event for
`must_not_appear` entries.

**Step 10 — CLI** (`cmd/iormesh`): subcommands `run` (foreground
tracer: `-pid`, `-project`, `-duration`, `-out DIR`) and
`normalize` (`-bracket`, `-check expect.json`). `main.go` only
dispatches.

**Step 11 — integration harness**: lift
`integrationtests/harness.go`, `parse.go`, `kernelloss.go`,
`unexercised.go`, `kernel_support_test.go` and the `gatecmd`
runner from ior; swap `ioworkload` for `java -jar workload.jar`.
One test:

```go
func TestWalkingSkeleton(t *testing.T) {
    h := newHarness(t)
    port := h.FreePort() // both start before either runs
    server := h.StartWorkload("tcp-loopback", "--role", "server",
        "--port", port)
    client := h.StartWorkload("tcp-loopback", "--role", "client",
        "--port", port)
    // Releases the server, then the client once the server printed
    // its "listening" line.
    trace := h.Trace(server.PID, client.PID)
    got := normalize.Normalize(trace.Events,
        normalize.Options{Bracket: "tcp-loopback", TmpPrefix: h.Tmp})
    for _, v := range normalize.Check(got, trace.Graph,
        h.Expectation("tcp-loopback")) {
        t.Error(v)
    }
    if n := countEdges(trace.Graph, "connected", "fused"); n != 2 {
        t.Fatalf("want one fused connection (2 edges), got %d", n)
    }
}
```

**Done when**: PLAN.md's M1 acceptance holds;
`mage integrationTest` passes ten times in a row; a second
normalized run diffs empty against the first inside the bracket.

## S3 — overhead and volume budget

**Goal**: replace decision #25's placeholders with measurements.
Runs on the M1 build. Result: `docs/spikes/s3-overhead.md`.

1. Lift `scripts/perf-baseline.sh` and the `perf/` layout from
   ior.
2. Add a load scenario to the workload: `syscall-burst --ops N
   --threads T` (tight 1-byte read/write loop on a temp file —
   the worst case per syscall).
3. Measure, three runs each, idle host:
   - **target slowdown**: `syscall-burst` wall time untraced vs
     traced;
   - **bystander cost**: the same burst run by a process *outside*
     `target_map` while a trace is active — this is the host-wide
     tax of classic syscall tracepoints that ior documents in
     `docs/raw-tracepoint-design.md`;
   - **tracer CPU**: `utime+stime` of the tracer over the run;
   - **loss**: `drop` events per million syscalls at 1, 4 and 16
     threads;
   - **volume**: NDJSON and parquet bytes per million events.
4. Write the four numbers into decision #25, and recommend: the
   default event families (#7), whether a size cap must force
   early rotation (#16), and whether hot read/write loops need
   in-kernel aggregation (ior's `syscall_aggregate_map` is the
   model).

## M2 — full trace surface

**Goal**: everything in PLAN.md's "What we trace" except Java.
Work in this order — each item is one PR-sized unit with its
scenarios landing first (principle #1).

1. **Process tree.** ior's `internal/c/exec.c` already handles
   `task:task_newtask`, `sched_process_exec` and
   `sched_process_exit` — but only to *report* tasks; its filter
   drops a forked child because the child has a new tgid. Adapt
   the new-task handler so a traced parent's child joins the
   traced set:

   ```c
   // Runs in the parent's context. A new thread shares its
   // creator's tgid and is covered already; a new process is
   // inserted here, before it can make its first syscall.
   SEC("tracepoint/task/task_newtask")
   int handle_task_newtask(void *raw) {
       struct trace_event_raw_task_newtask___ior *ctx = raw;
       __u32 parent = bpf_get_current_pid_tgid() >> 32;
       if (!bpf_map_lookup_elem(&target_map, &parent))
           return 0;
       // vmlinux.h carries no macros: define the flag next to
       // ior's IOR_CLONE_FILES (0x00010000 is CLONE_THREAD).
       if (BPF_CORE_READ(ctx, clone_flags) & IORMESH_CLONE_THREAD)
           return 0;
       __u32 child = BPF_CORE_READ(ctx, pid);
       __u8 one = 1;
       bpf_map_update_elem(&target_map, &child, &one, BPF_ANY);
       // then emit ior's task_newtask_event as before
       return 0;
   }
   ```

   Keep ior's CO-RE "flavor" struct (`___ior` suffix) for the
   context — it is what keeps the object buildable on hosts whose
   `vmlinux.h` differs. Remove exited tgids from `target_map` in
   the exit handler. Scenario: `fork-exec` (the workload starts a
   child JVM via `ProcessBuilder`).

2. **Target spec** (`internal/target`):

   ```go
   // Spec says which processes a session traces.
   type Spec struct {
       PIDs       []uint32
       Pattern    string // glob over "exe argv…" (decision #23)
       ListenPort uint16 // whoever owns this listening socket
   }

   // Resolve returns the tgids matching right now; the exec
   // handler calls Matches for every later exec.
   func (s Spec) Resolve(proc ProcFS) ([]uint32, error)
   func (s Spec) Matches(exe string, argv []string) bool
   ```

   Pattern sessions leave `target_map` empty until a match; the
   exec event is evaluated in userspace and the tgid inserted.
   The window between exec and insert loses the first syscalls of
   a restarted app — record it as a `drop` event with reason
   `attach_gap`, never silently. Scenario: `restart`.

3. **App identity**: all incarnations matched by one pattern share
   an `app:<project>/<pattern>` node with `incarnation` edges.

4. **File surface**: attach the remaining file families (ior's
   family table, `internal/tracepoints/family.go`). For each
   family, port ior's test from `integrationtests/<family>_test.go`
   with the Java scenario swapped in.

5. **Rename-aware paths**: on `rename*` exit, rewrite `path` of
   every open `fdState` under the old name; unit-test directory
   renames and cross-directory moves.

6. **fd lifecycle**: `dup*`, `fcntl(F_DUPFD*)` copy `fdState`
   (new `gen`); fork copies the parent's table; exec drops
   `O_CLOEXEC` fds.

7. **IPC, signals, UDP, epoll**: as families, default-off per S3's
   recommendation.

8. **Redaction** (`internal/redact`, cgo-free — M3 and M5 reuse
   it):

   ```go
   // Policy masks secrets in argv, URIs and headers before
   // anything is written. Deny wins over allow.
   type Policy struct {
       ArgvDeny   []string // globs: "-D*password*", "--token*"
       HeaderDeny []string // "authorization", "cookie", …
       QueryDeny  []string // parameter names
   }

   func (p Policy) Argv(argv []string) []string
   ```

   A masked value becomes `"***"`; `--token X` masks the *next*
   element too. Scenario: `argv-secret`, with the planted secret
   in `must_not_appear`.

9. **Native stacks** (opt-in, `-native-stacks`):
   `bpf_get_stackid` into a `BPF_MAP_TYPE_STACK_TRACE`,
   symbolized in userspace from `/proc/<pid>/maps`. Off by
   default; documented as not useful for JIT frames.

10. **Non-Java peer**: build ior's `ioworkload` as the peer for
    the `non-java-peer` scenario; assert `attribution: none`.

**Done when**: PLAN.md's M2 acceptance holds, including the S3
budget as a `mage perfGate` target.

## S1 — Java↔syscall join

**Goal**: prove that a Java stack can be joined to the syscalls it
caused, and what that costs. Throwaway code under `spikes/s1/`;
result in `docs/spikes/s1-java-join.md`. Everything below is
*hypothesis* until the spike says otherwise.

**Constraint that shapes the design**: the JDK's I/O classes are
loaded before any agent runs, and retransformation cannot add,
remove or rename methods. So hooks are pure *body rewrites* of
existing methods — the native-method-prefix trick is unavailable.

**Experiment 1 — the hook set.** Instrument the thin Java methods
that directly call the native I/O functions. Starting list for
JDK 21 (verify each against the JDK sources — these internal names
move between releases, which feeds decision #29):

| Class | Methods | Covers |
|-------|---------|--------|
| `java.io.FileInputStream` | `open`, `read`, `readBytes` | stream reads |
| `java.io.FileOutputStream` | `open`, `write`, `writeBytes` | stream writes |
| `java.io.RandomAccessFile` | `open`, `read*`, `write*` | random access |
| `sun.nio.ch.FileDispatcherImpl` | `read`, `write`, `pread`, `pwrite` | `FileChannel` |
| `sun.nio.ch.SocketDispatcher` | `read`, `write`, `readv`, `writev` | sockets |
| `sun.nio.ch.Net` | `connect`, `accept`, `bind`, `listen` | lifecycle |
| `sun.nio.fs.UnixNativeDispatcher` | `open`, `rename`, `unlink`, … | `Files.*` |

Hooking *this low* is deliberate: the bracket around the native
call is as tight as it gets, and the captured stack still
contains every caller above. (It is also what would later make
virtual threads tractable — they cannot park inside such a
bracket — but virtual-thread attribution is deferred; v1 only
flags carrier-thread I/O. See PLAN.md, "Deferred — not in v1".)

**Experiment 2 — the agent skeleton.**

```java
public final class Agent {
    public static void premain(String args, Instrumentation inst) {
        install(args, inst);
    }
    public static void agentmain(String args, Instrumentation inst) {
        install(args, inst);          // live attach, decision #2
    }

    private static void install(String args, Instrumentation inst) {
        // java.base classes can only call code the bootstrap
        // loader sees, so the hook entry points live in a tiny
        // separate jar appended to its search path.
        inst.appendToBootstrapClassLoaderSearch(bridgeJar(args));
        HookTable table = HookTable.v1();
        inst.addTransformer(new BoundaryTransformer(table), true);
        inst.retransformClasses(table.loadedTargets(inst));
    }
}
```

```java
/** Loaded by the bootstrap loader; called from rewritten JDK code. */
public final class Hooks {
    private static final ThreadLocal<Boolean> IN_HOOK =
        ThreadLocal.withInitial(() -> false);

    /** Returns a token for exit(), or 0 when the hook is skipped. */
    public static long enter(int hookId) {
        if (IN_HOOK.get()) return 0;   // the agent's own I/O
        IN_HOOK.set(true);
        try {
            return Spool.begin(hookId, System.nanoTime(),
                StackIds.current());
        } finally {
            IN_HOOK.set(false);
        }
    }

    public static void exit(int hookId, long token) {
        if (token != 0) Spool.end(token, System.nanoTime());
    }
}
```

`BoundaryTransformer` uses ASM (shaded into the agent jar — the
agent may have dependencies, the workload may not) to wrap each
target method body in `enter`/`try`/`finally exit`.
`StackIds.current()` walks with `StackWalker`, hashes the frames,
and writes a `STACK` record the first time a hash is seen.

**Experiment 3 — the join key.** Front-runner first, then the
agent-only fallbacks.

- **(u) uprobes + TID, all in eBPF.** Verified on 2026-11-18
  (JDK 25, Fedora, bpftrace): a uprobe/uretprobe pair on
  `Java_java_io_FileOutputStream_writeBytes` (`libjava.so`) and
  `Java_sun_nio_ch_UnixFileDispatcherImpl_write0` (`libnio.so`)
  set and cleared a per-TID map entry; the `sys_enter_write`
  handler found the entry for all 12 of the test's writes — 6 on
  a platform thread, 6 on a virtual thread's carrier — and for
  none of the host's other writes. Exact, no clocks, no agent.

  ```
  uprobe:…/libnio.so:Java_sun_nio_ch_UnixFileDispatcherImpl_write0
      { @in[tid] = probe; }
  uretprobe:…/libnio.so:Java_sun_nio_ch_UnixFileDispatcherImpl_write0
      { delete(@in[tid]); }
  tracepoint:syscalls:sys_enter_write /@in[tid] != ""/ { … }
  ```

  What it does *not* give: Java identity. The kernel sees the
  carrier (`ForkJoinPool-1-…`), not the virtual thread's name,
  and a BPF stack walk returned only unsymbolized addresses for
  the Java frames (interpreter frames in the test, run with
  `-XX:+PreserveFramePointer`). Still to settle: how the Java
  stack and thread reach the bracket — the agent (then: how its
  record meets the uprobe's TID), or reading HotSpot's frames
  from eBPF as profilers do; uprobe cost per I/O op; and JDK
  builds whose libraries are stripped or statically linked.

- **(a) fd + time window.** The hook knows the Java-level fd
  (`FileDescriptor.fd`). The correlator matches syscalls on
  `(pid, fd)` whose enter/exit fall inside the bracket. No TID
  needed; the matched syscall then *teaches* the TID↔Java-thread
  mapping. Ambiguous only when two threads use one fd at once —
  detectable as overlapping brackets, then reported as
  `attribution: none`. Opens match on path instead of fd.
- **(b) kernel TID without native code.** `readlink
  /proc/thread-self` yields `<pid>/task/<tid>`; cache it per
  carrier thread. Costs one syscall per thread, not per I/O.
- **(c) a per-thread slot BPF reads** at syscall time. Exact and
  agent-clock-free, but needs native memory and a uprobe or a
  registered address. Only if (a) and (b) fail.

**Experiment 4 — clocks.** ior stamps BPF records with
`bpf_ktime_get_boot_ns()` (CLOCK_BOOTTIME); `System.nanoTime()` is
CLOCK_MONOTONIC on Linux. They differ by time spent suspended.
The tracer samples `boottime − monotonic` at start and on every
agent watermark and converts brackets to the boot clock (ior's
`internal/bootclock.go` already handles boot-clock domains).
Verify the residual error is far below a syscall's duration.

**Experiment 5 — transport** (decision #27). Compare:

- an append-only spool file — simple, but every flush is a
  `write` the tracer then sees as the target's I/O and must
  filter by path;
- a memory-mapped ring (`FileChannel.map`, single writer) — no
  syscalls on the hot path, so no self-noise.

Record types either way:

```
BRACKET   hook_id, tid|fd, enter_ns, exit_ns, stack_id, thread_id
STACK     stack_id, n, method_id[n]
METHOD    method_id, class, method, descriptor, file, line, jar
THREAD    thread_id, name, is_virtual
WATERMARK mono_ns        // "everything before this is written"
```

**Experiment 6 — cost.** `syscall-burst` from S3 through
`FileOutputStream`: untraced, agent only, agent + tracer. Report
ns per I/O op and the share spent in the stack walk.

**Experiment 7 — attach.** Confirm on JDK 21: `-javaagent` works
silently; live attach prints the JEP 451 warning; what
`-XX:-EnableDynamicAgentLoading` does; what a root-run tracer must
do to attach to a JVM owned by another uid.

**Exit**: PLAN.md's S1 exit criterion, plus a recommendation for
each of decisions #2, #6, #27 and #29.

## M3 — Java agent

**Goal**: S1's prototype, productionized. PLAN.md: "M3 — Java
agent". Assumes S1 confirmed the design; where it did not, its
report replaces the affected steps.

**Files**

```
agent/
  bridge/src/iormesh/bridge/Hooks.java       # bootstrap-visible
  core/src/iormesh/agent/Agent.java          # premain/agentmain
  core/src/iormesh/agent/HookTable.java      # class → methods → id
  core/src/iormesh/agent/BoundaryTransformer.java
  core/src/iormesh/agent/StackIds.java
  core/src/iormesh/agent/Spool.java
  core/src/iormesh/agent/http/…              # one file per library
  core/src/iormesh/agent/otel/SpanContext.java
internal/agentspool/    # Go reader for the spool (cgo-free)
internal/correlate/javajoin.go
```

**Steps**

1. Build: `mage agentBuild` → `iormesh-agent.jar` with the bridge
   jar nested as a resource; ASM shaded under
   `iormesh.agent.shaded`.
2. `HookTable` is data, not code: a table of
   `(class, method, descriptor, hookId, kind)` so decision #29's
   per-JDK differences become table variants.
3. The Go side joins brackets to events:

   ```go
   // joiner attributes syscall events to Java brackets. If S1
   // settles on the uprobe join, the bracket arrives from BPF
   // already tied to the syscall and this shrinks to a lookup
   // of the agent's stack for that bracket. Both
   // streams are ordered per key, so one cursor per key is
   // enough; nothing is ever attributed by guessing.
   type joiner struct {
       pending map[joinKey][]agentspool.Bracket // sorted by enter
       stacks  map[uint32][]uint32              // stack id → methods
       mark    uint64                           // agent watermark
   }

   // attribute sets ev.Attribution and ev.Java. An event newer
   // than the watermark is held back: its bracket may not have
   // been written yet.
   func (j *joiner) attribute(ev *schema.Event) (ready bool)
   ```

   Events are released in `seq` order once the watermark passes
   them, or after a 2s timeout when no agent is attached — then
   `attribution: none`.
4. Method table and thread table are emitted as their own NDJSON
   record kinds and parquet files (`methods`, `threads`), keyed by
   id.
5. HTTP hooks: one transformer per library, each emitting an
   `http` event at request completion with `conn_id` (via the
   socket's fd) and `req_seq` (per-connection counter). Start
   with the JDK's `HttpClient` and built-in `HttpServer` — the
   pair the workload uses — then widen per decision #22.
   Headers pass through `redact.Policy` *in the agent*, before
   the spool.
6. OTEL: if `io.opentelemetry.api.trace.Span` is loadable in the
   app's class loader, read `Span.current().getSpanContext()`
   reflectively at `enter` and add ids to the bracket. No OTEL
   dependency in the agent.
7. Scenarios first, as always: `method-flow`,
   `http-plain`, `http-tls`, `http-redaction`, `otel`.

**Done when**: PLAN.md's M3 acceptance holds.

## S2 — cross-host join

**Goal**: a join key that survives NAT, and clock bounds from the
traffic. Throwaway code under `spikes/s2/`; result in
`docs/spikes/s2-cross-host-join.md`. *Hypotheses* throughout.

**Test rig** — two "hosts" on one machine:

```sh
ip netns add hostA; ip netns add hostB; ip netns add nat
# veth pairs hostA↔nat and nat↔hostB, then in nat:
ip netns exec nat nft add rule ip nat prerouting \
    tcp dport 8080 dnat to 10.0.2.2:8080
```

One tracer per namespace's process set, each with its own
`-host-id` override and `-test-clock-offset` (added by this
spike; test-only).

**Experiment 1 — ISN pair as key.** NAT rewrites addresses and
ports but not TCP sequence numbers. At the `ESTABLISHED`
transition read, from `struct tcp_sock`:

```c
// hypothesis: right after the handshake, before any data,
//   iss = snd_una - 1   (our initial sequence number)
//   irs = rcv_nxt - 1   (the peer's)
__u32 iss = BPF_CORE_READ(tp, snd_una) - 1;
__u32 irs = BPF_CORE_READ(tp, rcv_nxt) - 1;
```

The client's `(iss, irs)` must equal the server's `(irs, iss)`.
Open questions the spike must answer:

- the passive side reaches `ESTABLISHED` before `accept()`, when
  no owner is registered (see M1 step 4). Can the pair be
  reconstructed later from `snd_una − bytes_acked` and
  `rcv_nxt − bytes_received` (mod 2³²)? Verify the off-by-one for
  the SYN on both sides.
- TCP Fast Open carries data in the SYN — does the arithmetic
  hold?
- what breaks it: SYN proxies, sequence-randomizing firewalls,
  any L4 proxy that terminates the connection.

**Experiment 2 — conntrack as the alternative**: read the
original↔reply tuple mapping on the NAT host. Works only where a
tracer or conntrack access exists on that host.

**Experiment 3 — clock bounds.** For hosts A and B with offset
θ = clock_B − clock_A, every message gives one inequality:

```
A→B message:  θ < recv_B − send_A
B→A message:  θ > send_B − recv_A
```

`send` is the `write` *enter* time, `recv` the `read` *exit* time
of the same stream bytes (matched by cumulative byte offset on
the connection). Intersect over all messages; report the interval
width on loopback, veth and a real LAN.

**Exit**: PLAN.md's S2 exit criterion, plus a pick for decision
#26.

## M4 — controller

**Goal**: `add` → `deploy` → `refresh` → `rm`, merge on the
laptop. `collect` and `build` are separate commands — sync only,
and offline build only — and `refresh` is the two in a row;
keep them separate in code too (`collect.go` never imports the
merge). PLAN.md: "M4 — Controller".

![Operator workflow](diagrams/workflow.svg)

### Tracer side

Subcommands of `/opt/iormesh/bin/iormesh` (the only thing the
sudoers rule names):

| Subcommand  | Does |
|-------------|------|
| `run`       | the foreground tracer (also standalone mode) |
| `start`     | launches `run` as a transient systemd unit |
| `stop`      | `systemctl stop iormesh-<project>` |
| `status`    | prints state from `<trace-dir>/<project>/state.json` |
| `upgrade F` | verifies staged binary `F`, replaces itself (#1, #28) |
| `uninstall` | stops every unit, removes `/opt/iormesh` |

```go
// startUnit launches the tracer under systemd so it outlives the
// SSH session and is capped by its cgroup. RuntimeMaxSec is the
// second, independent bound next to the tracer's own -duration.
func startUnit(cfg RunConfig) error {
    args := []string{
        "--unit=iormesh-" + cfg.Project, "--collect",
        "--property=RuntimeMaxSec=" + cfg.maxRuntime(), // duration+5m
        "--property=CPUQuota=" + cfg.CPUQuota,          // decision #25
        "--property=MemoryMax=" + cfg.MemoryMax,
        "--property=KillSignal=SIGTERM",
        "--property=TimeoutStopSec=30",
        selfPath(), "run",
    }
    return runCommand("systemd-run", append(args, cfg.runFlags()...)...)
}
```

Segment writer (`internal/segment`):

```go
// Writer is a schema.Sink that rotates parquet segments. Only
// the active file is ever open; a finished one is renamed into
// ready/, which makes it immutable and safe to rsync.
type Writer struct {
    dir, fqdn, session string
    seq                int
    active             *parquet.GenericWriter[schema.Event]
    start              time.Time // active segment opened; names it
    rotateAt           time.Time
    watchdog           DiskWatchdog
}

func (w *Writer) rotate() error {
    if err := w.active.Close(); err != nil { // writes the footer
        return err
    }
    name := fmt.Sprintf("%s-%s-%s-%06d.parquet",
        w.fqdn, w.session, w.start.UTC().Format("20060102T150405Z"), w.seq)
    // Same filesystem, so the rename is atomic: a reader sees the
    // whole segment or none of it.
    if err := os.Rename(w.activePath(), filepath.Join(w.dir, "ready", name)); err != nil {
        return err
    }
    w.seq++
    return w.openNext()
}
```

- Compression: `parquet.Compression(&parquet.Zstd)` on the writer.
- `ready/` is created owned by the deploying user (`start` passes
  `SUDO_UID`/`SUDO_GID` on to `run` as `-ready-owner`).
- `DiskWatchdog.Check()` runs on a ticker and before each rotate:
  `statfs` on the trace dir; over the limit → flush, write the
  `end` event with reason `disk_limit`, exit 0.
- On SIGTERM: rotate once (flushing the active segment), write
  `end` with reason `stop`.

### Controller side

Registry, `~/.config/iormesh/servers.yaml`:

```yaml
servers:
  - host: app1.example.org
    projects:
      - name: shop
        target: {pattern: "*java*orders-service*"}
        flags: ["-duration", "4h"]
expand:
  allow: ["*.example.org", "10.20.0.0/16"]
  max_nodes: 20
```

`deploy`, as the commands it runs (all through one multiplexed
SSH connection per host):

```sh
ssh H 'uname -m; command -v rsync systemd-run'      # preflight
# verify the local build's signature (decision #28), then:
rsync -a iormesh-linux-$ARCH H:/tmp/iormesh.staged
ssh H sudo /opt/iormesh/bin/iormesh upgrade /tmp/iormesh.staged
ssh H sudo /opt/iormesh/bin/iormesh start -project shop \
    -target-pattern '*java*orders-service*' -duration 4h
```

(First install on a host has no binary to `upgrade` with — that
bootstrap is decision #1.)

`collect`, per host and project:

```sh
rsync -a --remove-source-files \
    H:/opt/iormesh/traces/shop/ready/ \
    ~/.local/share/iormesh/traces/shop/
```

Packages: `internal/federate/registry.go`, `ssh.go` (wraps the
`ssh`/`rsync` binaries — no SSH library, the operator's
`~/.ssh/config` must just work), `collect.go`, `merge.go`.

### The merge

```go
// Half is one end of a TCP connection as one host recorded it.
type Half struct {
    Host        string
    Proc        string         // node id
    Role        string         // "client" | "server"
    Local, Peer netip.AddrPort
    Key         JoinKey        // S2's NAT-tolerant key, may be zero
    Established uint64         // that host's boot ns
}

// Fuse pairs client and server halves into cross-host edges.
// Order of preference: join key, then mirrored 4-tuple within
// the time window. A half without a partner is returned
// unresolved — it stays in the graph and feeds M6.
func Fuse(halves []Half, window time.Duration) (
    edges []schema.Edge, unresolved []Half)
```

Rules, each a table-driven test:

- a key match beats a tuple match; two candidates for one half is
  an error surfaced in the summary, never a coin toss;
- tuple matching compares the client's `(Local, Peer)` with the
  server's `(Peer, Local)`;
- port reuse: the same tuple twice must be separated by its
  `Established` times;
- fused edges get `State: "fused"` and a `conn_id` shared by both
  hosts' events.

Clock offsets (`internal/federate/clock.go`) implement S2's
inequalities per host pair and attach `{min, max}` to the merged
graph as a `clock_bound` record. Cross-host ordering is a
topological sort over per-thread order plus send→receive edges
(`internal/federate/order.go`); timestamps only break ties
between causally unrelated events, and such ties are flagged.

### Service database

`internal/servicedb`, sqlite through a pure-Go driver
(`modernc.org/sqlite` — keeps the controller cgo-free):

```sql
CREATE TABLE endpoint (
  id INTEGER PRIMARY KEY,
  project TEXT NOT NULL, host TEXT NOT NULL,
  hostname TEXT, ip TEXT NOT NULL, port INTEGER NOT NULL,
  UNIQUE (project, ip, port));
CREATE TABLE interaction (
  client_app TEXT NOT NULL, endpoint_id INTEGER NOT NULL,
  tls INTEGER NOT NULL, http_host TEXT,
  first_seen INTEGER NOT NULL, last_seen INTEGER NOT NULL,
  connections INTEGER NOT NULL,
  PRIMARY KEY (client_app, endpoint_id));
CREATE TABLE naming_rule (
  id INTEGER PRIMARY KEY, host_glob TEXT NOT NULL,
  port INTEGER, service TEXT NOT NULL);   -- human input: never
                                          -- touched by build
```

`build` only ever upserts `endpoint` and `interaction`
(`ON CONFLICT … DO UPDATE` widening `first_seen`/`last_seen`,
adding to `connections` by segment — track merged segment names
in a fourth table so rebuilding is idempotent). Names are
resolved at read time by joining `naming_rule`, so a new rule
applies retroactively without rewriting anything.

**Done when**: PLAN.md's M4 acceptance holds, driven by
`integrationtests/multihost_test.go` on two network namespaces
plus one run on two real hosts.

## M5 — views, query, eval

The three surfaces — local site, terminal UI, plain text — and
the view model they share are designed in
[visualization.md](visualization.md), including the build order
(V1 lands with M4, V2–V4 here). Build `internal/viewmodel` and
its goldens first; a renderer never reads the merged graph
directly.

![The two joins behind one line of the sequence view](diagrams/join.svg)

**Determinism rules** for everything in `internal/render` — a
test renders each golden twice and compares bytes:

- input is a *normalized* graph; iterate only over sorted slices;
- layout is a pure function of the sorted order — no force
  simulation, no randomness, no clock;
- coordinates are integers; where division is needed, round once
  and format with `strconv.Itoa`;
- no timestamps, hostnames of the rendering machine, or version
  strings in the output;
- escape all text through one function.

```go
// Layout assigns positions purely from the graph's sorted order:
// hosts become columns, node types become rows within a host.
func Layout(g schema.Graph) Placed

// SVG serializes a layout. Same Placed in, same bytes out.
func SVG(p Placed) []byte

// Sequence renders conversations: one lifeline per service, one
// arrow per fused request, file ops as notes on the lifeline of
// the thread that did them.
func Sequence(g schema.Graph, order []federate.Ordered) []byte
```

**Query** (`internal/query`, `iormeshctl query`):

```sh
iormeshctl query --project shop --thread 'http-*' \
    --from 12:00:00 --to 12:00:05 --max-bytes 200000
iormeshctl query --project shop --conn conn:4be1… --follow
iormeshctl query --project shop --file '/etc/orders.conf'
```

Output is schema NDJSON. When `--max-bytes` cuts the result, the
last line is `{"event":"truncated","args":{…remaining count…}}` —
a reader always knows it saw a partial answer. Implementation:
predicate pushdown onto the parquet columns (that is what the
nullable `args` columns are for), then a streaming merge by
`(host, seq)`.

**Eval** (`eval/`):

```yaml
# eval/questions/microservices.yaml
- id: order-file-writer
  scenario: microservices
  question: Which Java method wrote order-42.json?
  answer_contains: ["OrderStore#save"]
- id: gateway-chain
  scenario: microservices
  question: List the services a GET /checkout touches, in order.
  answer_sequence: ["gateway", "orders", "inventory", "billing"]
```

`mage eval` gives the model the summary rollup plus a `query`
tool, scores each answer with the declared matcher, and writes
`eval/results/<date>.json`. The model command is configurable;
the pass threshold is pinned when the question set is written
(PLAN.md, M5).

**Done when**: PLAN.md's M5 acceptance holds.

## M6 — mesh expansion

`internal/expand` (cgo-free). The whole feature is a pure
function plus a loop that calls existing M4 code:

```go
// Plan decides which unresolved peers to enroll in one round.
// It never touches the network.
func Plan(unresolved []federate.Half, reg Registry,
    allow Allowlist, lim Limits) (deploy []Candidate, skipped []Skip)

type Candidate struct {
    Host string // peer address as observed
    Port uint16 // becomes the tracer's -target-listen-port
    Via  string // which traced app revealed it
    Hop  int    // distance from the originally enrolled hosts
}

type Skip struct {
    Candidate
    Reason string // not_allowed | hop_limit | node_cap | enrolled
}

type Limits struct{ MaxHops, MaxNodes int }
```

`iormeshctl expand` prints `deploy` and `skipped` and stops.
`expand --apply` runs M4's `add` + `deploy` for each candidate,
recording `Hop` in the registry entry so the next round can
enforce the limit.

Tests (table-driven, no network): allowlist glob and CIDR
matching; hop counting across rounds; node cap; an already
enrolled host is skipped (this is what terminates A→B→A);
loopback and link-local peers are never candidates.

**Done when**: PLAN.md's M6 acceptance holds.

## M7 — Kubernetes

Later phase; only the shape is fixed here.

- `internal/k8s/cgroup.go`: parse `/proc/<pid>/cgroup` →
  pod UID + container id (cgroup v1 and v2 path forms; unit-test
  against captured strings from kind).
- Pod names: resolved on the controller through the API server
  (`kubectl get pods -o json`), joined by pod UID — the tracer
  never talks to the API.
- `deploy/sidecar-patch.yaml`: the strategic-merge patch adding
  the tracer container with `shareProcessNamespace: true`, the
  capabilities from decision #13 and a shared `emptyDir` for
  traces and the agent spool.
- `iormeshctl` gains a k8s transport implementing the same
  interface as SSH:

  ```go
  // Transport is how the controller reaches a tracer.
  type Transport interface {
      Run(ctx context.Context, argv ...string) ([]byte, error)
      Fetch(ctx context.Context, remoteDir, localDir string) error
  }
  ```

  M4 codes against this interface from day one, with SSH as the
  only implementation.
- ClusterIP join: S2's key if it survives kube-proxy's DNAT;
  otherwise conntrack on the node.
- `internal/routemap`: static YAML first, API discovery second
  (decision #15).

**Done when**: PLAN.md's M7 acceptance holds.
