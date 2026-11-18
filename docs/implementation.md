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
`internal/schema` (cgo-free, landed with M1 as the v1 *draft*) and
are the Go form of PLAN.md's "Trace output contract". The package
is the reference; the sketches below show its shape.

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
    Session       *Session `json:"session,omitempty" parquet:"session,optional"`
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
    Lost       *uint64 `json:"lost,omitempty" parquet:"lost,optional"`
    EndReason  *string `json:"end_reason,omitempty" parquet:"end_reason,optional"`
}
```

Field notes:

- `host_id` is `/etc/machine-id`; the session header's optional
  `session` object (`schema.Session`) adds boot id, FQDN, kernel
  release and arch.
- `pid_incarnation` is the task's start time on the boot clock
  (`task->start_boottime` via CO-RE) — unique per PID reuse.
- `seq` is per host, starts at 1 and has no gaps but explicit
  `drop` events; 0 means "never assigned" and is rejected.
- `fd` is the fd the event acts on: the argument, or for open*
  and accept* the returned one; `-1` (`schema.NoFD`) when there
  is none — synthetic kinds, a failed open or accept.
- `fd_gen` counts how often this fd number was (re)created in this
  process incarnation: +1 on *every* fd-creating syscall landing
  on it — open*, accept*, socket, dup*, pipe, … — so the
  correlator must see those syscalls even where it emits no
  event for them. Otherwise a library's open and a later
  `socket()` on the same number (tcp-loopback's client `$fd6`)
  would share one (pid, fd, fd_gen). `0` means the fd predates
  the trace, or there is no fd.
- `err` is the errno name and is only set when `ret < 0`.
- `event` is the syscall name, or one of the synthetic kinds:
  `tcp_state`, `marker`, `drop` (ring-buffer loss), `session`
  (header), `end` (why the trace ended).
- `attribution` is `"java"` or `"none"` — never empty. A
  `carrier` flag (added with M3) marks I/O that ran for a
  virtual thread, which v1 attributes to the carrier.
- `Java` (`thread`, `stack` of method ids; set exactly when
  `attribution` is `java`) is settled by S1 (thread name from the
  agent's `THREAD` record, stack ids from its `STACK` records);
  `HTTP` is provisional until M3. M1 assigns `conn_id` locally to fused loopback pairs;
  across hosts the merge assigns it from the ISN-pair join (S2,
  decision #26). The Java fields are nested
  (`java.thread`, `java.stack`) — settled by S1; PLAN.md's flat
  `java_thread`/`java_stack` were a sketch. `HTTP` stays
  provisional until M3.
- `schema.Sink` (`Event(Event) error`, `Close() error`) is what
  the correlator writes to; the NDJSON and parquet writers
  implement it.

Reading is strict: `schema.DecodeEvent` rejects unknown keys at
any depth — matched case-sensitively, since encoding/json alone
would let `"Comm"` overwrite `"comm"` — required keys (every
field without `omitempty`) that are missing or `null`, trailing
data, a `schema_version` other than `Version`, an unknown
`event` kind, a bad `attribution` (`java` needs a non-empty
`java.stack`), and a kind missing the argument it cannot be read
without (open* → non-empty `path`, `marker` → `marker`,
`tcp_state` → `sock_state`, `drop` → `lost` > 0, `end` → a known
`end_reason`, `session` → a complete `session` object, which no
other kind may carry). The kind list only grows: M2
appends its syscalls there.

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
    Type  string            `json:"type"` // host|process|file|socket, +later
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
`proc:<host_id>/<pid>@<pid_incarnation>`, `file:<host_id>:<path>`
(a colon, so an absolute path never makes a double slash),
`conn:<conn_id>`. A `socket` node is one connection — the socket
pair, not one end — and carries the `conn:` id, so both
processes of a fused pair point at the same node. Edge shapes:
`runs_on` process → host, `opened` process → file, `connected`
process → socket with `state` `fused` (both ends traced and
joined) or `unresolved` (one end seen); no other edge carries a
state. Slices are sorted (`Graph.Sort`) before every export; Go
map iteration order must never reach an output file.
`nodes` and `edges` are always arrays, never `null` (the encoder
writes `[]` for an empty graph). `schema.DecodeGraph` and
`Graph.Validate` reject unsorted or duplicate nodes and edges,
ids without their type's prefix (or a file id without
`<host>:<path>`), and edges to missing nodes or nodes of the
wrong type.

### Expectations (the golden format)

A scenario's expectation file sits next to its source in
`workload/scenarios/<name>/expect.json`. Symbolic ids (`$p1`,
`$t1`, `$fd1`, `$port1`, `$h1` for hosts, `$c1` for
connections) bind on first match;
the checker then requires consistency.

The shipped `file-basic` golden (write, close, reopen, read):

```json
{"scenario": "file-basic",
 "schema_version": 1,
 "events": [
   {"thread": "$t1", "event": "openat",
    "args": {"path": "$tmp/file-basic.dat", "flags": 577}, "fd": "$fd1"},
   {"thread": "$t1", "event": "write", "fd": "$fd1", "bytes": 4096},
   {"thread": "$t1", "event": "close", "fd": "$fd1"},
   {"thread": "$t1", "event": "openat",
    "args": {"path": "$tmp/file-basic.dat", "flags": 0}, "fd": "$fd2"},
   {"thread": "$t1", "event": "read", "fd": "$fd2", "bytes": 4096},
   {"thread": "$t1", "event": "close", "fd": "$fd2"}
 ],
 "edges": [
   {"type": "opened", "from": "$p1", "to": "file:$h1:$tmp/file-basic.dat"}
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
  `::ffff:127.0.0.1` unless the host lacks IPv6 (the normalizer
  unmaps it to `127.0.0.1` anyway, see M1 step 8). M2's
  `tcp-ipv4`, `tcp-ipv6` and `udp-loopback` do declare
  addresses: their NIO channels are opened with an explicit
  protocol family, so each socket has exactly one.
- A kind's fds beyond the one `fd` field (`pipe2`'s pair, a
  `dup2`'s target) are declared through the events that use
  them, as `fd-lifecycle` does for its `dup`.
- `flags` are compared by access mode plus the declared bits, so
  577 means "write-only, create, truncate" and tolerates an extra
  `O_CLOEXEC`; `0` matches any read-only open (M1 step 9).
- `attribution` (`"java"` or `"none"`), when declared, must equal
  the event's; unset, it is not compared. `non-java-peer`
  declares `none` on every event of its Go peer.
- `$a1` names an app node — the one node every incarnation of a
  pattern-matched app links to (M2 step 3). Only edges use it;
  it binds the whole node id, so `restart`'s two `incarnation`
  edges from one `$a1` assert one app identity, whatever the
  id's format turns out to be.

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
     accepts one connection, reads 5 bytes, writes 5 bytes, then
     reads until the client's EOF before closing (so the client
     always closes first and the TCP teardown is the same in every
     run — M1 step 11);
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
   after that line. Bad options or roles exit 2; a
   `--dir` that is not a directory exits 1. A scenario the
   host cannot run (M2's `tcp-ipv6` without `::1`) throws
   `ScenarioSkipped` and exits 3: the self-test prints `skip`,
   and a harness reports a skip, never a pass.

5. `expect.json` per scenario, in the format above.

**Tests**: a JUnit-free self-test mode — `java -jar workload.jar
--self-test` runs every scenario without a tracer and exits
non-zero on an exception; `mage workloadTest` wraps it, then
checks a gated run (PID first on stdout; no marker before the
startup file appears; still alive after the scenario until the
hold file appears) and a two-process tcp-loopback run (server on
`--port 0`, PID then `listening <port>` on stdout; the client
started on that port; both print the end marker and exit 0),
and runs M2's `udp-loopback` and `unix-socket` (`listening
<path>`, passed to the client as `--path`) the same way. It
also checks that `--expect` prints valid JSON naming the scenario
for every name `--list` prints, that malformed invocations —
unknown scenario or role, a missing or duplicate option, a stray
argument, a missing, non-numeric or out-of-range `--port`, an
unknown scenario option — exit with the usage code 2, and that
well-formed runs failing at run time — a client on a closed port,
a `--dir` that does not exist — exit with 1, and that `tcp-ipv6`
on an IPv4-only JVM exits with the skip code 3. `restart` must
refuse a leftover state file (exit 1), `non-java-peer` must skip
(3) from a jar with no `echopeer` beside it and fail (1) on a
bad `--peer`, and `echopeer` itself exits 2 on bad arguments
(`magefile_restartpeer.go`). The process harness lives in
`magefile_workload.go`.

**Done when**: PLAN.md's M0 acceptance holds — the M1 scenarios
run standalone and `strace -f -e trace=openat` shows the two
marker opens around each scenario.

## M1 — walking skeleton

**Goal**: every layer, once: probes → correlator → NDJSON →
normalizer → golden check → integration harness, on `file-basic`
and `tcp-loopback`. PLAN.md: "M1 — Walking skeleton".

**Step 1 — lift the build and generator from ior** (done, from
ior `b21afc50328c`; every copied file names that commit and its
changes in a provenance header):

- Mage targets `Build`, `BpfBuild`, `Generate*`, `Test`, `Vet`,
  `Lint`, `Fmt`, `FmtCheck`, `Clean` — split over
  `magefile_bpf.go`, `magefile_generate.go` and `magefile_fmt.go`
  beside M0's `Magefile.go`. `Build` compiles every package
  (`go build ./...`) until `cmd/iormesh` exists (step 10). The
  force switch is `IORMESH_FORCE_GENERATE`.
- `internal/generate/`, `internal/tracepoints/`,
  `internal/types/`, `internal/csvlist/` (a tracepoints
  dependency), whole, with their tests; only the tests that check
  ior's own docs were dropped.
- `internal/c/`: every hand-written file but ior's raw-tracepoint
  prototype `rawsyscall.c` — not just
  `maps.h`/`types.h`/`filter.c`/`fdname.c`/`fileident.c`, because
  the generated handlers call helpers from all of them (restart,
  exec, recv, poll, handle, iouring). `ior.bpf.c` became
  `iormesh.bpf.c`. The C macros keep ior's `IOR_` prefix so ior
  fixes port without renaming.
- `internal/bpfembed.go` (override `IORMESH_BPF_OBJECT`,
  object `iormesh.bpf.o`); `internal/gatecmd/`, a trimmed
  `internal/buildgate/` (argv pins, lint-config review, the
  planted-defect lint run, no nested modules, the `//nolint`
  ban) and `.golangci.yml`.
- `vmlinux.h` is dumped from the build host's BTF on first
  `mage bpfBuild` and gitignored, as in ior.
- Not lifted yet: `scripts/build-with-docker.sh` (the container
  build needs the tracer binary of step 10; deployed binaries
  need it by M4).

Regenerating on the generation host reproduced ior's committed
artifacts byte for byte (since step 4 they differ only by the
socket hooks and `tcp_state_event`), so `mage generate` passes
its diff gate with ior's full syscall set.

**Step 2 — narrow the attach set, not the generator** (done).
The generated handlers stay for all syscalls; only attachment
narrows. `tracepoints.DefaultSyscalls()` is M1's set —
`open`/`openat`/`openat2`, `read`, `write`, `close`, `connect`,
`accept`/`accept4` — and replaces ior's FS-family fallback in
`ParseSelectorWithDimensions`, the selector ior already uses for
`-trace-syscalls`: with no `-trace-*` selector and no `-tps`
regex (which, as in ior, alone decides then), exactly these
attach; an explicit selector replaces the set, a `-no-trace-*`
one narrows it. Nothing attaches yet — the loader is step 5.

**Step 3 — the target filter becomes a map** (done). ior
filters on one TGID baked into rodata (`PID_FILTER`). iormesh
needs several targets now and a process tree in M2, so the gate
is a hash map userspace fills before it attaches:

```c
// maps.h — tgids being traced (a set; the value is unused).
struct {
    __uint(type, BPF_MAP_TYPE_HASH);
    __uint(max_entries, 4096);
    __type(key, __u32);   // tgid
    __type(value, __u8);
} target_map SEC(".maps");

// filter.c
static __always_inline int iormesh_pid_targeted(__u32 tgid) {
    return bpf_map_lookup_elem(&target_map, &tgid) != 0;
}
```

`filter()` checks ior's own-PID exclusion (`IOR_PID_FILTER`,
so the tracer never traces itself) first, then
`iormesh_pid_targeted`, then ior's optional `TID_FILTER`. The
`PID_FILTER` rodata is gone, and with it ior's "-1 traces all":
an empty map traces nothing, which is also what a pattern
session needs until its first match. `exec.c`'s two direct
`PID_FILTER` tests (exec record scope, `task_newtask` child
scope) call `iormesh_pid_targeted` too.

**Step 4 — TCP 4-tuples from the socket** (done). New file
`internal/c/sock.c`, included ahead of the generated handlers:

- `sock_owner_map` (LRU hash, 65536): `struct sock *` →
  `{pid, fd}`. An entry must not outlive its socket: slab reuse
  hands a freed sock's address to the next socket, possibly a
  non-target's, which would then be attributed to the old
  owner. LRU only bounds what is left (sockets open when the
  trace ends, a lost tracepoint run).
- **connect (active side)**: the generated `sys_enter_connect`
  handler calls `iormesh_sock_on_connect(pid, fd)`, which walks
  fd → `struct file` → `struct socket` → `struct sock`, keeps
  only IPv4/IPv6 TCP sockets and registers the owner. Then the
  in-syscall `CLOSE → SYN_SENT` (local port still 0) and the
  softirq `SYN_SENT → ESTABLISHED` transition (full 4-tuple —
  the local port the syscall never sees) are both reported.
  The `sys_exit_connect` handler calls
  `iormesh_sock_on_connect_exit(tid)` with the sock parked by
  tid at enter (`connect_sock_map`): a connect failing before
  `SYN_SENT` (`ENETUNREACH`, `EINVAL`, a cgroup `EACCES`, ...)
  leaves the sock in `TCP_CLOSE` with no transition, and its
  `close()` is silent too, so a sock still `TCP_CLOSE` at exit
  is unregistered there. Chosen over a kprobe/fentry on
  `__sk_destruct`: it stays on stable tracepoints and costs only
  target connects instead of every socket free on the host.
- **accept (passive side)**: the child socket reaches
  `ESTABLISHED` in softirq *before* `accept()` returns, when no
  owner is registered, so that transition is lost. The generated
  `sys_exit_accept`/`sys_exit_accept4` handlers call
  `iormesh_sock_on_accept(pid, ret)`: register the returned fd's
  sock and emit a snapshot read straight from `sk->__sk_common`
  (`oldstate = TCP_STATE_SNAPSHOT` (0), `newstate` the socket's
  current state). A child the peer reset in the accept queue is
  already `TCP_CLOSE`: it still gets its snapshot but is not
  registered, since it never transitions again.
- **`sock:inet_sock_set_state`** (`handle_inet_sock_set_state`):
  TCP only, and only sockets found in `sock_owner_map`. It never
  filters or attributes by the current task — in softirq that is
  whichever task was interrupted — only by the socket. On
  `TCP_CLOSE` the entry is deleted, since the next socket may
  reuse the address.
- The fd → sock walk uses `BPF_CORE_READ` probe reads, not
  `fileident.c`'s typed walk: connect and accept are rare, and
  the probe reads work without the `bpf_rdonly_cast` kfunc
  (kernels before 6.2). `sk_protocol` is read as a probed
  bitfield (it was one before 5.6).
- The hooks are emitted by the generator
  (`internal/generate/sockowner.go`) right behind `filter()` and
  ahead of the per-tid hook, so a sampled-out call still
  registers its socket.

The record is `struct tcp_state_event` in `types.h` (88 bytes,
generated into `types.TcpStateEvent`): `event_type`
(`TCP_STATE_EVENT`, 68), `schema_version`, `time`, `sk` (the
join key — fd numbers are reused, the socket is the
connection), owner `pid`/`fd`, `oldstate`/`newstate`,
`family`, `sport`/`dport` (host order, `sport` local), and
`saddr`/`daddr` (16 bytes, network order, IPv4 in the first 4).
Every scalar is 32/64-bit because the Go type generator maps no
16-bit integers.

Tested by `mage bpfRootTest` (root, via `sudo -n`): the test
binary of `./internal` (`internal/sock_root_test.go`) loads the
whole object, attaches only the connect/accept handlers and the
state tracepoint with libbpfgo directly (the loader is step 5),
and over loopback checks the client's `SYN_SENT → ESTABLISHED`
tuple and owner fd (Go's `net.Dial` connects non-blocking and
gets `EINPROGRESS`, like NIO), the accept snapshot's mirrored
tuple, `TCP_CLOSE` for both sockets with `sock_owner_map`
emptied; that a connect to 255.255.255.255 (`ENETUNREACH`)
and a reset child taken by plain `accept(2)` leave
`sock_owner_map` empty; and that an empty `target_map`, or one
naming another PID, yields nothing (with a positive control). The target only counts tests
reported as passed (`gatecmd.RootBPFTests`, pinned by
`internal/buildgate`). Not covered yet: IPv6, Java processes.

**Step 5 — loader and ring buffer** (`internal/bpf`, cgo; built).
Output of this package is a channel of decoded raw records
(`internal/types`), nothing more:

- `bpf.Open(Config{Pids, Probes, Buffer})` loads the object
  (`internal.LoadBPFModule`, embedded or `IORMESH_BPF_OBJECT`),
  sets `IOR_PID_FILTER` to the tracer's own PID (self-exclusion),
  seeds `target_map`, attaches the probe set one at a time
  (libbpfgo's link list is not thread-safe — ior's
  `libbpfAttachMu`) and starts the ring-buffer poll.
  `AddTarget`/`RemoveTarget` edit `target_map` at run time.
- `DefaultProbes()` is enter+exit of every
  `tracepoints.DefaultSyscalls()` syscall plus
  `sock:inet_sock_set_state`. A syscall's enter and exit attach
  together or not at all; a pair whose tracepoint this kernel
  lacks (`ENOENT`) is skipped and listed by `Skipped()`. A
  program missing from our own object, any other attach error,
  and a set where nothing attached (an unmounted tracefs looks
  like every tracepoint missing) fail `Open`.
- `Records()` yields typed records (`*types.OpenEvent`, …) in
  ring-buffer order; the decode table is ior's
  `rawRuntimeEvents` mapping (`eventloop_kinds.go`) without its
  event-loop handlers, plus `TCP_STATE_EVENT`. A full channel is
  backpressure: the ring buffer fills and the kernel counts drops.
- `Losses()` = kernel drops (`ringbuf_drop_map`, summed per CPU)
  + records that arrived undecodable (unknown type, malformed,
  short) — the correlator turns deltas into `drop` events.
- `Close()` leaves the teardown order to libbpfgo's
  `Module.Close` — ring buffer (stops the poll, closes the raw
  channel and so `Records`), then links, then the object — and
  never destroys a link itself (a failed destroy already freed
  it). Records in flight at Close are dropped and not counted;
  the map methods return an error after Close but must not race
  it.

Root tests (`internal/loader_root_test.go`, run by `mage
bpfRootTest`): a bash target opening `/etc/hostname` with shell
builtins yields an `OpenEvent` with no loss; an untargeted PID
yields nothing until `AddTarget`, nothing after `RemoveTarget`,
and is traced again after a second `AddTarget`; `Close` closes
`Records` with both channels full and nobody reading; `Open`
with only missing tracepoints fails.

**Step 6 — the correlator** (`internal/correlate`, cgo-free;
built). `correlate.New(Config{Host, Sink, ProcInfo, WallOffsetNS,
FuseWindowNS})` returns a `Correlator`; `Handle(rec)` takes each
record from `bpf.Tracer.Records()`, `Close()` flushes and closes
the sink, `Graph()` returns the sorted per-host graph. One
goroutine owns it. `Host{ID, Project}` lives here, not in
`schema` (the guide's earlier `schema.HostInfo`). The rules, each
unit-tested with synthetic record streams:

- **Pairing**: an enter waits per tid for its exit (a thread runs
  one syscall at a time); an exit without the matching enter
  (lost to a drop) emits nothing and is counted (`Unpaired`).
  ior's restart handling is not lifted: M1 attaches no restart
  probes (`SYSCALL_RESTART_EVENT`), so it lands with M2's signal
  work.
- **fd generations**: an open or accept exit with `ret >= 0`
  creates a new instance (`gen = last + 1`); `close` removes it
  (except on `EBADF`). M1 does not trace `socket`, `dup` or
  `pipe`, so the creation rule is completed from the other side:
  an fd never seen predates the trace (`gen 0`), and an fd used
  again after its close was recreated by an untraced syscall
  (`gen = last + 1`). `(pid, pid_incarnation, fd, fd_gen)` thus
  names one instance even when a library's open and a later
  socket land on one number.
- **Sockets**: a socket's first `tcp_state` record binds the
  owner's fd to the socket (`sk`) — `CLOSE → SYN_SENT` inside
  connect, or the accept snapshot, which precedes its accept
  exit and so reports the generation that exit will create.
  Later transitions report the instance bound then, even after
  close(2), and never re-bind a reused number. `connect` and
  `accept` events carry the known 4-tuple in `args`.
- **Markers**: an open of `/iormesh-marker/<s>/<edge>` becomes a
  `marker` event with `args.marker = "<s>/<edge>"`.
- **Loopback join**: two sockets whose `(local, remote)` mirror
  each other, established within 5s, share one `conn_id` — the
  hex SHA-256 (first 16 chars) of the sorted address pair plus
  the earlier establish time — and both processes get a fused
  `connected` edge to `conn:<id>`. A socket still alone when its
  window passes gets its own id and an unresolved edge.
- **Every socket event carries `conn_id`** (connect, accept,
  read, write, close, tcp_state on a socket): the normalizer
  keeps a `connected` edge only for conn ids its bracketed
  events carry (step 8). To give each event its final id, socket
  events wait in a FIFO until their socket is fused or its
  window passes; events behind them wait too, so the sink still
  sees gap-free `seq` order (seq is assigned on completion). The
  cost: an unfused socket (a peer on another host) holds every
  later event in memory for up to the 5s window, measured on the
  record clock — `Close` always flushes.
- **Paths**: an enter whose path read faulted is completed by the
  exit-time re-read (`OPEN_NAME_FIXUP_EVENT`); an open whose path
  stays unknown tracks its fd but emits no event (`NoPath`
  counts it) — `args.path` is required and a `file:<host>:` node
  would be a lie. Dual-stack sockets' `::ffff:` addresses are
  unmapped, so both ends of a JVM loopback pair fuse and `args`
  shows `127.0.0.1`.
- **Graph**: host, process (attr `comm` only — raw pids or ports
  in attrs would survive normalization), `runs_on`; file nodes
  `file:<host_id>:<path>` from the exact event path with an
  `opened` edge per successful open; socket nodes per conn id.
- **Envelope**: `pid_incarnation` and `comm` come from
  `ProcInfo` (`/proc/<pid>/stat` starttime, clock-tick
  resolution); `wall_ns = boot_ns + WallOffsetNS`
  (`BootToWallOffset`, measured once); `err` is the errno name.
- **Host-level events** go through the same queue, so `seq`
  stays gap-free and ordered: `Session(schema.Session)` queues
  the header and must come before the first `Handle`
  (`ErrSessionNotFirst` otherwise); `RecordLosses(total)` takes
  the cumulative `bpf.Losses.Total()` and queues a `drop` with
  `args.lost` = the growth since the last call, nothing when it
  did not grow; `End(reason)` checks the reason
  (`schema.KnownEndReason`) and queues the `end` behind every
  held socket event, after which every call fails (`ErrEnded`).
  They carry PID 0 and are stamped by `Config.Clock`
  (`CLOCK_BOOTTIME` by default). Field reference:
  [schema.md](schema.md), "Host-level events".

`golden_test.go` feeds streams shaped like `file-basic` and
`tcp-loopback` (JVM noise outside the bracket included) through
the correlator, the normalizer and the shipped `expect.json`.

**Step 7 — NDJSON sink and graph export**
(`internal/schema/ndjson.go`; built): `NewNDJSONSink(w)` writes one
validated event per line, keys in struct order (encoding/json),
buffered, with a sticky write error that `Close` reports at the
latest; `ReadNDJSON` reads a stream back strictly; `WriteGraph` /
`WriteGraphFile` write the graph sorted, validated and (for the
file) atomically; the CLI (step 10) picks the file names. The on-disk format is documented in
[schema.md](schema.md). `internal/correlate/export_test.go` runs a
fixed record stream — session header, file work, a drop, a fused
loopback pair, end — through the correlator into both writers
and compares the bytes with committed goldens (`-update`
rewrites them).

**Step 8 — the normalizer** (`internal/normalize`, cgo-free; done):

```go
// Trace is one trace's event stream and graph export. They are
// normalized together: node ids embed the same host ids, PIDs,
// paths and connection ids as the events.
type Trace struct {
    Events []schema.Event
    Graph  schema.Graph
}

// Normalize cuts the trace to opt.Bracket, then rewrites
// run-specific values in order of first appearance. It never
// reorders events and never modifies its input.
func Normalize(in Trace, opt Options) (Trace, error)

type Options struct {
    Bracket   string // scenario name; empty keeps everything
    TmpPrefix string // replaced by "$tmp" in paths and file ids
}
```

The bracket is cut *per process*: each process keeps its events
from its own `<scenario>/begin` marker to its own `/end` marker,
inclusive — tcp-loopback's server and client bracket themselves
separately, and one global window would let the first role's
JVM startup in. Processes without a begin marker are dropped;
host-level events (PID 0: `drop`, `session`, `end`) are kept
inside the union of the brackets, so a ring-buffer gap in the
scenario stays visible. No begin marker at all
(`ErrNoBracket`), a begin without an end
(`ErrUnterminatedBracket`), or repeated or out-of-order markers
(`ErrMalformedBracket`) are errors, never a best-effort cut.

The cut comes *before* renumbering, so ids count from the
bracket and noise outside it never shifts them. The output stays
a `[]schema.Event`, so numeric fields carry ordinals instead of
`$` strings; renumbered values start right after the range kept
verbatim, so the two never collide:

- `host_id`, `conn_id` → `$h1`, `$c1`, …
- `pid`, `tid`, `pid_incarnation` → 1, 2, … The PID is keyed by
  host and PID, so a reused PID keeps its number and the
  incarnation tells the two processes apart.
- `fd` ≥ 3 → 3, 4, … per fd *instance* (pid, fd, `fd_gen`);
  `fd_gen` becomes 1, or stays 0 for an fd predating the trace.
  fds 0–2 stay.
- `ret` of a successful open* or accept* → the normalized fd
  (it *is* the fd).
- ports ≥ 1024 → 1024, 1025, …, keyed by value, so a loopback
  server's local port and its client's remote port stay one
  port. Well-known ports stay.
- `local_addr`, `remote_addr`: IPv4-mapped addresses are
  unmapped (`::ffff:127.0.0.1` → `127.0.0.1`) — the JVM's
  dual-stack sockets report the mapped form unless the host
  lacks IPv6.
- `path`, `new_path`: `TmpPrefix` → `$tmp` (whole leading
  components only).
- `seq`, `boot_ns`, `wall_ns` → the event's rank in the output;
  `latency_ns` → 0 (a duration whose value is noise).
- node ids and edge ends get the same symbols
  (`file:$h1:$tmp/a.dat`, `proc:$h1/1@1`, `conn:$c1`); the graph
  is sorted again after rewriting.
- host-level events (PID 0) keep `pid`, `pid_incarnation` and
  `tid` 0, so they stay recognizable and a run-specific drop
  never shifts a process ordinal.

With a bracket the graph is pruned to what the bracketed events
account for — the graph has no times, so otherwise a JVM
startup file opened before the begin marker would satisfy a
declared edge or trip `must_not_appear`. Kept: processes with
bracketed events and their `runs_on` edges, `opened` edges to a
file such an event of that process opened (`path` or
`new_path`), `connected` edges to a connection whose `conn_id`
such an event carries — so the correlator must set `conn_id` on
a connection's socket events — and the nodes those name.

Not normalized: event names, comm, flags, byte counts, other
return values, paths outside `TmpPrefix`, node and edge attrs
(only copied). v1 has no inode field yet; inode-derived ids join
the table when M2 adds one.

**Step 9 — the golden check** (same package; done):

```go
// Check asserts declared ⊆ collected on a normalized, bracketed
// trace. It returns every violation, not just the first.
func Check(got Trace, want Expectation) []Violation

// LoadExpectation reads expect.json strictly (unknown keys and
// trailing data are errors) and validates it.
func LoadExpectation(path string) (Expectation, error)
```

Symbols bind to values as a bijection per kind: a symbol keeps
its first value, and two symbols of one kind never share one
(`$fd1` and `$fd2` are different instances). `$p` binds to the
process node id, `$t` to a thread of it, `$fd` to the instance
(pid, fd, `fd_gen`), `$port` to the port, `$c` to `conn_id`;
node-id templates (`file:$h1:$tmp/a.dat`, `conn:$c1`) bind the
symbols inside them. A trail of bindings lets a failed attempt
undo without copying.

Algorithm: declared events are grouped by thread; each declared
thread is assigned a collected thread, backtracking over
candidate threads only. Within a thread the match is greedy:
each declared event takes the first collected event after the
previous match that fits. Unset declared fields are not
compared. Open flags compare by access mode (`O_ACCMODE`) plus
every declared bit, tolerating extra bits — the JDK, libc or
architecture may add `O_CLOEXEC` or `O_LARGEFILE`, while 577's
`O_CREAT|O_TRUNC` must be there; declared 0 matches any
read-only open. If no assignment exists, one event violation
names the deepest declared thread that failed, its first
missing event and the best candidate's progress. Edges are then
matched greedily in declared order (each takes the first fitting
graph edge, without backtracking) (an empty declared `state` matches
any) with the bindings the events made — or the best partial
ones — and finally every string reachable from every event and
the graph (walked by reflection: JSON escaping would hide a
planted secret from a substring search) is scanned for
`must_not_appear` entries, one violation per entry with its
count and first location.

Tests run synthetic traces shaped like real runs — noise
threads, fd reuse, startup I/O outside the bracket, extra open
flags, mapped addresses — against the shipped `expect.json`
files, plus one negative case per rule.

**Step 10 — CLI** (`cmd/iormesh`, logic in `internal/cli`; built,
`mage build` links `./iormesh`). `main.go` only dispatches.

- `iormesh run -pid PID[,PID…] -project NAME -out DIR
  [-duration 4h] [-ready FILE]` — the foreground tracer:
  `bpf.Open` → `correlate` → `schema.NDJSONSink` into
  `DIR/events.ndjson`, then `DIR/graph.json` at exit. It stops at
  `-duration` (default 4h, ior-style bound), on SIGINT/SIGTERM, or
  when every target has exited (draining until the ring buffer is
  quiet for 300ms), and prints a one-line summary with the loss
  counters to stderr. The stream opens with a `session` header
  (boot id, FQDN, kernel release, arch — gathered before the
  trace directory is touched), gets a `drop` event whenever the
  loss counters grew (polled on the 200ms tick, during the final
  drain too, and once after the loop, before the tracer closes)
  and closes with an `end` event: `duration`, `stop` (signal —
  also one that cuts the drain short), `targets_exited`, or
  `error` (write error, ready file, record stream closed,
  unreadable loss counters — the last also stops the run, since
  drops must never be silent). Loading the object takes seconds, so
  `-ready FILE` is created once every probe is attached — a
  harness waits for it before releasing the target's startup
  gate. Flags are validated (pids positive, distinct, running;
  project `[A-Za-z0-9._-]`; duration positive); errors exit 2.
- `iormesh normalize [-bracket S] [-tmp PREFIX] [-check
  expect.json] [-out DIR] TRACEDIR` — reads a trace directory
  strictly, normalizes it, then either golden-checks it (each
  violation printed, exit 3) or writes the normalized trace to
  `-out` (or its events to stdout).

Verified by hand on this host: the real `file-basic` and the
two-process `tcp-loopback` scenarios, traced with `-ready` gating,
pass `normalize -check` against their goldens — one fused
connection for the JVM pair.

**Step 11 — integration harness** (done; `mage integrationTest`):
`integrationtests/` lifts ior's `harness.go`, `parse.go`,
`kernelloss.go`, `unexercised.go`, `kernel_support_test.go` and
the judged-run helpers, and `internal/gatecmd/integration.go`
plus `magefile_integration.go` lift the runner and its gate, with
`ioworkload` swapped for `java -jar workload.jar` and ior's
`.ior.zst` for the trace directory `iormesh run` writes. The
details and the test table are in docs/testing.md. The test as
built:

```go
func TestWalkingSkeleton(t *testing.T) {
    enableParallelIfRequested(t)
    h := newHarness(t)
    // Starts server and client on a free port behind their gates,
    // traces both PIDs, releases the server, then the client once
    // the server printed "listening"; re-runs on kernel loss.
    run := traceJudged(t, h, startTCPPair(t))
    assertOnlyTracedPIDs(t, run.Trace.Events, run.pids)
    got := normalized(t, h, run, "tcp-loopback")
    want := expectation(t, "tcp-loopback")
    assertGolden(t, got, want) // normalize.Check, each violation
    // one socket node; two connected edges, fused, one per PID
    assertOneFusedConnection(t, run.Trace.Graph, run.pids)
    // plus: mutated goldens must fail the check
}
```

Beside it: `TestFileBasic`, `TestUntracedProcessStaysOut` (an
untraced twin leaves nothing) and
`TestSecondRunNormalizesIdentically` (two runs per scenario,
normalized inside the bracket, equal per process — see
docs/testing.md for why per process). That last check made the
tcp-loopback server wait for the client's EOF before closing:
the racing closes traced different TCP teardowns run to run.

**Done when**: PLAN.md's M1 acceptance holds;
`mage integrationTest` passes ten times in a row; a second
normalized run diffs empty against the first inside the bracket.

## S3 — overhead and volume budget

**Goal**: replace decision #25's placeholders with measurements.
Runs on the M1 build. Result: `docs/spikes/s3-overhead.md`.

1. Lift `scripts/perf-baseline.sh` and the `perf/` layout from
   ior. *Landed*: `scripts/perf-baseline.sh record [label]`
   builds the jar, runs `syscall-burst` untraced `PERF_COUNT`
   times (default 6, benchstat's minimum for a confidence
   interval) at each of `PERF_THREADS` (default
   `1 4 16`) and writes `perf/burst-<label>.txt` in benchstat
   format (`BenchmarkSyscallBurst/mode=untraced/threads=T`, with
   ns/op and ns/syscall); `compare OLD NEW` runs benchstat. ior's
   label rules, dirty-tree fingerprint and no-overwrite refusal
   are kept; its `go test -bench` set and static BPF metrics are
   not (iormesh has no hot-path benchmarks yet). Step 3 adds its
   traced and bystander runs as further `mode=` values of the
   same file. Usage: docs/testing.md "Performance baselines".
   The committed `perf/burst-d34e2e2.txt` was recorded under
   load (loadavg 7.6, see its header) as proof the tooling works
   — it is not the S3 reference; step 3 re-records on an idle
   host.
2. Add a load scenario to the workload: `syscall-burst --ops N
   --threads T` (tight 1-byte read/write loop on a temp file —
   the worst case per syscall). *Landed*
   (`workload/src/.../scenarios/SyscallBurst.java`): N ops
   (default 1,000,000) split evenly over T platform threads
   (default 1, at most 1024, N ≥ T); each thread holds a write
   and a read fd on its own `syscall-burst-<i>.dat`, so one op
   is exactly `write(fdW, 1)` + `read(fdR, 1)` — no lseek — and
   every byte read back is checked. It prints
   `syscall-burst ops=N threads=T syscalls=2N elapsed_ns=E` on
   stdout, E timing the loops alone (opens before a start latch,
   JVM startup outside). Its expect.json declares only the
   marker bracket (PLAN.md, "Load scenarios"). `mage
   workloadTest` checks the result line and that bad `--ops` /
   `--threads` exit 2.
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
   `attach_gap`, never silently. Scenario: `restart` (landed
   ahead, task oa4): a launcher runs `RestartApp` as a child JVM,
   reaps it and runs it again with the identical argv
   (`java <flags> -cp <cp> iormesh.workload.scenarios.RestartApp
   <dir>`); the first incarnation finds no state file (a failed
   `openat`) and writes one, the second reads it and rewrites
   it. The harness binds the session to the glob
   `*iormesh.workload.scenarios.RestartApp*`, which matches both
   incarnations and never the launcher (`-jar workload.jar
   restart`), so each incarnation emits its own `restart`
   markers and the per-process cut keeps it without
   fork-following. The JVM's own startup runs between exec and
   the begin marker, so an `attach_gap` drop lands before the
   bracket; one inside it means the insert came too late and
   the run must fail.

3. **App identity**: all incarnations matched by one pattern share
   an `app:<project>/<pattern>` node with `incarnation` edges
   (app → process). `restart`'s golden declares them from one
   `$a1`; the graph must accept the `app` node type and the
   `incarnation` edge, and the normalizer passes `app:` ids
   through unchanged (they carry nothing run-specific). Its
   bracket prune keeps an `incarnation` edge, and so its app
   node, when the edge's process has events in the bracket —
   the app node never has events of its own
   (`TestRestartGoldenEndToEnd` runs the golden through
   `Normalize`). The edge type is a normalize-local constant
   until the graph schema defines it.

4. **File surface**: attach the remaining file families (ior's
   family table, `internal/tracepoints/family.go`). For each
   family, port ior's test from `integrationtests/<family>_test.go`
   with the Java scenario swapped in.
   Scenarios (landed ahead, task ma4): `file-lifecycle` (mkdir,
   O_EXCL create, rename, ftruncate, utimensat, unlink, rmdir),
   `file-links` (symlink, readlink, statx no-follow, link) and
   `file-attrs` (chmod, chown, fchmodat, fchown, f*xattr). Their
   goldens name the syscalls strace shows on JDK 25 and 27
   (`rename`, not `renameat2`; `statx`, which JDKs before 22
   issue as `newfstatat`) and use only the existing `path`,
   `new_path` and `flags` args. They do not load until the new kinds are
   appended to `schema.KnownKind`, which `ParseExpectation`
   checks — that is this step's first change.

5. **Rename-aware paths**: on `rename*` exit, rewrite `path` of
   every open `fdState` under the old name; unit-test directory
   renames and cross-directory moves.
   `file-lifecycle` writes through an fd after renaming its
   file, so its golden proves this end to end.

6. **fd lifecycle**: `dup*`, `fcntl(F_DUPFD*)` copy `fdState`
   (new `gen`); fork copies the parent's table; exec drops
   `O_CLOEXEC` fds.
   Scenario: `fd-lifecycle` — fd reuse, a `dup` (the JDK's only
   in-process one: `Files.newDirectoryStream`) and stdin/stdout
   inherited by a child JVM (`ProcessBuilder` redirects, `dup2`
   onto 0 and 1 in the child). The child emits no markers, so
   `normalize`'s per-process cut must also keep processes forked
   inside a bracket; today it drops them.

7. **IPC, signals, UDP, epoll**: as families, default-off per S3's
   recommendation.
   Scenarios (landed ahead, task na4; strace-verified on JDK 25,
   26 and 27-ea): `pipe-ipc` (a `java.nio.channels.Pipe`, then a
   child JVM's stdin/stdout pipes), `unix-socket` (server and
   client roles on `$tmp/unix-socket.sock`), `signals` (SIGTERM
   via `Process.destroy`, SIGKILL via `destroyForcibly`, to two
   child JVMs), `udp-loopback` (server and client roles, one
   datagram each way) and `tcp-ipv4`/`tcp-ipv6` (one AF_INET or
   AF_INET6 connection in one process, full 4-tuples declared).
   Their goldens use kinds `schema.KnownKind` lacks — `bind`,
   `unlink`, `sendto`, `recvfrom`, `pipe2`, `dup2`, `kill` — and
   only existing fields, which leaves gaps this step must close
   and then tighten the goldens with: `pipe2` returns two fds
   (declared without `fd`; the ends bind through their reads and
   writes), `kill` has no target-PID or signal argument (declared
   bare; delivery with the sender's PID — `signal_generate` — is
   not declared at all), a unix socket's path is `args.path` on
   `bind` and `connect`, and UDP and pipes declare no edges
   because the graph has no edge type for them yet. The children
   of `pipe-ipc` and `signals` emit no markers, like
   `fd-lifecycle`'s (step 6). The first NIO use inside a bracket
   also makes a `socketpair` and throwaway AF_INET6 probe
   sockets; nothing declares them.
   `tcp-ipv6` (and `non-java-peer`) exit with the skip code 3
   when the host cannot run them, but `integrationtests/process.go`
   still fails on any non-zero exit: the harness that runs these
   scenarios must map exit 3 to `t.Skip`, or a skip reads as a
   failure.

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

10. **Non-Java peer**: scenario `non-java-peer` (landed ahead,
    task oa4). Its peer is `workload/echopeer`, a cgo-free Go
    echo server that `mage workloadBuild` builds next to the
    jar — ior's `ioworkload` has no scenario that serves a TCP
    client. It uses raw `syscall` calls on a locked main thread
    (socket, bind, listen, `accept4`, read, write, close, then
    an `openat`/write/close of `$tmp/non-java-peer.log`), so its
    declared syscalls are exact and carry TID == PID; it
    brackets itself with the scenario's markers. The JVM spawns
    it, connects over AF_INET, then reads the log back. The
    golden declares `attribution: none` on every peer event;
    the tracer must never attribute a non-JVM process (no
    agent, no Java fields).

**Done when**: PLAN.md's M2 acceptance holds, including the S3
budget as a `mage perfGate` target.

## S1 — Java↔syscall join

**Goal**: prove that a Java stack can be joined to the syscalls it
caused, and what that costs. Throwaway code under `spikes/s1/`;
result in [`docs/spikes/s1-java-join.md`](spikes/s1-java-join.md).
The experiments below were the plan; each ends with what S1
found.

**Constraint that shapes the design**: the JDK's I/O classes are
loaded before any agent runs, and retransformation cannot add,
remove or rename methods. So hooks are pure *body rewrites* of
existing methods — the native-method-prefix trick is unavailable.

**Experiment 1 — the hook set.** Instrument the thin Java methods
that directly call the native I/O functions. Verified by S1 against
the JDK 21 sources and the JDK 25 bytecode (21 and 25 agree for
every entry; 17 differs — decision #29;
[result](spikes/s1-java-join.md)):

| Class (body rewritten) | Java methods | Native (uprobe) | Syscalls allowed under it |
|-------|---------|--------|--------|
| `java.io.FileInputStream` | `open`, `read` (all overloads) | `open0`, `read0`, `readBytes` | openat, fstat / read |
| `java.io.FileOutputStream` | `open`, `write` (all overloads) | `open0`, `write`, `writeBytes` | openat, fstat / write |
| `java.io.RandomAccessFile` | `open`, `readBytes`, `writeBytes`, `seek` | `open0`, `readBytes0`, `writeBytes0`, `seek0` | openat, fstat / read / write / lseek |
| `java.io.FileDescriptor` | `close` | `close0` | close |
| `sun.nio.ch.UnixFileDispatcherImpl` | `read`, `pread`, `readv`, `write`, `pwrite`, `writev`, `seek` | `read0`, `pread0`, … | read, pread64, … |
| `sun.nio.ch.SocketDispatcher` | `read`, `readv`, `write`, `writev` | `read0`, `readv0`, `write0`, `writev0` | read, readv, write, writev |
| `sun.nio.ch.UnixDispatcher` | `close` | `close0` | close |
| `sun.nio.ch.Net` | `socket`, `serverSocket`, `bind`, `connect` | `socket0`, `bind0`, `connect0` | socket, setsockopt / bind / connect |
| `sun.nio.ch.NioSocketImpl` | `accept`, `listen` (callers) | `Net.accept`, `Net.listen` | accept, accept4 / listen |
| `sun.nio.ch.NioSocketImpl` | `shutdownInput`, `shutdownOutput` (callers) | `Net.shutdown` | shutdown |
| `sun.nio.ch.ServerSocketChannelImpl` | `implAccept` (caller) | `Net.accept` | accept, accept4 |
| `sun.nio.fs.UnixNativeDispatcher` | `open`, `unlink`, `rename`, `stat`, … | `open0`, `unlink0`, … | openat / unlink(at) / rename(at) / statx |

What S1 corrected in the first draft of this table:

- `FileDispatcherImpl`'s `read/write/pread/pwrite` live in
  `sun.nio.ch.UnixFileDispatcherImpl` since JDK 21; the Linux
  `FileDispatcherImpl` keeps only `transferTo0/From0`.
- `Net.accept`, `Net.listen`, `Net.shutdown` and `Net.poll` are
  themselves native — nothing to rewrite. Their *callers* are
  hooked: `NioSocketImpl.accept/listen` (and
  `shutdownInput/Output`), `ServerSocketChannelImpl.implAccept`
  for NIO channels.
- `FileInputStream.readBytes`, `FileOutputStream.writeBytes` and
  `RandomAccessFile.readBytes0/writeBytes0` are natives; the Java
  callers (`read`, `write`, RAF's private `readBytes`/`writeBytes`)
  are the hooks.
- Socket close is `UnixDispatcher.close0` (JDK 21+), not a
  `SocketDispatcher` method.

The uprobe side needs four rules the first experiment did not
show: (1) a **curated leaf set** of natives — hooking every
`Java_*` makes `Class.forName0` and friends outer brackets whose
mark leaks onto unrelated I/O below them; (2) a **per-native
allowlist of syscall kinds** (last column) — the JVM's own work
inside a bracket (jimage `pread` while loading an exception class
inside `open0`, `mprotect` inside `Inet6Address.init`) is "JVM
work", not the native's I/O; (3) **drop syscall 335** on x86_64,
`uretprobe(2)` (kernel ≥ 6.11): every uretprobe return enters it
on the target thread; (4) **per-probe ids** (BPF cookies) as the
mark, never runtime symbolization — `libextnet.so` is mapped after
attach and its natives showed up as raw addresses.

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
        // S1: java.base must also *read* the bridge's (unnamed)
        // module, or the rewritten JDK methods fail to link Hooks.
        inst.redefineModule(Object.class.getModule(),
            Set.of(bridgeModule()), Map.of(), Map.of(), Set.of(), Map.of());
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

S1 result: the prototype (`spikes/s1/agent`, `java.lang.classfile`,
JDK 25 only) did exactly this, with `enter(id)`/`exit(id)` and a
per-thread bracket stack instead of a token. The stack walk turned
out to be the agent's dominant cost (Experiment 6).

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

S1 result: (u) plus (b). The uprobe mark names the native call in
the kernel; the agent bracket on the same kernel TID (from one
`readlink /proc/thread-self` per thread) that contains the marked
syscall names the Java method and stack. Every declared op of
`file-basic` and `tcp-loopback` joined, 0 hook/mark mismatches.
The production rules — curated leaf natives, per-native syscall
allowlist, nr 335 dropped, probe ids, mark reset — are listed
under Experiment 1. (a) and (c) were not needed.

**Experiment 4 — clocks.** ior stamps BPF records with
`bpf_ktime_get_boot_ns()` (CLOCK_BOOTTIME); `System.nanoTime()` is
CLOCK_MONOTONIC on Linux. They differ by time spent suspended.
The tracer samples `boottime − monotonic` at start and on every
agent watermark and converts brackets to the boot clock (ior's
`internal/bootclock.go` already handles boot-clock domains).
Verify the residual error is far below a syscall's duration.

S1 result: don't convert for the join. A (boot, mono) pair read
in BPF is off by roughly 70–230 ns, run-dependent (two helper
calls), and pushed 14 of 20 000 kernel stamps out of tight
`nanoTime()` brackets in one run; stamping with `bpf_ktime_get_ns`
(MONOTONIC, `nanoTime`'s clock) put 0 of 160 000 outside. So BPF
records the native's entry in MONOTONIC as the join field (the
prototype used the `sys_enter` stamp; the native-entry variant is
untested); event timestamps stay on the boot clock. Agent-only
records are placed with a user-space midpoint offset
(mono–boot–mono: 2–11 ns spread, load-dependent), re-sampled per
segment.

**Experiment 5 — transport** (decision #27). Compare:

- an append-only spool file — simple, but every flush is a
  `write` the tracer then sees as the target's I/O and must
  filter by path;
- a memory-mapped ring (`FileChannel.map`, single writer) — no
  syscalls on the hot path, so no self-noise.

S1 result: the ring (decision #27) — 0 syscalls per record,
drop-and-count when the reader is a ring behind, every record
delivered or counted in the runs; layout in M3 step 4. Record
types either way:

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

S1 result (loaded host, CPU time, indicative): per hooked I/O
call the uprobe pair costs ≈3.5 µs, an agent bracket without stack
≈0.3 µs, the 10-frame stack walk ≈8 µs (~95 % of the agent). As
bpftrace attached them (by path, no PID filter) the uprobes trap
in every process mapping the JDK library; per-PID attachment is
for M3/#25 to confirm.

**Experiment 7 — attach.** Confirm on JDK 21: `-javaagent` works
silently; live attach prints the JEP 451 warning; what
`-XX:-EnableDynamicAgentLoading` does; what a root-run tracer must
do to attach to a JVM owned by another uid.

S1 result (JDK 25 and 27-ea; no JDK 21 runtime): `-javaagent`
silent; live attach works and prints the JEP 451 warning on both;
`+EnableDynamicAgentLoading` silences it;
`-EnableDynamicAgentLoading` fails the load with
`AgentLoadException`; a root attacher reaches a JVM of another uid
as is.

**Exit**: PLAN.md's S1 exit criterion, plus a recommendation for
each of decisions #2, #6, #27 and #29. Met — see
[the S1 result](spikes/s1-java-join.md); #2, #6 and #27 are
decided in PLAN.md, #29 keeps its default with the 17 → 21 table
differences recorded.

## M3 — Java agent

**Goal**: S1's prototype, productionized. PLAN.md: "M3 — Java
agent". S1 confirmed the design with the corrections below
([result](spikes/s1-java-join.md)): the join is the uprobe TID
mark plus the agent bracket on the same TID, compared in
CLOCK_MONOTONIC; the transport is an mmap ring.

**Files**

```
agent/
  bridge/src/iormesh/bridge/Hooks.java       # bootstrap-visible
  bridge/src/iormesh/bridge/Ring.java        # mmap spool writer
  core/src/iormesh/agent/Agent.java          # premain/agentmain
  core/src/iormesh/agent/HookTable.java      # class → methods → id
  core/src/iormesh/agent/BoundaryTransformer.java
  core/src/iormesh/agent/StackIds.java
  core/src/iormesh/agent/http/…              # one file per library
  core/src/iormesh/agent/otel/SpanContext.java
internal/agentspool/    # Go reader for the ring (cgo-free)
internal/correlate/javajoin.go
```

**Steps**

1. Build: `mage agentBuild` → `iormesh-agent.jar` with the bridge
   jar nested as a resource; ASM shaded under
   `iormesh.agent.shaded` (the S1 prototype used
   `java.lang.classfile`, final only in JDK 24 — M3 must run on
   21 and, per #29, 17). Manifest: `Premain-Class`,
   `Agent-Class`, `Can-Retransform-Classes`.
2. Install, from `premain` and `agentmain` alike: append the
   bridge jar to the bootstrap search, then
   `Instrumentation.redefineModule(java.base, extraReads =
   {bridge's unnamed module})` — without it the rewritten JDK
   methods fail to link `Hooks`. Map the ring, warm up the hot
   path (thread state, `StackWalker`), then retransform. Expect
   the CDS warning on stderr ("Sharing is only supported for boot
   loader classes…") — the workload's stdout contract is intact.
3. `HookTable` is data, not code: a table of
   `(class, method, descriptor, hookId, native, allowed syscalls)`
   so decision #29's per-JDK differences become table variants and
   the tracer gets the same table for its uprobe set (S1's hook
   table above).
4. The ring (decision #27): one file per JVM,
   `<spool dir>/<pid>.ring`, in the tracer-owned directory, mapped
   `MAP_SHARED` by agent and tracer. Header (magic, capacity, pid,
   `head` and `tail` on their own cache lines, drop counters), then
   records framed by one 8-byte word `len | type << 32` published
   with a release store. Writers reserve with a CAS on `head`;
   when the reader is a full ring behind they drop and count —
   never block the target. Record types:

   ```
   BRACKET   hook_id, ktid, enter_mono, exit_mono, stack_id, thread_id
   STACK     stack_id, n, frames[n]    // first time an id is seen
   THREAD    thread_id, ktid, name     // first bracket of a thread
   METHOD    method_id, class, method, descriptor, file, line, jar
   WATERMARK mono_ns                   // every bracket that exited
                                       // before this is in the ring
   ```

   Brackets are written at exit, so a syscall inside a still-open
   bracket has no record yet; an agent timer thread writes
   `WATERMARK` (a memory store, no syscall) so the joiner knows
   when to stop waiting. The S1 prototype had no watermark — it
   drained after exit.

   The agent's only syscalls are the ring's setup in `install`
   and one `readlink("/proc/thread-self")` per thread; the tracer
   tags both as the agent's (spool path; first such readlink per
   TID before that TID's THREAD record) — never the target's.
5. The Go side joins the agent bracket to the uprobe-marked
   syscall:

   ```go
   // joiner pairs a uprobe-marked syscall with the agent bracket
   // that contains it. The BPF side stamps the native's entry
   // with bpf_ktime_get_ns (CLOCK_MONOTONIC, the clock of
   // System.nanoTime), so containment needs no conversion; the
   // event's own timestamp stays on the boot clock. A marked
   // syscall with no bracket stays attribution "none" (no Java
   // stack); nothing is attributed by guessing. S1 paired on the
   // sys_enter stamp; the native-entry stamp is untested.
   type joiner struct {
       open   map[uint32][]agentspool.Bracket // per ktid, by enter
       stacks map[uint64][]uint32             // stack id → methods
   }

   func (j *joiner) attribute(ev *schema.Event) (ready bool)
   ```

   Events are released once an agent `WATERMARK` has passed the
   syscall's time, or after a 2 s timeout when no agent is
   attached — then `attribution: none`.
6. Method table and thread table are emitted as their own NDJSON
   record kinds and parquet files (`methods`, `threads`), keyed by
   id.
7. HTTP hooks: one transformer per library, each emitting an
   `http` event at request completion with `conn_id` (via the
   socket's fd) and `req_seq` (per-connection counter). Start
   with the JDK's `HttpClient` and built-in `HttpServer` — the
   pair the workload uses — then widen per decision #22.
   Headers pass through `redact.Policy` *in the agent*, before
   the spool.
8. OTEL: if `io.opentelemetry.api.trace.Span` is loadable in the
   app's class loader, read `Span.current().getSpanContext()`
   reflectively at `enter` and add ids to the bracket. No OTEL
   dependency in the agent.
9. Scenarios first, as always: `method-flow`,
   `http-plain`, `http-tls`, `http-redaction`, `otel`.

**Done when**: PLAN.md's M3 acceptance holds.

## S2 — cross-host join

**Goal**: a join key that survives NAT, and clock bounds from the
traffic. Throwaway code under `spikes/s2/`; result in
[docs/spikes/s2-cross-host.md](spikes/s2-cross-host.md).
**Done** (2026-11-18): the ISN pair is the key (decision #26)
and the clock bounds hold. The experiments below keep their
original questions, each followed by what the spike found.

**Test rig** — two "hosts" on one machine
(`spikes/s2/rig.sh`, as built):

```sh
ip netns add s2hostA; ip netns add s2nat; ip netns add s2hostB
# veths created inside the namespaces, then in s2nat (nft):
#   prerouting:  ip daddr 10.0.1.254 tcp dport 8080 dnat to 10.0.2.2:8080
#   postrouting: snat to 10.0.2.254:40000-40999 random  (second pass)
```

The spike used bpftrace instead of iormesh tracers: one
`clock.bt` per host namespace, each adding its own test-only
offset to every timestamp. M4's integration test does the same
with real tracers, each with its own `-host-id` override and
`-test-clock-offset` (test-only).

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
Open questions the spike answered:

- the passive side reaches `ESTABLISHED` before `accept()`, when
  no owner is registered (see M1 step 4). Can the pair be
  reconstructed later from `snd_una − bytes_acked` and
  `rcv_nxt − bytes_received` (mod 2³²)? Verify the off-by-one for
  the SYN on both sides.
- TCP Fast Open carries data in the SYN — does the arithmetic
  hold?
- what breaks it: SYN proxies, sequence-randomizing firewalls,
  any L4 proxy that terminates the connection.

*Found*: the pair matched 34/34 connections, all unique, under
DNAT and DNAT plus random-port SNAT, with reused 4-tuples and
TFO. The direct form fails once data (TFO) or a FIN has moved
the counters, and nothing is readable at the opening transition
of either side. The rebuild holds at `ESTABLISHED` and every
later record up to `CLOSE` — but the off-by-one for the SYN is
*not* the same on both sides:

```c
// role-aware rebuild, all mod 2^32; read at ESTABLISHED or later
//   connect side: bytes_acked already counts our own SYN (the
//   SYN-ACK runs tcp_snd_una_update), so no -1;
//   accept side: the child starts with snd_una = iss + 1 and
//   bytes_acked = 0; bytes_received never counts the peer's SYN.
__u32 iss = snd_una - (__u32)bytes_acked - (is_accept ? 1 : 0);
__u32 irs = rcv_nxt - (__u32)bytes_received - 1;
```

The TCP state record must carry `snd_una`, `rcv_nxt`,
`bytes_acked` and `bytes_received` (added in M4; extends M1's
88-byte `tcp_state_event`); the role is derived from the
socket's first record (connect vs accept).
**Experiment 2 — conntrack as the alternative**: read the
original↔reply tuple mapping on the NAT host. Works only where a
tracer or conntrack access exists on that host. *Found*: as an
event stream it joins every connection, but reused tuples need a
time order across hosts; a `/proc/net/nf_conntrack` snapshot
loses all but the last use of a tuple and misattributed 10/34
under SNAT. Optional only.

**Experiment 3 — clock bounds.** For hosts A and B with offset
θ = clock_B − clock_A, every message gives one inequality:

```
A→B message:  θ < recv_B − send_A
B→A message:  θ > send_B − recv_A
```

`send` is the `write` *enter* time, `recv` the `read` *exit* time
of the same stream bytes. Intersect over all messages; report the
interval width on loopback, veth and a real LAN.

*Found* (veth through one forwarding namespace; loopback and a
real LAN untested): the bytes are matched by TCP sequence offset
`seq − (iss + 1)`, identical on both ends, and each receive pairs
with the send that queued its last byte. A receive's offset is
`copied_seq` at `tcp_recvmsg` entry plus the bytes returned —
after the call it is one too far when the FIN was consumed in
the same call. Injected skews of +1.23 s and −0.99 s were
recovered inside ≈10–25 µs (runs 7–9, load-dependent; midpoint
within 1 µs, no violated
inequality); single request/response connections alone gave
82–104 µs, because only a receiver already blocked in `recv`
gives a tight inequality.

**Exit**: PLAN.md's S2 exit criterion, plus a pick for decision
#26. Met: the ISN pair, decided in PLAN.md.

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
// ISNPair is decision #26's key, oriented the same on both hosts:
// the connecting side's ISN first. NAT never rewrites it.
type ISNPair struct{ Client, Server uint32 }

// KeyFromState rebuilds the pair from one TCP state record taken
// at ESTABLISHED or later (S2): the connecting side's bytes_acked
// already counts its own SYN, the accepting side's does not;
// bytes_received never counts the peer's SYN. All mod 2^32.
func KeyFromState(r TCPState) ISNPair {
    own := r.SndUna - uint32(r.BytesAcked)
    if r.Role == "server" {
        own--
    }
    peer := r.RcvNxt - uint32(r.BytesReceived) - 1
    if r.Role == "server" {
        return ISNPair{Client: peer, Server: own}
    }
    return ISNPair{Client: own, Server: peer}
}

// Half is one end of a TCP connection as one host recorded it.
type Half struct {
    Host        string
    Proc        string         // node id
    Role        string         // "client" | "server"
    Local, Peer netip.AddrPort // differ across NAT; display only
    Key         ISNPair        // zero when no record had the fields
    Established uint64         // that host's boot ns
}

// Fuse pairs client and server halves into cross-host edges by
// ISN pair. window is coarse (minutes): it only guards against
// the ISN clock wrapping (~275 s), the pair itself separates
// reused 4-tuples. A half without a partner is returned
// unresolved — it stays in the graph and feeds M6.
func Fuse(halves []Half, window time.Duration) (
    edges []schema.Edge, unresolved []Half)
```

Rules, each a table-driven test:

- a client half fuses with the server half whose `Key` is equal;
  the 4-tuples are not compared (NAT rewrites them);
- two candidates for one half is an error surfaced in the
  summary, never a coin toss; candidates whose raw `Established`
  times differ by more than `window` do not count — the window is
  minutes, far wider than any plausible clock offset, so fusing
  needs no clock bound (the bound is computed afterwards, from
  the fused connections);
- a zero key, or a key nobody else has (sequence-randomizing
  firewall, SYN proxy, untraced far end), leaves the half
  unresolved — no 4-tuple fallback, that would be a guess;
- port reuse: the same 4-tuple six times gives six distinct keys
  and six edges (S2's TIME_WAIT and RST reuse phases);
- fused edges get `State: "fused"` and a `conn_id` shared by both
  hosts' events.

Clock offsets (`internal/federate/clock.go`) implement S2's
inequalities per host pair: every receive pairs with the send
that queued its last byte, matched by sequence offset
`seq − (iss + 1)` (so the data records need their sequence
range: `write_seq` after the send minus the bytes queued — TFO
assigns the ISN inside the call — and `copied_seq` before the
receive). θ = clock_B − clock_A is below
`recv_B − send_A` and above `send_B − recv_A`; the intersection,
computed per time window so drift shows up, is attached to the
merged graph as a `clock_bound` record `{min, max}` with its
width. An empty intersection is reported, not clamped. Cross-host ordering is a
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
