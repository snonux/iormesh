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
  (`/proc/<pid>/stat` starttime, clock-tick resolution) — unique
  per PID reuse more than one clock tick apart. It is read when the session takes the process
  on (see "Envelope" below), not at its first record.
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
  `HTTP` was provisional until M3's acceptance froze it. M1
  assigns `conn_id` locally to fused loopback pairs;
  across hosts the merge assigns it from the ISN-pair join (S2,
  decision #26). The Java fields are nested
  (`java.thread`, `java.stack`) — settled by S1; PLAN.md's flat
  `java_thread`/`java_stack` were a sketch. `HTTP` was
  provisional until M3's acceptance froze it.
- `schema.Sink` (`Event(*Event) error`, `Close() error`) is what
  the correlator writes to; the NDJSON and parquet writers
  implement it. `Event` borrows the event for the call — a sink
  that keeps it copies it (task ud4: the ~400-byte event was
  copied once per pipeline hop by value).

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
joined) or `unresolved` (one end seen), and from M2 `incarnation`
app → process and `forked` process → process; no other edge
carries a state. Slices are sorted (`Graph.Sort`) before every export; Go
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
- A kind's second fd (`pipe2`'s other end, `epoll_ctl`'s
  target) is `args.fd2`, an `$fd` symbol bound to the instance
  like `fd` (M2 step 7); a `dup2`'s returned fd is still declared
  through the events that use it, as `fd-lifecycle` does for its
  `dup`. `args.signal` compares a `kill`'s or `signal`'s number.
- `flags` are compared by access mode plus the declared bits, so
  577 means "write-only, create, truncate" and tolerates an extra
  `O_CLOEXEC`; `0` matches any read-only open (M1 step 9).
- `attribution` (`"java"` or `"none"`), when declared, must equal
  the event's; unset, it is not compared. `non-java-peer`
  declares `none` on every event of its Go peer.
- M3's agent fields: `"attribution": "java"`, `"java":
  {"thread": "<Java thread name>", "method": "<class>#<method>"}`
  (the causing method: the innermost workload frame),
  `{"event": "http", "conn_id": "$c1", "http": {"method",
  "scheme", "host", "path", "status", "req_seq"}}` events, and
  `"otel": {"trace_id", "span_id"}` (with attribution java).
  `otel` is compared when absent too, like `args.count`: an
  event declared without it must be outside any span. The M3
  scenarios first carried these in a second file,
  `expect-pending.json`, until the agent could produce them
  (M3 step 9); task 4b4 promoted the last one, and a test now
  fails on any new pending file.
- `$a1` names an app node — the one node every incarnation of a
  pattern-matched app links to (M2 step 3). Only edges use it;
  it binds the whole node id, so `restart`'s two `incarnation`
  edges from one `$a1` assert one app identity, whatever the
  id's format turns out to be.

## M0 — workload framework

**Goal**: a JDK-only Java 21 app (the core; the OTEL flavor is
its own jar, #5) that runs one named scenario, brackets it with
markers, and ships the scenario's expectations.
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
  otel/                                 # M3's OTEL flavor, own jar
  libs/                                 # M3's third-party HTTP, own jar
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
<path>`, passed to the client as `--path`) the same way, and
M4's `microservices` as four processes, servers released leaves
first. It
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
- `scripts/build-with-docker.sh` was not lifted as a script: the
  container builds (task ab4) are Mage targets over
  `internal/containerbuild`, with ior's Dockerfile adapted as
  `scripts/container/tracer-amd64.Containerfile` (docs/build.md,
  "Container builds").

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

The record is `struct tcp_state_event` in `types.h` (88 bytes in
M1, 112 since ve4, generated into `types.TcpStateEvent`):
`event_type` (`TCP_STATE_EVENT`, 68), `schema_version`, `time`,
`sk` (the join key — fd numbers are reused, the socket is the
connection), owner `pid`/`fd`, `oldstate`/`newstate`, `family`,
`sport`/`dport` (host order, `sport` local), `seq_status` (M1's
reserved word), `saddr`/`daddr` (16 bytes, network order, IPv4
in the first 4), and since ve4 the four `tcp_sock` fields of the
M4 merge (see "The merge"). Every scalar is 32/64-bit because the Go type generator maps no
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
  `libbpfAttachMu`) and starts the ring-buffer reader.
  `AddTarget`/`RemoveTarget` edit `target_map` at run time.
- `DefaultProbes()` is enter+exit of every
  `tracepoints.DefaultSyscalls()` syscall plus
  `sock:inet_sock_set_state`. A syscall's enter and exit attach
  together or not at all; a pair whose tracepoint this kernel
  lacks (`ENOENT`) is skipped and listed by `Skipped()`. Since
  task 8e4 a syscall handler "attaches" by filling its slot in
  `dispatch.c`'s program arrays (indexed by the arch's syscall
  number, `sysnum_<arch>.go`); the two `raw_syscalls`
  dispatchers attach after the slots, exit side first. A
  program missing from our own object, any other attach error,
  and a set where nothing attached (an unmounted tracefs looks
  like every tracepoint missing) fail `Open`.
- `Records()` yields typed records (`*types.OpenEvent`, …) in
  ring-buffer order, in batches of up to 256 (task wc4); the
  decode table is ior's `rawRuntimeEvents` mapping
  (`eventloop_kinds.go`) without its event-loop handlers, plus
  `TCP_STATE_EVENT`. The reader maps `event_map` itself
  (`ringbuf.go`: consumer page, producer page, data area mapped
  twice) and decodes in place — no libbpfgo callback, copy or
  channel hop per record, which cost more than the correlator and
  encoder together (decision #25). It pauses 200 µs after a
  partial batch so a burst arrives in batches, and blocks in
  epoll on an empty ring. A full channel is backpressure: the
  ring buffer fills and the kernel counts drops.
- `Losses()` = kernel drops (`ringbuf_drop_map`, summed per CPU)
  + records that arrived undecodable (unknown type, malformed,
  short) — the correlator turns deltas into `drop` events.
- `Close()` stops the reader (it sees the close within its 50 ms
  epoll timeout, or at once when blocked on a full `Records`,
  which it then closes), unmaps the ring, and leaves the rest to
  libbpfgo's `Module.Close` — links, then the object — never
  destroying a link itself (a failed destroy already freed it).
  Records in flight at Close are dropped and not counted; the
  map methods return an error after Close but must not race it.

Root tests (`internal/loader_root_test.go`, run by `mage
bpfRootTest`): a bash target opening `/etc/hostname` with shell
builtins yields an `OpenEvent` with no loss; an untargeted PID
yields nothing until `AddTarget`, nothing after `RemoveTarget`,
and is traced again after a second `AddTarget`; `Close` closes
`Records` with its channel full and nobody reading; `Open`
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
- **Envelope**: `pid_incarnation` and `comm` come from one
  `/proc/<pid>/stat` read (starttime, clock-tick resolution, and
  the comm field), taken when the session takes the process on:
  at session start for a `-pid` or port target and at adoption
  for a pattern match (`Seed`, `Incarnation`; task jd4). A read at
  the first record instead would, with the correlator lagging past
  the target's exit and a reuse of its PID, give the target's
  events the newcomer's identity. A forked child is read when its
  `task_newtask` record is handled (the record carries no start
  time); a start later than the record's time is a later process
  on the reused PID, so the child keeps `pid_incarnation` 0 and
  the record's inherited comm, as does a child already gone (no
  `/proc` entry). A reuse within the fork's own 10 ms tick rounds
  down to a start at or before the record and is not detected.
  The remaining lazy read is `ProcInfo` for a pid first met in a
  record — an untraced `kill` target, or a child whose
  `task_newtask` record was lost — and it keeps that race. An
  exec record (`sched_process_exec`, successful execs only)
  replaces `comm` with the kernel's post-exec name for later
  events and the process node (task hd4; an empty record
  comm falls back to the exe's basename cut to 15 bytes);
  `wall_ns = boot_ns + WallOffsetNS` (`BootToWallOffset`,
  measured once); `err` is the errno name.
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
JVM startup in. A process without markers of its own that a
bracketed process forked inside its bracket (a `forked` edge,
M2 step 6; its first event lies inside the parent's bracket)
takes the parent's window, transitively for its own children —
fd-lifecycle's, pipe-ipc's and signals' child JVMs emit no
markers. Other processes without a begin marker are dropped;
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
a connection's socket events —, `forked` edges whose two
processes both have bracketed events, and the nodes those name.

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
   not (iormesh has no hot-path benchmarks yet). Usage:
   docs/testing.md "Performance baselines". The committed
   `perf/burst-d34e2e2.txt` was recorded under load (loadavg
   7.6, see its header) as proof the tooling works — it is not
   the S3 reference. Step 3 took its traced and bystander runs
   with `spikes/s3/measure.sh` instead (it needs the tracer as
   root and per-run tracer CPU, which the baseline script does
   not do); the untraced baseline is taken by the same script,
   as interleaved pairs (#25's paired CPU-time method, task
   md4).
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

*Done* (2026-11-18): `spikes/s3/measure.sh OUTDIR` takes the
samples (untraced, traced, bystander per thread count; tracer
CPU via `/usr/bin/time` around the sudo'd tracer, the
idle-target tracer's CPU as the fixed load/attach cost), results
in `spikes/s3/results/`, write-up in
[spikes/s3-overhead.md](spikes/s3-overhead.md). Parquet bytes
could not be measured (no parquet writer before M4); zstd on the
volume run's NDJSON stands in. Every quoted figure is recomputed
from `spikes/s3/results/samples.txt`. Outcome: decision #25 is
Decided with a budget; #16 gets a 64 MB size cap; #7 keeps its
default; hot read/write loops get in-kernel aggregation in M2
(step 11 below). Task md4 (2026-11-19) reworked the script for
#25's paired CPU-time method: taskset pinning (`S3_TRACER_CPU`,
`S3_LOAD_CPUS`), CPU times from `/proc/PID/stat` (the tracer's
from its ready file to exit, the burst's from its startup gate to
its result line), ABAB bystander pairs (`S3_PAIRS`), mode
selection (`S3_MODES`) and medians in `summary.txt`. Task ud4
added the Java join's worst case and an A/B: `S3_AGENT=1` (every
burst under the agent, a fresh `-spool` dir per traced run, the
joined count recorded), `S3_TRACER` (another build) and `S3_PERF=1`
(the tracer's instructions per event, which host load barely moves).

## M2 — full trace surface

**Goal**: everything in PLAN.md's "What we trace" except Java.
Work in this order — each item is one PR-sized unit with its
scenarios landing first (principle #1).

1. **Process tree** (done, task pa4). ior's `internal/c/exec.c`
   handles `task:task_newtask`, `sched_process_exec` and
   `sched_process_exit` — but only to *report* tasks; its filter
   drops a forked child because the child has a new tgid. The
   new-task handler now inserts a traced creator's child into
   `target_map` before the child is first woken, so no record of
   it is lost (`iormesh_follow_child`):

   ```c
   // Runs in the creator's context inside copy_process(). A new
   // thread shares its creator's tgid and is covered already.
   if (clone_flags & IOR_CLONE_THREAD)
       return;
   if (!iormesh_pid_targeted(creator_pid))
       return;
   traced.state = IORMESH_TARGET_TRACED;
   if (bpf_map_update_elem(&target_map, &child_pid, &traced, BPF_ANY))
       ior_count_ringbuf_drop(); // a full map loses the child, counted
   ```

   ior's CO-RE "flavor" structs (`___ior` suffix) stay for the
   contexts. The exit handler removes a tgid once its whole group
   is dead (`iormesh_forget_target`). Userspace never re-inserts
   an entry the kernel may have removed: a pattern match is
   adopted only by updating its still-existing pending entry
   (`AdoptExec`, `BPF_EXIST`), and a match the start scan found is
   removed again if it exited before its insert — "exited" includes
   a task already in `do_exit` (`PF_EXITING` in `/proc/<pid>/stat`),
   whose exit handler has run while its state still reads R/S/D.
   So no entry outlives its process, and a reused PID is not
   traced. The three handlers attach as `bpf.ControlProbes`. The
   correlator registers a forked child at its `task_newtask`
   record (its incarnation is read from `/proc` then, and dropped
   to 0 if it postdates the record — see "Envelope") and drops a
   dead thread's pending enter at its exit.
   Root tests: `TestFollowForkExec` (a traced bash's forked and
   exec'd child is traced and leaves `target_map` once it exited)
   and `TestUntracedForkNotFollowed` (the negative, then the
   positive control). The `fork-exec` workload scenario is not
   written yet; `fd-lifecycle`, `pipe-ipc` and `signals` spawn
   child JVMs and exercise following once their families land.

2. **Target spec** (`internal/target`, done, task pa4):

   ```go
   // Spec says which processes a session traces; exactly one is set.
   type Spec struct {
       PIDs       []uint32
       Pattern    string // glob over the command line or the exe
       ListenPort uint16 // the owners of this listening socket
   }

   func (s Spec) Resolve(p ProcFS, self uint32) ([]uint32, error)
   func (s Spec) Matches(exe string, argv []string) bool
   ```

   `iormesh run` takes `-pid`, `-pattern` or `-listen-port`
   (exactly one). The glob's `*` crosses `/` and spaces, `?` is one
   byte, `\` escapes; a pattern without a literal character is
   refused (it would trace every process). `Matches` tries the
   argv joined by spaces, then the exe path — never the comm.
   `-listen-port` resolves the owners once, at start
   (`/proc/net/tcp{,6}` LISTEN inode → `/proc/*/fd`); following
   the port across restarts stays open for M6 (decision #23).

   Pattern sessions load the object with `IORMESH_EXEC_WATCH`
   set: every exec of an untraced process is reported as an
   `exec_argv_event` (exe from the tracepoint, the first 1 KiB of
   the argument block; userspace re-reads `/proc/<pid>/cmdline`
   when it was cut) and marked PENDING in `target_map` first.
   Userspace adopts a match (`bpf.Tracer.AdoptExec`) or dismisses
   it (`IgnoreExec`); a match that exited before the verdict is
   not adopted and is no incarnation. Processes already running at
   start are found by a `/proc` scan after the probes attached,
   skipping the tracer, its ancestors and every other process
   running the tracer's executable, whose command lines name the
   pattern. Matching the whole command line is decision #23's
   default and it is broad: `grep RestartApp`, or an editor on a
   file of that name, matches `*RestartApp*` as well, so a pattern
   should name what only the app's command line carries. While PENDING, `filter()` counts every record the
   process loses: that is the window between exec and insert, and
   `AdoptExec` returns it as the gap, which the correlator writes
   as a `drop` naming the process with `args.drop_reason`
   `attach_gap`, ahead of its first traced event — never silently
   (a lower bound; 0 writes nothing). Children a process forks
   while it is still PENDING are not followed, and their records
   are not in the gap either: until the verdict nothing says the
   tree belongs to the session. A PENDING entry carries its
   exec time, so the dismissal of a posix_spawn child's first exec
   (`jspawnhelper`) cannot remove the entry of its second (the
   app). A pattern session never ends by itself — it waits for
   re-appearance (decision #23) until `-duration` or a signal, and
   a stop drains the records in flight (at most 2 s). PID and port
   sessions end once every target and followed child is gone,
   judged by exit records and by `/proc` (start time, tgid, not a
   zombie; an unreadable start time counts as gone), so a reused
   PID cannot keep a run alive.
   Root test: `TestExecWatchAdoptAndIgnore`. Scenario: `restart`
   (landed ahead, task oa4): a launcher runs `RestartApp` as a
   child JVM, reaps it and runs it again with the identical argv
   (`java <flags> -cp <cp> iormesh.workload.scenarios.RestartApp
   <dir>`); the first incarnation finds no state file (a failed
   `openat`) and writes one, the second reads it and rewrites it.
   `TestRestartPattern` binds the session to the glob
   `*iormesh.workload.scenarios.RestartApp*`, which matches both
   incarnations and never the launcher (`-jar workload.jar
   restart`), so each incarnation emits its own `restart` markers
   and the per-process cut keeps it without fork-following. The
   JVM's own startup runs between exec and the begin marker, so an
   `attach_gap` drop lands before the bracket; the test fails on
   one inside it.

3. **App identity** (done, task pa4): all incarnations matched by
   one pattern share an `app:<project>/<pattern>` node
   (`schema.NodeApp`, `schema.AppNodeID`) with `incarnation` edges
   (`schema.EdgeIncarnation`, app → process), which
   `Graph.Validate` accepts and `correlate.Correlator.Incarnation`
   adds. `restart`'s golden declares them from one `$a1`; the
   normalizer passes `app:` ids through unchanged (they carry
   nothing run-specific). Its bracket prune keeps an `incarnation`
   edge, and so its app node, when the edge's process has events
   in the bracket — the app node never has events of its own
   (`TestRestartGoldenEndToEnd` runs the golden through
   `Normalize`; `TestRestartPattern` runs it against the real
   tracer).

4. **File surface** (done, task qa4): `tracepoints.DefaultSyscalls`
   appends the file surface to M1's set (decision #7: file on by
   default) — create/delete, rename and links, the stat family,
   truncate, timestamps, chmod/chown, xattrs, positional and
   vectored I/O, and `dup*`, which `file-attrs` declares (step 6
   adds `fcntl`). Left out: `access`/`faccessat` (fired at
   syscall frequency, decision #25), `getdents`, `lseek`,
   `fsync`, mounts, zero-copy (step 7's opt-in `zerocopy`).
   Each name is a kind in
   `internal/schema/kinds_file.go` (a unit test keeps attach list
   and kinds in step). The correlator keys the exit on the enter's
   record shape — fd, open, path or name event (`files.go`):

   ```go
   switch en.shape { // an integer compare first: read/write pay nothing
   case shapeOpen: return nil, c.openDetail(ev, p, en, ret)
   case shapePath: return nil, c.pathDetail(ev, p, en, ret)
   case shapeName: c.nameDetail(ev, p, en, ret); return nil, true
   }
   ```

   A path kind's `fd` is its dirfd (`-1` for `AT_FDCWD`); relative
   paths resolve against a dirfd the trace saw opened, an empty
   one (`AT_EMPTY_PATH`, `utimensat(fd, NULL)`) names the fd's own
   file; the cwd is not tracked. `flags` appear only where the
   syscall has them (`statx`'s `AT_SYMLINK_NOFOLLOW`). A symlink's
   text stays unresolved; opens through a symlink keep the link's
   path (resolving it needs the inode capture, not attached). The
   dup kinds create the new fd instance with the old one's path;
   step 6 completes the copy. The three goldens load and pass
   (`TestFileLifecycle`, `TestFileLinks`, `TestFileAttrs`); JDK 25
   issues `statx`, as declared. `file-attrs` now declares
   `fchmodat`'s name resolved against its directory fd, and
   `fchown`'s and `fsetxattr`'s fd paths.
   Not measured yet: S3's decision #25 budget was settled on M1's
   nine syscalls; this step adds about 60 default tracepoints, and
   `newstat`, `statx` and `newfstatat` fire often in a JVM. The
   `spikes/s3/measure.sh` run against #25 is due before M2's
   acceptance (step 11).

5. **Rename-aware paths** (done, task qa4, `paths.go`): each fd
   instance a traced open (or dup) named keeps its absolute path;
   a successful `rename*` rewrites every fd at or below the old
   name, in every traced process (a rename is filesystem-wide),
   and adds an `opened` edge to the new name. Matching is by name,
   not inode: the rename record has no inode, and a directory
   rename moves files of many inodes. What a name match could get
   wrong — a name reused by another file — is narrowed, not
   closed, by marking the fds of an unlinked name, or of a name a
   rename replaced, gone. It stays open for an unlink, recreate or
   rename by an untraced process (no record), a cwd-relative
   unlink or rename (absolute fd paths cannot match it),
   `renameat2(RENAME_EXCHANGE)` (the record's flags are always 0:
   the fds at the new name are marked gone instead of swapped),
   and processes in different mount namespaces (one name, two
   files — M7's containers). The fd metadata kinds (`ftruncate`, `fchown`, `f*xattr`,
   …) report the current path; `read`/`write` carry none
   (decision #25). Unit tests cover a directory rename, a
   cross-directory move, a prefix-sharing sibling, a failed rename
   and reuse after unlink; `file-lifecycle` declares its
   `ftruncate` after the rename on `b.dat`, so the golden proves
   it end to end (`TestGoldenFileLifecycle` offline too).

6. **fd lifecycle** (done, task ra4, `internal/correlate/fdtable.go`,
   modelled on ior's `fdTracker`): each process's fd table is the
   `procState` maps — generation, path, socket binding and
   close-on-exec state per fd.
   - `dup`, `dup2`, `dup3` and `fcntl(F_DUPFD, F_DUPFD_CLOEXEC)`
     make a new instance of the returned number (new `fd_gen`)
     naming the old fd's path *and* socket, so the copy's reads
     and writes join the same file and connection; its
     close-on-exec state is the call's (`dup3`'s `O_CLOEXEC`,
     `F_DUPFD_CLOEXEC`). `dup2` onto itself creates nothing.
     `dup3` reports `flags`, `fcntl` its command as `args.cmd`.
   - Fork (`task_newtask` with the creator's tgid): the child
     gets a copy of the parent's table — same instances and
     generations, one lineage, so the child's next instance of
     a number continues after the parent's — and the graph a
     `forked` edge parent → child. A `CLONE_FILES` sharer is
     copied too (not modelled as shared).
   - Exec (`sched_process_exec`): fds whose close-on-exec state
     is set are closed; so are unknown ones (predating the trace
     or from an untraced syscall), as in ior — keeping an fd the
     kernel closed would hand its path to the next untraced
     instance of the number — except unknown stdio 0–2. The
     state comes from the open and `accept4` flags, `dup3`,
     `F_DUPFD_CLOEXEC` and `fcntl(F_SETFD)`/`F_GETFD`; the JDK
     asks `F_GETFD` of the fds it redirects onto a child's stdio,
     so those are known; the pipes it hands its spawn helper stay
     unknown and get a new generation after the exec (an unknown
     inherited fd above 2 also loses its path and socket).
   - `close_range` is not attached: the JDK 25 child closes its
     fds one by one through `close` (strace-measured). A
     program using it keeps those fds in the table.
   `fcntl` joins the default attach set; a JVM calls it while
   setting descriptors up, not per I/O, and the #25 measurement
   due at step 11 covers it. Unit tests: every copy form, a
   copy onto an open number and of a socket, fork inheritance
   and isolation, exec per close-on-exec source
   (`fdtable_test.go`).
   Normalizer: a process without markers that a bracketed
   process forked inside its bracket — by a `forked` edge, with
   its first event inside the parent's bracket — is cut to the
   parent's window, transitively (`inheritSpans`,
   `TestBracketKeepsForkedChildren`); `prune` keeps a `forked`
   edge when both processes have bracketed events.
   Scenario: `fd-lifecycle` — fd reuse, a `dup` (the JDK's only
   in-process one: `Files.newDirectoryStream`) and stdin/stdout
   inherited by a child JVM (`ProcessBuilder` redirects, `dup2`
   onto 0 and 1 in the child, between jspawnhelper's exec and
   the child JVM's). Its golden now also declares the `forked`
   edge `$p1` → `$p2`. `TestFdLifecycle` passes it under the
   real tracer; `TestForkedChildrenKept` shows pipe-ipc's and
   signals' children kept by the cut (their goldens are step
   7's). The golden format still has no field for a copy's
   returned fd: `fd-lifecycle` declares it through its close.

7. **IPC, signals, UDP, epoll** (done, task sa4): families after
   ior's `-trace-families` concept, defaults per decision #7.
   - **Families** (`internal/tracepoints/trace_family.go`):
     `file` (M1's opens and fd I/O, steps 4–6) and `socket`
     (connect, accept*, bind, sendto, recvfrom) on; `ipc` (pipe,
     pipe2, socketpair), `signal` (kill, tgkill, tkill plus the raw
     tracepoint `signal_generate`), `poll` (epoll_create*,
     epoll_ctl, epoll_wait/pwait/pwait2, poll, ppoll) and
     `zerocopy` (sendfile64, splice, tee, copy_file_range) opt-in:
     `iormesh run -trace-families signal,ipc` (or `all`).
     `bpf.ProbesFor(families)` is the attach set; lifecycle's
     control probes always attach. The flag adds, it cannot drop
     file or socket. ior's own family names (FS, Network, …) are
     not accepted: every syscall of a family here has a schema kind
     and a correlator shape, which ior's broader groups lack.
     Not attached yet: sendmsg/recvmsg (their addresses sit in a
     msghdr), select/pselect6, vmsplice. The default set grows by
     bind, sendto and recvfrom. glibc's send(3)/recv(3) are these
     syscalls, so a stream socket's sendto/recvfrom sends no
     address record (`IORMESH_ADDR_NOT_STREAM`; its connection
     already has the addresses) and pays only the enter-time park
     of the argument by tid; each datagram costs a second,
     232-byte record. Step 11's #25 measurement covers both.
   - **Socket addresses** (`internal/c/sockaddr.c`): the generated
     handlers of bind, connect, sendto and recvfrom call
     `iormesh_addr_on_enter` (parks the address argument by tid) and
     `iormesh_addr_on_exit`, which sends a `sock_addr_event` right
     before the exit record: the socket's own addresses (read from
     the `struct sock`), the argument (read by its own family's
     size, only as far as its parked length — recvfrom's
     value-result length is read back at exit, 0 means no address;
     a sun_path is cut at the length, abstract names included) and,
     for AF_UNIX, the path and `unix_sk(sk)->peer`. The accept exits report an
     accepted AF_UNIX socket the same way; TCP keeps sock.c's
     state records. The hooks are listed in
     `internal/generate/sockowner.go` (`sockAddrHooks`), rendered
     behind `filter()` like the TCP owner hooks.
   - **Signals** (`internal/c/signal.c`): a raw `signal_generate`
     handler — the classic tracepoint names only the target's tid,
     the raw one its `task_struct` — emits a `signal_event` when
     the sender or the target (by tgid) is traced. The current task
     is the sender only of a task-sent signal: siginfo
     `SEND_SIG_NOINFO`, or `si_code` `SI_USER`, `SI_QUEUE` or
     `SI_TKILL`; `SEND_SIG_PRIV` and every other code (timers,
     `SIGCHLD`, `SIGIO`, faults) is the kernel's, with no sender
     (root test `TestKernelSignalHasNoSender`: a child's `SIGCHLD`
     names no traced sender). It
     fires inside kill(2), so the correlator attaches target and
     signal to the pending `kill`, and records a `signal` event on
     a traced receiver (args.sender_pid) unless the signal was
     ignored or not queued; both traced: a `signaled` edge.
   - **Correlator** (`internal/correlate/ipc.go`, `signals.go`):
     non-TCP connections are `sockState`s too, so their events wait
     for a conn id and draw `connected` edges like TCP's. A pipe or
     socketpair creates both fd instances (event fd = first end,
     `args.fd2`/`fd2_gen` the second) naming one connection, fused
     at once; a child that inherits an end, or dup2s it onto its
     stdio, joins the node. A unix stream connection's two ends
     share the sorted (sk, peer) key (`unixTable`). UDP is
     connectionless, so the unit is the *flow*: one (local,
     remote) pair, keyed per datagram; the two sides of a loopback
     flow mirror each other and fuse in a table of their own
     (`datagram`), like TCP's ends. A socket bound to the wildcard
     address sends from what the route picks, which towards a
     loopback peer is the peer's own address — anything else stays
     unresolved, never guessed; the wildcard itself is written as
     no `local_addr`. A flow ends with the fd that carried its first
     datagram (`dropFlows`), so the table stays bounded by open
     sockets; one left by an exec or exit lasts the session.
     epoll_ctl reports the epoll fd, the
     registered one as `args.fd2` and the op as `args.cmd`; the
     waits name the epoll fd and their latency is the block.
     Zero-copy calls report their first fd and the bytes moved
     (the second fd is not captured yet).
   - **Normalizer and goldens**: `args.fd2` is renumbered as an fd
     instance of its process, `target_pid`/`sender_pid` take their
     process's PID ordinal; expectations gain `args.fd2` (an `$fd`
     symbol) and `args.signal`; prune keeps a `signaled` edge when
     both processes have bracketed events.
   Scenarios (landed ahead, task na4; strace-verified on JDK 25,
   26 and 27-ea), now declared in full: `pipe-ipc` (each pipe2
   binds both ends — the JDK makes the child's stdin pipe before
   its stdout pipe — and three fused pipe connections, two shared
   with the child), `unix-socket` (path on bind and connect, one
   fused connection), `signals` (kill with signal 15, then 9; each
   child's `signal` event; two `signaled` edges), `udp-loopback`
   (per-datagram addresses, one fused flow) and
   `tcp-ipv4`/`tcp-ipv6` (full 4-tuples, fused). Integration
   tests (`integrationtests/ipc_test.go`): `TestPipeIPC` and
   `TestSignals` run with `-trace-families ipc` and `signal`,
   the others on the defaults. The first NIO use also makes a
   `socketpair` and throwaway AF_INET6 probe sockets; nothing
   declares them. A workload exiting with the skip code 3
   (`tcp-ipv6` without an IPv6 loopback) ends the harness run with
   `ErrWorkloadSkipped`, which `traceJudged` turns into `t.Skip`
   (`TestSkipCodeIsSkip`) — and `mage integrationTest`'s gate,
   which fails on any skip, says why.

8. **Redaction** (`internal/redact`, cgo-free — M3 and M5 reuse
   it; done, task ta4):

   ```go
   // Policy masks secrets before anything is written: deny globs
   // ('*', '?') over keys folded to lower case without '-', '_'
   // and '.', so "*apikey*" covers --api-key, API_KEY, apiKey.
   type Policy struct {
       ArgvDeny   []string   // "*password*", "*token*", "*secret*", …
       FlagRules  []FlagRule // {Program: "mysql*", Flag: "-p"}, …
       HeaderDeny []string   // "authorization", "cookie", …
       QueryDeny  []string   // "*": every query value (#22)
   }

   func Default() Policy
   func (p Policy) Argv(argv []string) []string
   func (p Policy) Header(name, value string) string
   func (p Policy) URL(raw string) string  // absolute or request target
   func (p Policy) Text(s string) string   // URLs embedded in text
   ```

   A masked value becomes `"***"` and the structure stays:
   `-Dpassword=***`, `--token ***` (a bare denied option masks the
   *next* element, whatever it looks like — `-Dname` without `=`
   excepted, it sets a property to ""), `DB_PASSWORD=***`,
   `https://user:***@host/?q=***` (userinfo always; a lone
   userinfo, a token, whole; an '@' past the first '/' still ends
   a userinfo that looks like `user:pass`). Options too generic to
   deny everywhere are scoped by program (`FlagRule`, matched
   against every program word before the option, so `sudo`,
   `env`, `timeout` and `docker login` are covered): `-p` is
   mysql's password but java's module path, so `mysql -psecret` →
   `-p***` and `java -p mods` stays. An element of several words
   (`"--token x"`, `sh -c "…"`) is redacted word by word, shell
   quoting respected; `Name: value` elements (`curl -H`,
   `--header=`) go through the header denylist, colon pairs
   (`--api_key:x`, `pass:x`) through both. The defaults over-mask
   rather than leak (`--password-stdin` masks its successor); a
   secret under an innocuous name is not recognized — a denylist
   is best effort, and the environment is never captured.
   There is no allowlist in v1 (#22's default). The Java agent
   (M3) cannot import Go: it either sends headers to the tracer
   for `Policy.Header` before the spool or mirrors the defaults —
   M3 decides, keeping `Default()` the one list.

   Capture: every exec of a traced process sends one more record
   after `process_exec_event`: `PROCESS_ARGV_EVENT`, the
   `exec_argv_event` struct of the exec watch (exe, the first
   1 KiB of argv) under its own type, so `internal/cli` records
   it instead of judging it. A cut block is completed from
   `/proc/<pid>/cmdline` only when that starts with the copied
   bytes (the process may have exec'd again, or its PID been
   reused); otherwise the cut argv stays, its partial last element
   masked (`ExecInfo.Cut`). Records are never logged: they carry
   the raw argv. The exec a
   pattern session adopts is recorded too, after its
   `attach_gap` drop. `correlate.Correlator.Exec` builds the
   `exec` event — `args.path` the exe, `args.argv` redacted
   there, before the event exists (`Config.Redaction`, nil =
   `Default()`). The normalizer rewrites argv elements under the
   temp dir like paths. Not recorded: a process already running
   at attach (a `-pid` target, the start scan's matches) has no
   exec, so no argv. Graph node attrs stay `comm` only.
   Scenario: `argv-secret` — the workload starts a child JVM
   with `-Dpassword=S`, `--token S`, `--api-key=S` and a URL
   with userinfo and a query value; its golden declares the
   child's `exec` and lists `S` in `must_not_appear`.
   `TestArgvSecretRedacted` also checks the raw `events.ndjson`
   and `graph.json` and that each shape is in the exec's argv,
   masked. Unit tests: `internal/redact` (table, negatives,
   multi-word, header and wrapper cases, `FuzzArgv`,
   `FuzzArgvOneElement`), `TestExecRecordsRedactedArgv`,
   `TestExecArgvRecordedRedacted` (cli routing),
   `TestCutArgvCompletion` (/proc agrees, unreadable, exec'd
   again).

9. **Native stacks** (opt-in, `-native-stacks`; done, task
   ua4): `bpf_get_stackid(ctx, &stack_map, BPF_F_USER_STACK)`
   into a `BPF_MAP_TYPE_STACK_TRACE` (8192 buckets of 64
   frames, no `BPF_F_REUSE_STACKID`, so an id never changes
   meaning during a session), symbolized in userspace from
   `/proc/<pid>/maps`. Off by default; documented as not useful
   for JIT frames.
   - **Kernel** (`internal/c/stack.c`): every generated enter
     handler calls `iormesh_stack_on_enter(ctx, pid, tid, now)`
     after the hooks that may drop the enter (sampling, the
     read/write fold) and before its record
     (`renderStackHook`; `TestStackHookOnEveryEmittedEnter`
     pins the placement, `TestCommittedHandlersCallTheStackHook`
     the committed file). It sends a 40-byte `stack_event` (tid,
     the enter's own `now`, the id or the walk's negative errno,
     the image tag `mm->arg_start`) ahead of the enter record.
     `IORMESH_NATIVE_STACKS` is a load-time constant, so with the
     flag off the verifier prunes the call and userspace shrinks
     `stack_map` to one entry (it is preallocated, ~4.3 MB). User stacks only: at
     `sys_enter` the kernel stack is the entry path.
   - **Correlator** (`internal/correlate/stacks.go`): a stack
     record is turned into a trace id by `Config.Stacks` and
     parked per tid; `base` hands it to the event whose enter has
     the same time, so a stack whose enter was lost pairs with
     nothing. Without the flag the map stays empty and `base`
     pays a length check. An exec tells the table a new image
     begins.
   - **Table** (`internal/stacks`, cgo-free): ids are per process
     image and kernel id — the same addresses in two images are
     two call sites — numbered from 1. The image is (pid,
     incarnation, tag), the tag being `mm->arg_start`, which the
     stack record carries and `/proc/<pid>/stat` shows (field
     48); a snapshot of `/proc/<pid>/maps` is believed only when
     stat shows the record's incarnation and tag before and after
     the read, so a late record of an exec'd, exited or reused
     process keeps bare addresses. A new stack is read from the
     map (`bpf.Tracer.StackTrace`), its image's maps are
     snapshotted and the files they map opened right away,
     through `/proc/<pid>/map_files/<range>` (the mapped file
     itself: no path under the traced process's control is
     resolved as root), `O_NONBLOCK`, regular files only, because
     a short-lived process takes both with it when it exits; the
     slow part — parsing ELF symbol tables, a 20 MB libjvm once —
     runs on a worker goroutine, behind a `recover` (debug/elf is
     not hardened against hostile files), that writes
     `stacks.ndjson`. Modules are cached by device, inode, size
     and mtime; uncovered addresses are remembered per image; a
     process's ids and snapshots are dropped at its exec and exit
     (and a thread's parked stack at its exit). The first version
     symbolized inline and resolved nothing for the
     non-java-peer's Go peer: the correlator was still parsing the
     JVM's libraries when the peer exited. Frames keep the raw
     address and add module, file offset, GNU build id and
     `.symtab`/`.dynsym` symbol where known; the run's summary
     line adds `native stacks N (inline I; uncaptured M:
     collisions C, faults F, other O)`.
   - **Collisions** (task kd4): with no `BPF_F_REUSE_STACKID` a
     stack hashing to a taken bucket got `-EEXIST`, and
     collisions grow with the square of the distinct stacks
     (2–7 of ~194 per non-java-peer run, enough to flake
     `TestNativeStacks`). Any `bpf_get_stackid` error but
     `-EFAULT` now falls back to `bpf_get_stack` into a 552-byte
     `stack_inline_event` carrying the frames; `Table.InlineID`
     gives such stacks ids by their frames per image. Ids still
     never change meaning.
   - **Schema**: `Event.StackID` (`stack_id`, appended, optional)
     and `schema.Stack`/`StackFrame` with a strict reader
     (`ReadStacks`); docs/schema.md "Native stacks".
   Tests: `internal/stacks` (maps lines, build-id notes, symbol
   lookup; with a fake /proc: opening through `map_files`, a FIFO
   there neither blocking nor parsed, another image or
   incarnation keeping bare addresses, a known miss not
   re-reading; garbage and corrupt ELF and `FuzzSafeReadModule`;
   id rules, a write error ending the trace), the
   correlator's pairing (`stacks_test.go`), schema round trip
   and strictness, `TestOpenStacks*` (a stale table removed when
   off); root `TestNativeStacksOnlyWhenAsked` (a one-entry map
   and no stack record without the flag; with it the full map
   and a stack record stamped with the open's own time and a
   non-zero image tag, readable back); integration
   `TestNativeStacks` (non-java-peer with
   `-native-stacks`: the golden still passes, every `stack_id`
   resolves to a stack of its own process, and the Go peer's
   `accept4` and log `openat` stacks reach `main.*` in the
   echopeer binary). Cost: [s3-overhead.md](spikes/s3-overhead.md),
   "Follow-up (ua4)".

10. **Non-Java peer** (done, task ua4): scenario
    `non-java-peer` (landed ahead, task oa4). Its peer is
    `workload/echopeer`, a cgo-free Go echo server that `mage
    workloadBuild` builds next to the jar — ior's `ioworkload`
    has no scenario that serves a TCP client. It uses raw
    `syscall` calls on a locked main thread (socket, bind,
    listen, `accept4`, read, write, close, then an
    `openat`/write/close of `$tmp/non-java-peer.log`), so its
    declared syscalls are exact and carry TID == PID; it
    brackets itself with the scenario's markers. The JVM spawns
    it, connects over AF_INET, then reads the log back. The
    golden declares `attribution: none` on every peer event;
    the tracer must never attribute a non-JVM process (no
    agent, no Java fields). `TestNonJavaPeer` passes it under
    the real tracer (the peer is followed as the JVM's forked
    child, found by its `exec` of `echopeer`), checks the
    degraded shape on the raw trace — every event `attribution:
    none`, no `java` or `http` payload — and that a default run
    has no `stack_id` and no `stacks.ndjson`; mutations (the
    peer attributed to Java, its connection unresolved) fail.
    In M2 no agent exists, so every process has this shape; M3
    must keep it for the peer once JVM events gain `java`.
    Seen while writing the test: a process's `comm` was what the
    correlator first read for its pid, so a child the JVM forked
    kept the JVM's comm after its exec. Fixed (task hd4): the exec
    record's post-exec comm renames the process for its `exec`
    event, every later event and its graph node; the test now
    also checks the peer's exec and accept4 carry `echopeer`.
11. **Budget (decision #25, from S3)**: in-kernel aggregation of
    hot read/write loops (done, task vc4), after ior's
    `syscall_aggregate_map` — repeated read/write on one fd
    instance by one thread summed per flush into one event with a
    count; every state-changing event stays individual.
    `internal/c/rwagg.c` keeps a per-thread `rw_agg_map` entry of
    four keys (enter trace id, fd, file identity). The generated
    read and write handlers (`internal/generate/rwagg.go`) call
    `ior_on_rw_syscall_enter` — the one enter hook that does not
    flush — then `iormesh_rw_on_enter`, which holds back the enter
    record of a *hot* call (within 1 ms of the key's previous exit)
    once the key has sent four hot calls individually; the exit
    hook `iormesh_rw_on_exit` (given the per-tid exit hook's
    verdict, and clearing the in-flight markers whatever it is)
    folds a held call that returned data within 1 ms. Flush points:
    every other enter hook (`ior_on_syscall_enter*` in filter.c
    flush the thread first); every read or write sent as a single
    event sends all the thread's pending sums first (a held call
    that turns out not hot: the sums, the enter it owes, its exit);
    100 ms of span or the count cap, checked when a call folds; a
    fifth key (LRU); `signal_generate` for a signal the thread sent
    itself (never a kernel-raised one, which may fire in an
    interrupt inside a hook) and `task_newtask` in the creator's
    context; exec and thread exit (which also drop the entry). At
    the session end `traceLoop` sets `rw_agg_stop`
    (`bpf.Tracer.StopAggregation`) before its final drain — from
    then on no hook folds or flushes — and `finish` reads the
    frozen sums (`PendingAggregates`) into the correlator. A flush
    that finds the ring full counts one drop. The record is
    `rw_aggregate_event`; the correlator (`aggregate.go`) writes it
    as a read or write with summed `bytes`/`ret`/`latency_ns` and
    `args.count` (only when ≥ 2, so a single call is a plain
    event). The kernel keys by fd number (iormesh leaves
    `IOR_FILE_IDENT` off, so the identity is 0), so the correlator
    resolves the fd instance by time — `procState` keeps each
    instance's birth and the number's previous instance — and
    requires one instance live over the record's whole span; a
    record straddling another thread's close/dup2/reopen becomes
    an `unattributed` drop. The golden check matches a declaration
    without `args.count` only to a single call, and a lint
    (`TestGoldensDeclareFoldableRunsShort`) keeps goldens to at
    most four declared single reads or writes in a row on one fd.
    `iormesh run -aggregate=false` (rodata `IORMESH_RW_AGGREGATE`)
    turns it off. Tests: `rwagg_harness_test.go` compiles rwagg.c
    against a simulated map and ring and drives the fold, flush
    and stop rules, with mutations; correlator, schema, checker and
    parser unit tests; `TestSyscallBurstFoldsEveryCall`, which also
    requires zero unpaired exits on a run without counted loss.
    Review round 2 found the one silent loss left: a tracepoint
    program the kernel skips under its recursion guard (another BPF
    program running on that CPU) loses its record without a trace;
    `bpf.Losses.Missed` sums the attached programs'
    `recursion_misses` (`progmiss.go`) into `drop` events. Task vd4
    then moved it out of the lost records: the skip comes before
    the target filter, so the count is host-wide possible loss —
    its own `skipped_runs` drop and "skipped runs" summary figure;
    the integration harness still retries such a run. Task 8e4
    found why loaded hosts skipped in every run (Linux 7.2 runs
    per-syscall tracepoint programs preemptibly behind a
    per-program guard) and attaches the syscall handlers through
    two classic `raw_syscalls` dispatchers instead
    (`internal/c/dispatch.c`, `internal/bpf/dispatch.go`). Plus a
    cheaper consumer to reach
    ≤ 2 µs per event (done, task wc4: a hand-written encoder
    byte-identical to `encoding/json`, pooled records recycled,
    batched records read from the mapped ring; validation stays
    on the hot path, it costs ~15 ns once it stopped copying the
    event; numbers in
    [s3-overhead.md](spikes/s3-overhead.md), "Follow-up (wc4)";
    benchmark `go test ./internal/correlate -bench SyscallBurst`,
    profile `iormesh run -cpuprofile FILE`). Measured with
    `spikes/s3/measure.sh` against the #25 budget.

**Done when**: PLAN.md's M2 acceptance holds, including the S3
budget (decision #25) checked by a documented
`spikes/s3/measure.sh` run by #25's paired CPU-time method — not
a CI gate, CI machines are too noisy. M2's two timing items moved
to task md4, which gated M3's acceptance; both pass
([s3-overhead.md](spikes/s3-overhead.md), "Follow-up (md4)").

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

S1 result (JDK 25 and 27-ea; no JDK 21 runtime at the time —
the agent has since run on 21, task sc4): `-javaagent`
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
  bridge/src/iormesh/bridge/StackIds.java    # called by Hooks.enter
  bridge/src/iormesh/bridge/Ring.java        # mmap spool writer
  bridge/src/iormesh/bridge/Brackets.java    # per-thread bracket state
  bridge/src/iormesh/bridge/Watermarks.java  # WATERMARK timer + scan
  bridge/src/iormesh/bridge/Otel.java        # reflective span read
  core/src/iormesh/agent/Agent.java          # premain/agentmain
  core/src/iormesh/agent/AgentArgs.java      # bridge=, spool=, debug=…
  core/src/iormesh/agent/Bridge.java         # bootstrap append + module
  core/src/iormesh/agent/HookTable.java      # loads the tables below
  core/src/iormesh/agent/BoundaryTransformer.java
  core/resources/iormesh/agent/hooks-jdk17.tsv  # 17–20
  core/resources/iormesh/agent/hooks-jdk21.tsv  # 21–24
  core/resources/iormesh/agent/hooks-jdk25.tsv  # 25+
  core/src/iormesh/agent/http/…              # one file per library
  core/src/iormesh/agent/OtelTransformer.java  # arms Otel per copy
  test/src/iormesh/agent/…    # plain-main unit tests + AgentSmoke
  test/src/iormesh/bridge/…   # Hooks/Ring tests, RingStress
  tools/Relocate.java         # shades ASM (build tool)
  third_party/asm/            # ASM 9.11.1 jar, LICENSE, PROVENANCE
magefile_agent.go, magefile_agenttest.go
internal/agentspool/    # Go reader for the ring (cgo-free)
internal/correlate/javajoin.go
internal/javaattach/    # live attach: HotSpot's attach protocol (step 10)
internal/cli/attach.go  # `iormesh attach`, `run -agent`
```

**Steps**

1. Build: `mage agentBuild` → `iormesh-agent.jar` with the bridge
   jar nested as a resource; ASM shaded under
   `iormesh.agent.shaded.asm` (the S1 prototype used
   `java.lang.classfile`, final only in JDK 24 — M3 must run on
   21 and, per #29, 17). Manifest: `Premain-Class`,
   `Agent-Class`, `Can-Retransform-Classes`. Done (task 0b4):
   JDK-only like the workload, `--release 17`. ASM is vendored —
   the released `asm-9.11.1.jar` with its BSD-3 license and a
   PROVENANCE file (URL, SHA-1 matching Maven Central, SHA-256)
   — rather than downloaded by mage: build and tests stay offline,
   and mage checks the pinned SHA-256 before use. Shading needs
   no Maven: `agent/tools/Relocate.java` rewrites the constant
   pool's Utf8 entries (`org/objectweb/asm/` →
   `iormesh/agent/shaded/asm/`) and copies the rest of each class
   file unchanged; the core imports the relocated package
   directly. 9.11.1 reads class files up to Java 28; a JDK newer
   than ASM leaves the class unrewritten (stderr note, no
   attribution), never a broken target.
2. Install, from `premain` and `agentmain` alike: append the
   bridge jar to the bootstrap search, then
   `Instrumentation.redefineModule(java.base, extraReads =
   {bridge's unnamed module})` — without it the rewritten JDK
   methods fail to link `Hooks`. Map the ring, warm up the hot
   path (thread state, `StackWalker`), then retransform. Expect
   the CDS warning on stderr ("Sharing is only supported for boot
   loader classes…") — the workload's stdout contract is intact.
   Done (task 0b4; the ring by task 1b4, step 4): the nested
   bridge jar is copied to a temp file (the bootstrap path takes
   files only) and deleted once the bridge is loaded and warm —
   the JVM keeps the jar open, so nothing is left behind even on
   SIGKILL; the bridge holds `Hooks` *and* `StackIds`, since
   rewritten java.base code cannot see the agent's loader; the
   transformer rewrites one class of its own before the first JDK
   class, so ASM's class loading never lands in a bracket. `Hooks`
   kept S1's `enter(id)`/`exit(id)` with a per-thread bracket
   stack (not the token sketched in S1's Experiment 2): nested
   hooks need the stack anyway. Neither ever throws into the
   target, and the depth stays balanced when the walk or the
   bookkeeping fails (a slot is marked skipped until the bracket
   is fully open; a throwing exit would run the catch-all's exit a
   second time and close the caller's bracket). A second
   `-javaagent` or attach finds the bridge claimed and installs
   nothing. A closed bracket goes to the ring (step 4); with the
   agent option `debug=<path>` it also bumps a per-hook counter,
   and the table is dumped with each row's status (`applied`,
   `missing` = this JDK lacks the method, `unloaded`) and count at
   exit. Any install failure prints `iormesh-agent: disabled: …`,
   removes the transformer and leaves the JVM running; a failure
   before the first rewrite also releases the claim and deletes
   the ring, so a later live attach can still install. `BoundaryTransformer`
   rewrites only bootstrap-loaded classes of the table and only
   their listed methods (exact descriptors); it writes the
   catch-all handler's stack map frame itself instead of
   `COMPUTE_FRAMES`, which would load classes from inside a
   transformer. `StackIds` drops the bridge's own frames by
   package, keeps 10, hashes them with 64-bit FNV-1a (a collision
   would merge two stacks, undetected), and stores a stack's
   frames (`class#method(file:line)`) the first time its id is
   seen, for the ring's STACK record.
3. `HookTable` is data, not code: a table of
   `(class, method, descriptor, hookId, native, allowed syscalls)`
   so decision #29's per-JDK differences become table variants and
   the tracer gets the same table for its uprobe set (S1's hook
   table above). Done (task 0b4): one TSV per variant (columns id,
   kind, class, method, descriptor, native, syscalls) — `jdk17`
   for 17–20, `jdk21` for 21–24, `jdk25` for 25+, chosen by
   `Runtime.version().feature()`, below 17 no attribution. One row
   per overload; kinds come from a fixed set (open, read, write,
   close, seek, socket, bind, connect, listen, accept, shutdown,
   unlink, rename, stat). A test walks each row against the
   running JDK's class bytes: the method exists, has a body, the
   named native is a native method, and the method reaches it
   within three same-class calls (JDK 24 and later route
   `RandomAccessFile.write` through a JFR `implWrite` that 21 does
   not have); deliberately wrong rows must fail it. Beyond S1's
   table, 17 sends socket writes and closes from
   `SocketDispatcher` to `FileDispatcherImpl`'s natives, and
   `FileDispatcherImpl.close` reaches the already hooked
   `FileDescriptor.close`.

   Tests (`mage agentTest`): the transformer rewrites exactly the
   listed methods of a fixture (unlisted ones and an overload stay
   instruction-for-instruction the same), the rewritten class
   verifies and closes one bracket per call on return, exception
   and inner-handler paths, all inside a hand-opened outer bracket
   that must close exactly once; an unknown class, a class without
   a listed method and a non-bootstrap loader are no-ops; `Hooks`
   nesting, busy skips, overflow past 16 levels, mismatched exits,
   a throwing walk and a throwing exit; table parsing and the
   running-JDK check; `StackIds` per call site. Smoke:
   `AgentSmoke` (file streams, RAF, `Files` stat/move/delete, TCP
   loopback with a shutdown) under `-javaagent` must close a
   bracket of all fourteen hook kinds with no `missing` row, and
   `workload.jar file-basic` under the agent keeps its
   stdout/stderr contract and fires its open/write/read hooks.
   Checked on 25, 27-ea and 17 (GraalVM CE 17.0.9, via
   `IORMESH_AGENT_JDKS`); the smoke runs with boot-class
   verification on (`-XX:+BytecodeVerificationLocal`). Live attach
   fired the hooks on 25 and a doubled `-javaagent` installed once
   (by hand, not in `agentTest`). Not run: 18–20, 22–24. JDK 21
   (21.0.6, 21.0.12; task sc4) passes everything, smoke included,
   after one fix: 21 has no `Net.serverSocket(ProtocolFamily)`,
   only `serverSocket(boolean)` and `serverSocket(ProtocolFamily,
   boolean)` — S1's reading of the 21 sources had missed it. The
   `boolean` survives through 24 (`javap` of 22.0.2's and 24.0.2's
   class files; every other hooked signature matches 21) and is
   gone in 25, hence `jdk21` (21–24) and `jdk25` (25+), which
   differ in that one row.
4. The ring (decision #27): one file per JVM,
   `<spool dir>/<pid>.ring` (pid as the JVM sees it), in the
   tracer-owned directory, mapped `MAP_SHARED` by agent and
   tracer. The agent never creates the directory: a missing one
   disables the agent (no tracer is listening). Agent options:
   `spool=<dir>`, `ringsize=<bytes>` (a power of two, 64 KiB–1 GiB,
   default 8 MiB), `watermark=<ms>` (default 10, 0 = only at
   install and exit). Done (task 1b4). The header, little endian,
   version-checked by the reader:

   ```
   0    magic "IORMRING" (stored last, release: header complete)
   8    version u32 = 7     12  header size u32 = 4096
   16   capacity u64        24  pid u64     32  created mono ns
   64   head (writers' CAS, own cache line)
   128  tail (reader's release store, own cache line)
   192  dropped records per type u64[16]
   320  dropped bytes       328  abandoned records
   4096 data[capacity]
   ```

   Records are framed by one 8-byte word `len | type << 32` (len
   counts the word, a multiple of 8) published with a release
   store after the body; the reader copies a record, zeroes it and
   moves `tail`, so every lap starts from zeros, and a record that
   would straddle the end of the data area is preceded by a PAD.
   Writers reserve with a CAS on one shared `head` — not a ring
   per thread, which would need a file or a fixed slot table per
   thread (thread count unknown at setup) and a merge in the
   reader to keep the watermark promise; the stack walk (≈8 µs),
   not the CAS, bounds a bracket's cost. When the reader is a full
   ring behind the writer drops and counts per type — never
   blocks the target. A fill that throws (a `StackOverflowError`
   deep in the target) is published as a PAD and counted as
   abandoned, so the reader never waits on it. Record bodies:

   ```
   PAD       0  (skipped)
   BRACKET   1  hook i32, ktid i32, enter_mono, exit_mono,
                stack_id, thread_id u64        // 48 bytes
   STACK     2  stack_id u64, n u32, 0 u32, n × class_id u64,
                n × (u32 len, UTF-8)
   THREAD    3  thread_id u64, ktid i32, flags u32 (1 = virtual),
                (u32 len, name)
   WATERMARK 4  mono_ns u64    // every bracket that exited
                               // before this is in the ring
   HOOK      5  hook_id u64, 5 u32, 0 u32, kind, class, method,
                native, syscalls (comma-separated), as strings
      HTTP      6  exit_mono, stack_id, thread_id u64, ktid i32,
                status i32, n u32, nreq u32, flags u32 (1 = server),
                exchanges u32, method, scheme, host, path, version,
                local, remote, nreq request headers, response
                headers (step 7)
   CLASS     8  class_id u64, 3 u32, 0 u32, name, loader,
                code source, as strings (task fe4)
   ```

   Version 7 (task fe4) added CLASS, the STACK's class ids and
   the sixteen drop counters (CLASS is the ninth type, which
   moved dropped bytes and abandoned); 4–6 grew HTTP and added
   DNS (`internal/agentspool/format.go` lists each bump).
   Version 3 (task 3b4) added HTTP (step 7). Version 2 (task
   2b4) added HOOK and THREAD's flags: the agent
   writes its whole hook table into the still empty ring at
   install, so the joiner reads each hook's syscall allowlist
   from the ring — whatever variant or `table=` override the JVM
   chose — and the tracer keeps no copy of the TSVs. A virtual
   thread (JDK 21+, `Thread.isVirtual` found reflectively, the
   bridge being built for 17) is flagged in its THREAD, and its
   brackets carry the carrier tid read again at enter and exit
   (two `readlink`s per bracket, virtual threads only), 0 when it
   parked and moved carriers inside the bracket.

   STACK and THREAD are zero-padded to 8 and capped at 16 KiB
   (strings cut to 1024 chars); BRACKET and WATERMARK allocate
   nothing. A thread's first bracket writes its THREAD, a stack's
   first sighting its STACK. Neither is guaranteed to precede the
   BRACKETs naming it: the stack id is visible to other threads
   before its STACK is published, so a second thread on the same
   call site may publish its BRACKET first, and a dropped THREAD or
   STACK is retried later (after a 1 ms back-off, so back-pressure
   costs no allocation per bracket; each failed attempt counts as
   a drop). The joiner resolves ids against everything it has
   read. Sightings of stacks past StackIds' 65 536-entry cap count
   as dropped STACKs. The method table is the tracer's (step 6).
   A thread renamed after its first bracket writes another THREAD
   at its next bracket (an identity compare of `getName()` with
   the name last written, task fe4).

   Brackets are written at exit, so a syscall inside a still-open
   bracket has no record yet; the agent's watermark thread writes
   `WATERMARK` (a memory store, no syscall) so the joiner knows
   when to stop waiting. The S1 prototype had no watermark — it
   drained after exit. The promise holds without locks: a closing
   bracket marks its thread with its enter stamp before stamping
   the exit and clears the mark after publishing; a watermark
   reads the clock, then scans the threads and lowers itself to
   any marked thread's enter stamp. Both sides put a full fence
   between their volatile access and their clock read: the JMM
   does not order `System.nanoTime()` against volatiles, and on
   AArch64 the closing mark could otherwise become visible only
   after the exit was stamped. Install and JVM exit (a shutdown
   hook) write one too.

   The agent's syscalls are the ring's setup in `install`, one
   `readlink("/proc/thread-self")` per thread, and the watermark
   thread's sleeps; the tracer tags them as the agent's (spool
   path; first such readlink per TID before that TID's THREAD
   record; the watermark thread names itself
   `iormesh-agent-watermark` in its THREAD record) — never the
   target's. The exit hook's thread (debug dump) is never
   bracketed.

   Reader: `internal/agentspool` (cgo-free: `syscall.Mmap`,
   `sync/atomic` on the mapped words). `Open` rejects a header it
   cannot trust (`ErrNotReady` before the magic, `ErrVersion`,
   `ErrTruncated`, `ErrCorrupt`); `Drain` hands out `Bracket`,
   `Stack`, `Thread` and `Watermark` records in ring order. A
   framing error (length unaligned, past the lap or past `head`)
   makes the ring `ErrCorrupt` for good; a body that does not
   decode (wrong length, hook ≤ 0, exit before enter, bad UTF-8,
   non-zero padding) is skipped and counted as `Malformed`, an
   unknown type as `Unknown`. `Losses` adds the agent's header
   counters, so delivered + lost = written, the same contract as
   `bpf.Losses`. Tests: round trip, wraparound with PADs, eight
   concurrent writers with a live reader, a lagging reader,
   negative tables for headers, framing and bodies; and from
   `mage agentTest` the Java-driven ones — `RingStress` (eight
   Java threads, a 64 KiB ring lapping under a live Go reader) and
   a `hooks` mode (eight threads closing brackets through `Hooks`
   on one call site while the watermark thread runs every
   millisecond: no BRACKET after a WATERMARK it exited before,
   every id resolves; without the closing mark it failed 6 of 6
   runs), and `workload.jar file-basic` under the agent, whose
   ring must resolve every bracket's THREAD and STACK, match the
   debug dump's counts and keep every watermark's promise. Checked on
   25 (the Go-driven runs), the Java unit tests also on 17
   (GraalVM CE 17.0.9) and 27-ea.
5. The Go side joins the agent bracket to the syscall. Done (task
   2b4) as `correlate.JavaJoin` (`javajoin.go`, `javajvm.go`,
   `javaattr.go`), a `schema.Sink` between the correlator and the
   events file, switched on by `iormesh run -spool DIR`
   (`internal/cli/java.go`; `agentspool.Dir` finds and vets the
   rings — see [schema.md](schema.md#java-attribution-m3)):

   ```go
   // innermost: per tid, brackets sorted by exit (ring order is
   // exit order per thread); the first that ends at or after t and
   // began at or before it is the innermost container.
   func (v *jvm) innermost(tid int32, t uint64) (b agentspool.Bracket, found, ambiguous bool)
   ```

   2b4 shipped the join without the BPF uprobe mark, the hook's
   allowlist its only guard against JVM work of an allowed kind
   inside a bracket. Task td4 built the mark
   (`internal/c/javamark.c`):

   - `iormesh_java_native_enter`/`_exit`, `uprobe.multi` and
     `uretprobe.multi` programs, push and pop a per-tid stack of
     `{cookie, entry ns (CLOCK_MONOTONIC), user sp}` (depth 4, LRU
     `java_mark_map`). The return pops every frame entered at or
     below its stack pointer and never an outer native's, so a
     return whose push was never seen pops nothing it does not own
     (the return probe is attached first for this reason), and a
     push whose return never runs is cleared by the next return
     at its depth or above; deeper than 4, the mark says "unknown"
     (cookie 0) until the thread leaves every native.
   - A generated hook in every enter handler,
     `iormesh_java_mark_on_enter` (`renderJavaMarkHook`, placed like
     the stack hook), sends a `java_mark_event` (cookie, entry) ahead
     of the enter record, stamped with the enter's own time. Load-
     time switch `IORMESH_JAVA_MARK` (`bpf.Config.JavaMarks`, on with
     `-spool`); off, the lookup is pruned, the map shrinks to one
     entry and the uprobe programs are not loaded (no uprobe_multi
     needed). The thread-exit handler deletes the tid's mark.
     `uretprobe(2)` (x86_64 nr 335) is not in the default families,
     and its kind is in no allowlist, so it never joins.
   - `bpf.Tracer.AttachJavaMarks(pid, natives)` finds the JDK's lib
     directory through the JVM's `libjava.so` mapping and opens
     `libjava`, `libnio` and `libnet` as the JVM sees them, never
     by plain path (`javalibs.go`): a mapped one through
     `/proc/<pid>/map_files`, an unmapped one (libnio and libnet
     load lazily) with `openat2(RESOLVE_IN_ROOT)` under
     `/proc/<pid>/root`, all non-blocking and kept only as regular
     files, so a planted FIFO or symlink cannot hang or redirect
     the tracer. It resolves each native's JNI symbol
     (`agentspool.Hook.Symbol`) to a file offset in the opened file
     and attaches the return and then the entry program by that
     descriptor (`/proc/self/fd/N`: the file probed is the file
     parsed), with the JVM's pid (uprobe_multi links: no other
     process traps; Linux ≥ 6.10 or a stable kernel with commit
     46ba0e49, which made the pid filter cover every thread) and
     one cookie per native. The joiner calls it once per ring,
     after the first drain that read the HOOK rows, numbering
     natives by symbol; `iormesh run` also polls the spool once
     before its ready file, so JVMs already running are probed
     before the tracer says it is live. `DetachJavaMarks` drops a
     JVM's links when its thread group dies (pattern sessions),
     and `Close` drops the rest — libbpfgo does not track
     uprobe_multi links.

   The join: the session hands every mark record to
   `JavaJoin.Mark`, keyed by (tid, enter time) = the event's
   (tid, `boot_ns`). An event joins the innermost bracket on its
   tid containing the mark's native entry — the agent's own clock,
   no conversion — whose hook's native has the mark's cookie and
   whose allowlist has the event's kind; the syscall's start,
   converted with the user-space midpoint offset (S1 experiment
   2), must not lie after that bracket's exit, which catches a
   stale mark. Never guessed: no mark (issued outside every hooked
   native: JVM work), another native, no bracket, another kind,
   brackets that overlap without nesting, a folded read/write, or
   a stack the agent did not deliver leave `attribution: none`,
   each counted in the summary line; a JVM whose natives could not
   be probed (an attach error, none of its natives found) keeps
   all its syscalls `none` and is reported and counted, and so is
   one whose threads close 64 brackets after the probe without
   sending one mark (a kernel whose pid filter matches one thread). A
   virtual thread's I/O gets `java.carrier` and the carrier's
   name.

   Release: a JVM's event waits until the ring's watermark is
   5 ms (`Grace`) past the syscall's end — the watermark only
   covers brackets that already exited, and a bracket exits a
   little after its native's last syscall — or 2 s (`Timeout`);
   a syscall that ended before the ring existed is decided at
   once; another process's event leaves once a spool scan after
   it found no ring (scans every 50 ms and on the run loop's
   tick). Brackets are kept 10 s past the watermark: the
   correlator holds socket events up to its 5 s fuse window.
   Tests: `javajoin_test.go` (fake rings: join, every `none`
   reason, nesting, carrier, waiting, timeout, stale ring of a
   reused pid, corrupt ring; td4's negative cases — a read inside a
   `FileInputStream.read` bracket with no mark, another native's
   mark, an unknown native or a stale mark stays `none` — the
   probe once per ring with every native's cookie, and mark
   pruning), `javamarkhook_test.go` (the hook's placement in every
   enter handler), the root test `TestJavaMarkNamesTheNative` (a
   real JVM: its FileInputStream open and read carry `open0`'s
   and `readBytes`' cookies with an entry stamp, while `info0`'s
   `/proc` reads carry none), `agentspool` dir tests (symlink,
   hard link, foreign owner, pid mismatch), and the integration
   test `TestJavaMethodFlow`: `method-flow` under the agent
   satisfies its `expect-pending.json`, every declared op on its
   declared thread and method.
6. Method table and thread table are emitted as their own NDJSON
   files, `methods.ndjson` and `threads.ndjson` (`schema.Method`,
   `schema.Thread`, `schema.TableWriter`;
   [schema.md](schema.md#java-attribution-m3)), keyed by id. Done (task 2b4) for NDJSON; the types carry
   `parquet:` tags; the M4 segment writer (task 8b4) writes them as
   segment tables with `-trace-dir`. A method id is one
   distinct frame (`class#method(file:line)`, parsed into class,
   method, file, line), keyed by the frame text and its class.

   The class-load map (task fe4; PLAN.md M3 "Supporting
   mappings" has the decision) is a third table,
   `classes.ndjson` (`schema.Class`): one row per class a
   written method row names — class, loader label, code source —
   deduplicated across the trace's JVMs, and each method row
   carries its `class_id`. As built:

   ```
   agent/bridge/src/iormesh/bridge/ClassIds.java  # ClassValue ids, CLASS
   agent/bridge/src/iormesh/bridge/StackIds.java  # class id per frame
   internal/agentspool/record.go                  # Class, Stack.Classes
   internal/correlate/javaclass.go                # class rows, renames
   internal/schema/javatables.go                  # Class, CleanCodeSource
   ```

   - **Frame classes only.** `StackIds` walks with
     `RETAIN_CLASS_REFERENCE` (no measurable cost: the VM fills a
     frame's class either way) and, for a new stack only, asks
     `ClassIds` for each frame's class id. Every loaded class
     would cost ring space and agent work for classes no I/O
     stack ever names.
   - **No pinning, no locks.** The id lives in a `ClassValue`,
     whose `computeValue` runs without its lock; the CLASS record
     is written there, so a STACK naming the id lies behind it in
     the ring. Describing a class reads its loader, protection
     domain and module — nothing initializes a class.
   - **Code sources**: a `file:` location as a path, a boot
     loader class of the runtime image as `jrt:/<module>` (the
     platform loader's say jrt themselves), other URLs without
     userinfo, query or fragment — stripped in the agent and again
     by the tracer (`schema.CleanCodeSource`, the same algorithm
     as `ClassIds.clean`).
   - **Losses**: a dropped CLASS is retried when a later new
     stack names the class; a frame whose class never arrived
     gets a method row without `class_id`. The tracer resolves a
     stack once, when its first event joins, so a stack resolved
     before the retried CLASS arrived keeps no class for those
     frames even after it does (the drop is counted).
   - **SecurityManager** (JDK 17–23): the walker retaining
     classes and a class's protection domain need permissions
     the app's frames may lack, so the bridge (boot class path)
     asks for both in `doPrivileged`; a policy refusing even that
     gets the plain walker and stacks without class ids, never a
     failed bracket. `RingTest` runs a child JVM under a
     SecurityManager to check it (skipped on 24+, which has none).
   - **Renames**: a THREAD for a known Java thread is a rename;
     the thread's brackets read before it keep the earlier name
     (up to the latest exit among them still held), later ones
     carry the new name, and `threads.ndjson` gets a row per name
     change. An http or dns record still pending that completed
     after the thread's last held bracket but before the rename
     (any, when none is held) gets the new name.

   Tests: `RingTest` (CLASS ahead of the STACK, labels and code
   sources, rename), `ring_test.go` (round trip, malformed CLASS
   and STACK bodies, the counters' offsets), `javaclass_test.go`
   (dedup, another JAR, a lost CLASS, credentials, renames),
   `javatables_test.go`, `TestJavaAgentWorkload` (every stack's
   class ids resolve; the workload jar and java.base appear) and
   `TestJavaMethodFlow` (every method's class resolves; the
   scenario's frames map to the workload jar, the JDK's to jrt).
7. HTTP hooks: one transformer per library, each emitting an
   `http` event at request completion with `conn_id` (via the
   socket's fd) and `req_seq` (per-connection counter). Start
   with the JDK's `HttpClient` and built-in `HttpServer` — the
   pair the workload uses — then widen per decision #22.
   Headers pass through `redact.Policy` *in the agent*, before
   the spool.
   The workload's pair landed with step 9's scenarios: the
   `http` events their `expect-pending.json` declares (one per
   request, after the I/O that completes it; `req_seq` 1, 2, 3
   on both ends of one connection; query values masked) are
   this step's target. They assume the server's event is
   recorded inside `HttpServe#handle` when the exchange closes
   (`ex.close()`, after the reply is written) and the client's
   when the response returns inside `HttpFetch#call`.

   Done (task 3b4) for the JDK's three, as built:

   ```
   agent/core/src/iormesh/agent/HttpHooks.java       # libraries → ids, probes
   agent/core/src/iormesh/agent/HttpTransformer.java # exit-only rewrite
   agent/core/src/iormesh/agent/http/Library.java    # one class per library:
   agent/core/src/iormesh/agent/http/JdkHttpServer.java
   agent/core/src/iormesh/agent/http/JdkHttpClient.java
   agent/core/src/iormesh/agent/http/JdkUrlConnection.java
   agent/core/src/iormesh/agent/http/Redaction.java  # the agent's policy copy
   internal/agentspool/http.go                       # HTTP record (ring v3)
   internal/correlate/javahttp.go                    # http events
   ```

   - **One library per class, one rewriter.** A `Library` lists
     its `Target`s — (class, method, exact descriptor, which
     argument to pass) — each with a probe, and the packages its
     probes reflect on. `HttpTransformer` rewrites every target
     the same way: before each return, and in a catch-all for a
     method (a constructor only at its returns), it calls
     `Hooks.http(ret, this, arg, id)`. Exit only: an http event
     is stamped when the request is done and joins no syscall.
     Platform-loaded classes are candidates (java.net.http and
     jdk.httpserver live there); each library's module is made to
     read the bridge and to open its probes' packages to the
     agent (`Instrumentation.redefineModule`). A library whose
     module or internals are missing is skipped with a note; a
     JDK generation that changed a signature gets a second row
     (`parseHTTP` lost its `ProgressSource` after 17), reported
     `alternative` in the debug dump.
   - **Hooks.http** hands the three objects to the id's probe
     (a `BiFunction` of JDK types — the core is on another
     loader), which returns the request's strings, already
     redacted, or null (a binding hook, a request already
     recorded). Only then does it walk the stack and write an
     HTTP record; the thread is busy throughout, and marked
     closing from before its stamp until the record is published,
     so the watermark promise covers HTTP records like brackets.
   - **HttpServer**: `HttpExchangeImpl.close`/`HttpsExchangeImpl
     .close` (the handler's explicit close — the golden's
     `HttpServe#handle`) and `ServerImpl$Exchange.run` (the
     handler returned; one that ended its exchange by closing the
     body alone is recorded there, with the JDK's stack); first
     wins per `ExchangeImpl`, whose package-private getters the
     probe calls by reflection.
   - **HttpClient**: `HttpClientImpl.send`'s return, on the
     caller's thread (`HttpFetch#call`). The response does not
     know its connection; `HttpResponseImpl`'s constructor gets
     the JDK's `Response`, whose `localAddress` a binding hook
     ties to the response. `sendAsync` (task wd4): the public
     three-argument overload (the two-argument one and the facade
     reach it, `send` does not) is hooked; its probe takes the
     caller's identity — stack id (walked past the probe's own
     frames, `StackIds.belowAgent`), thread, kernel tid, OTEL span —
     through `Hooks.httpCaller()` and registers a completion on the
     returned future, which hands it with a describer to
     `Hooks.httpLate()`: the describer runs like a probe (busy
     thread, meta) on the completing thread and the record goes out
     under the caller's identity. A future already done at the
     return, or completing inline while the completion is
     registered (same thread), is recorded right there; one that
     fails records nothing; a completion that lands on a busy thread
     counts as a lost HTTP record (numbering stops, never shifts).
     Review of wd4: the future runs dependents last-registered
     first, so the agent's completion runs after the app's callbacks
     and `get()`'s waiter — its stamp is not the response's arrival.
     Every `sendAsync` record is therefore flagged
     `HTTP_UNORDERED` (flags bit 2) with no start: the joiner gives
     it no `req_seq` and stops numbering its connection's client
     end. Review round 2: that is not enough — the connection goes
     back to the pool when the async body is read, so a sync `send`
     reusing it can be recorded before the async record and take its
     ordinal. `sendAsync` is therefore also `marked()`: its entry
     calls `Hooks.httpMark`, which writes an HTTP record flagged
     `HTTP_ASYNC_MARK` (bit 4, empty strings, under the watermark
     promise) before the request can reach a connection, and every
     java.net.http record carries `HTTP_NET_HTTP` (bit 8; ring
     format 6). The joiner keeps the JVM's earliest marker stamp and
     numbers no java.net.http client request completing after it
     (`netHTTPNumbered`); such a request is also released only once
     the JVM's watermark passed its stamp (or by the timeout, then
     unnumbered), so no earlier marker can still be unread.
     HttpURLConnection and server ends are unaffected.
   - **HttpURLConnection**: `getInputStream0`'s first exit with a
     response code (`getResponseCode` and the header getters go
     through it; a 404 throws after reading the response), reading
     `responseCode` and the request and response `MessageHeader`s
     by reflection (calling `getResponseCode` would reconnect when
     no response came); `HttpClient.parseHTTP` binds the socket's
     ends, which `getInputStream0` drops after a body-less reply.
   - **Connection by socket ends, not fd**: the libraries hand out
     addresses through public API, a descriptor only through deep
     internals. The joiner indexes every socket event's ends under
     its `conn_id` and looks a request up by both ends, else the
     remote, else the local one — answering only when one
     connection is known there; `req_seq` is counted per process,
     connection and end (client or server: a same-JVM loopback
     call has both) in completion order, and left out over HTTP/2,
     without a connection, once the JVM may have lost an HTTP
     record (dropped, abandoned, malformed, or a hook call that
     failed — the agent counts it as dropped), and from a request
     that took several exchanges on (review of 3b4: a client
     library's own redirect or 401/407 retry — HttpClient's
     `previousResponse` chain, HttpURLConnection's `parseHTTP`
     count — is one request to the caller and several to the
     server). HttpURLConnection reaches `getInputStream0` again
     from inside it while following, so only the outermost exit
     records (a stack walk counts the frames). The joiner inserts
     the http events by time (each waits until an event that
     completed 5 ms after it arrived, or 2 s) and therefore numbers
     its output `seq` itself.
   - **Redaction twice**: `Redaction` (agent) masks denied headers,
     URI-valued headers (Location, Content-Location, Referer,
     Origin) as URLs even when relative, every query value, and
     URL userinfo with `internal/redact`'s authority heuristic;
     the joiner applies `internal/redact` again.

   Tests: `mage agentTest` — `HttpTransformerTest` (a fixture's
   ctor, return, last-argument-after-a-long and throw paths hand
   over the right objects; static and primitive targets refused),
   `HttpProbeTest` (redaction, addresses, the HttpClient probe on
   a fake response), `HttpHooksTest` (the HTTP record, quiet and
   throwing probes, oversized headers), and `AgentSmoke` now does
   one request through each library with a spool, so every JDK in
   `IORMESH_AGENT_JDKS` must record four (checked on 17, 25 and
   27-ea); Go — `internal/agentspool` (HTTP decode, malformed,
   drops), `internal/correlate` (`javahttp_test.go`: order,
   waiting, conn index, numbering rules, end last), `internal/
   schema` (`kinds_http_test.go`), `internal/normalize`
   (`ExpectedHTTP`); integration — `TestHTTPPlain`,
   `TestHTTPTLS`, `TestHTTPRedaction`, `TestOtelHTTP`
   ([testing.md](testing.md#m3-scenarios-ahead-of-the-agent)).

   Latency and sizes (task wd4): a `Target` marked `stamped()` —
   the method that begins a request: `HttpClientImpl.send`,
   `getInputStream0`, `ServerImpl$Exchange.run` — also calls
   `Hooks.httpEnter(this, id)` first thing, and its exits call
   `Hooks.httpExit`, which pops that stamp (a per-thread stack in
   `Brackets`, 8 deep, balanced past it; constructors are never
   stamped). Every probe gets the innermost open stamp and its
   `this` (`a[3]`) and adopts the start only when it is provably
   its request's: the stamped method's own exit
   (`Request.ownStart`), or for `HttpExchangeImpl.close` a dispatch
   whose `tx` is the closing exchange — never a stamp of another
   call. The probe's `long[]` meta (`Ring.M_*`) carries status,
   counts, flags, exchanges, start and the body sizes; ring format
   5 adds the start and both sizes to HTTP's fixed part. Sizes are
   the libraries' own numbers: HttpClient's publisher length and
   the response's Content-Length, HttpURLConnection's poster size
   or streaming count (read at `parseHTTP`, before
   `getInputStream0` lets go of them), HttpServer's
   `reqContentLen` and `rspContentLen` (0 means chunked or none —
   the reply headers tell which). The joiner turns the start into
   `boot_ns`/`latency_ns` and the sizes into `http.req_bytes` and
   `resp_bytes`. Tests: `HttpHooksTest` (record words, entry
   stamps and their nesting), `HttpTransformerTest` (stamped
   exits, the throw path, constructors never stamped),
   `HttpProbeTest` (declared sizes, adoption), Go decode and join
   tests, goldens `http-plain`/`http-tls` declare the sizes and
   every integration HTTP run checks each event has a latency.

   `sendAsync` is covered by the `http-async` scenario (task wd4:
   http-plain's calls through `sendAsync`, to `localhost`),
   `TestHTTPAsync`, `HttpProbeTest`'s completion case and the
   smoke's extra request (six HTTP records per JDK).

   DNS hooks (#8, task wd4): `JdkDns` is a library of one target,
   the public static `InetAddress.getAllByName(String)`, marked
   `lookup()`: `HttpTransformer` now takes static methods (null as
   `this`, arguments from slot 0) and calls `Hooks.dns` at a
   lookup's exits. Its probe returns `{host, address…}` only for a
   mapping new to the JVM (a bounded map of host → sorted address
   set), never for literals, the empty host or a failed lookup;
   `Hooks.dns` writes a DNS record (ring type 7) with the stack, and
   a lookup on a busy thread is the agent's own (an HTTP probe
   parsing a literal) and records nothing — no HTTP loss either.
   The joiner queues DNS records with the HTTP ones and emits `dns`
   events (`internal/correlate/javadns.go`; schema `KindDNS`,
   `Event.DNS`; normalize `ExpectedDNS`). Tests: `HttpProbeTest`
   (mappings, rotation, literals), `HttpHooksTest` (record, busy),
   `HttpTransformerTest` (static target, throw path), agentspool
   DNS decode, the join's order, the `http-async` golden's lookup
   of `localhost`, and the smoke's `dns` kind on every JDK.

   Third-party libraries (task de4, #22's order). The JDK's
   libraries live in the bootstrap and platform loaders; these
   live in whatever application loader the app (or its
   container, per web app) defines them in, so:

   ```
   agent/core/src/iormesh/agent/BridgeSight.java   # loader sees bridge?
   agent/core/src/iormesh/agent/http/Handles.java  # API per class
   agent/core/src/iormesh/agent/http/Servlet.java  # HttpServlet
   agent/core/src/iormesh/agent/http/ApacheHttpClient.java
   agent/core/src/iormesh/agent/http/Slots.java    # exchanges
   agent/core/src/iormesh/agent/http/OkHttp.java   # OkHttp 4/5
   agent/core/src/iormesh/agent/http/Netty.java    # Netty's HTTP/1.x codec
   workload/libs/                                  # module, jar, vendored libs
   ```

   - **A `Library` is `appLoaded()`**: no module to open, nothing
     resolved at install — its targets are listed whether or not
     the app has it. `HttpTransformer` rewrites a third-party
     class only in a non-JDK loader (a JDK class only in the
     bootstrap or platform loader), and only when `BridgeSight`
     says the loader resolves `iormesh.bridge.Hooks` to the
     bridge's own class (an OSGi framework without boot
     delegation would otherwise fail the app's method at its
     first return); a named module gets the read of the bridge
     added, or is skipped. The answer is kept per loader in a
     weak map and computed with no lock of the agent held
     (`Class.forName` may load through the app's loader — 4b4's
     live-attach lesson). A live attach retransforms the
     third-party classes already loaded, through the same check.
   - **Store scan**: before rewriting a third-party class, a first
     ASM pass refuses every listed method whose code stores into
     `this` or a passed argument's slot — the catch-all's
     hand-written frame declares those with their declared types,
     which javac never contradicts but another compiler might, and
     a VerifyError would fail the app's class. (The JDK's targets
     are not scanned: `getAllByName` reassigns its `host` with a
     `String`, which verifies.)
   - **Two arguments**: a `Target` may name a second argument
     (`withSecond`); the pair goes to the probe as one `Object[]`.
     The throw path now hands over the throwable as `ret` (it was
     null), so a void method's probe tells a throw from a return.
   - **Probes** find the library's API in the class of the object
     they are handed (`Handles`: the public lookup, one
     `MethodHandle` per getter — `getMethod` would resolve every
     public method's types and load classes the app never loads),
     cached in a `ClassValue` (as long as the class lives, never
     pinning its loader). They call nothing that changes the
     request. A probe that cannot read a request throws, which
     `Hooks` counts as a lost record (numbering stops, never
     shifts).
   - **Servlet**: `HttpServlet.service(ServletRequest,
     ServletResponse)`, jakarta and javax, entry-stamped, request
     and response as the pair. Only the original dispatch
     (`getDispatcherType()` REQUEST; Servlet 2.5 has none)
     records — a forward, include, error page or async dispatch
     is no new request — and of it only the outermost `service` on
     the thread (review of de4: a JSP under `JspServlet`, Spring's
     `ServletWrappingController`, a Servlet 2.5 forward, which has
     no dispatcher type): `Hooks.httpExit` tells a probe (`a[5]`)
     whether a stamp of the same hook is still open below. Status, headers and the reply's declared
     `Content-Length` (asked for by name: containers keep it
     outside the header map) at service's return; the request's
     `getContentLengthLong`. A request still in async processing
     is recorded then without status or reply size, unordered; a
     service that threw before the reply was committed leaves the
     status unknown. Not seen: a servlet overriding the public
     `service` without calling it (Jersey's `ServletContainer`),
     a filter answering without a servlet. The causing method is
     `jakarta.servlet.http.HttpServlet#service` (`javax.` is a JDK
     prefix to `CausingMethod`, so a javax servlet's event names
     the container's frame below it).

   Tests: `mage agentTest` — `HttpTransformerTest` (an app loader
   that sees the bridge is rewritten, the bootstrap loader, a
   blind loader and no bridge are not; the pair and the throwable
   on both paths; a reassigning method refused),
   `ServletHookTest` (the real targets rewriting a stand-in
   `HttpServlet` from `agent/test/src/jakarta/servlet`: a redacted
   request with addresses, sizes and its own start; a forward
   records nothing; async and throws as above); `mage
   workloadTest` — `workload-libs.jar`'s self-tests, listing,
   two-process pairs and failures; integration —
   `TestHTTPServlet` (`http-servlet`: the JDK HttpClient against a
   servlet in an embedded Tomcat 10.1, one request thread).

   - **Apache HttpClient 5** (classic): `InternalHttpClient` and
     `MinimalHttpClient.doExecute(HttpHost, ClassicHttpRequest,
     HttpContext)` — what every `execute` overload reaches —
     entry-stamped, target and request as the pair, record the
     request on the calling thread when the response is handed
     back (body unread). httpcore5's `HttpRequestExecutor.execute`
     (one exchange on one connection) records nothing: its probe
     finds the request's `doExecute` as the open hook and fills a
     per-thread `Slots.Slot` — exchanges counted (a followed
     redirect, an answered 401/407: the server saw each), the
     connection's socket ends, the version spoken — which the
     request's exit takes and clears both ways out. A request is its
     client and its start stamp together (review of de4): calls nest
     on one thread — an interceptor calling the same client, an
     OkHttp authenticator's own call — and each open one keeps its
     own slot, found by the stamp Hooks offers with the open hook;
     an outer exit clears any slot left above it. A request that
     failed records nothing, unless an exchange had happened: then
     the probe throws, a lost record (numbering stops, never
     shifts). The causing method is the library's
     `InternalHttpClient#doExecute`; the app's call is below it on
     the stack. HttpClient 4.x (`org.apache.http`) is not hooked.

   Tests (Apache): `AgentSmoke` executes one GET through the
   vendored client in a `URLClassLoader` of its own, so the dump
   must show `doExecute` and `execute` applied and recording on
   every JDK of `IORMESH_AGENT_JDKS` (17, 21, 25, 27 checked);
   `TestHTTPApache` (`http-apache`: the client against the JDK's
   HttpServer).

   - **OkHttp 4/5**: `RealCall.getResponseWithInterceptorChain$okhttp()`
     — what `execute()` and the dispatcher's `AsyncCall` both run —
     entry-stamped, records the call's original request and the
     chain's response on that thread (the caller's for `execute`,
     the dispatcher's for `enqueue`, before the app's callback);
     `CallServerInterceptor.intercept(Chain)` fills the call's slot
     like httpcore5's executor (exchanges, `Chain.connection()
     .socket()`'s ends). OkHttp's protocol names map to versions
     (`h2` is HTTP/2, not numbered). A cached response has no
     exchange and no connection. Kotlin-compiled; the store scan
     refuses neither method.
   - **Netty** (4.2, HTTP/1.x codec): no call spans a request, so
     `HttpObjectDecoder.decode` (its output list at the exit, with
     the context) and `HttpObjectEncoder.write` (the message) feed a
     per-channel FIFO (weak map; its lock held only to find or
     change a queue): a request opens an exchange — a server's,
     decoded, stamps the start —, the answer's head (status,
     headers; an interim 1xx is skipped) goes to the oldest one,
     and the answer's last content completes it, recorded with
     `Channel.localAddress()/remoteAddress()`. Server: at the
     encoder's write, before the flush; client: at the response's
     decoding; a close-delimited response's last content comes from
     `decodeLast`, hooked too (only its list's last element, and only
     to complete: its own `decode` ran inside it). Pairing in order
     holds only while nothing of the channel went unseen (review of
     de4), so the channel is poisoned — nothing more recorded on it
     — when its codec predates the agent (a live attach: the codecs'
     constructors, hooked, mark the ones made since), and, counting
     a lost record, when a probe fails, a write throws, an answer
     finds no request of its own, 64 requests wait unanswered, or one
     call completes two exchanges (one record per call). A decode
     that throws still passes its output on, which counts. Netty's
     HTTP/2 codecs are not seen.

   Tests (OkHttp, Netty): `TestHTTPOkHttp` (`http-okhttp`: OkHttp 5
   against the JDK's HttpServer) and `TestHTTPNetty` (`http-netty`:
   the JDK HttpClient against a Netty server, one acceptor and one
   worker event loop), `NettyPairingTest` (stand-ins of Netty's types
   in `agent/test/src/io/netty`: pipelining, 1xx, a throwing decode,
   decodeLast, and each poisoning case), `SlotsTest` (nested requests
   on one client and across calls), `ServletHookTest` (a nested
   service records once; the javax target on a Servlet 2.5-shaped
   stand-in in `agent/test/src/javax/servlet`); `AgentSmoke` also runs one OkHttp call from
   the vendored jars in a loader of their own, so the Kotlin targets
   are rewritten, verified and recording on every agent-test JDK (17,
   21, 25, 27 checked). The servlet and Netty hooks run only under
   the integration suite's JDK (25).

   Not hooked: Apache HttpClient 4 (`org.apache.http`) and its async
   client, OkHttp 3, Netty 4.1 (untested: a constructor the list
   misses leaves its codecs unmarked, so their channels record
   nothing — never a wrong pair), Jersey's `ServletContainer`
   (overrides `service`).
8. OTEL (done, task 4b4): read `Span.current().getSpanContext()`
   reflectively in the app's class loader at `enter` and add the
   ids to the bracket. No OTEL dependency in the agent. The `otel`
   scenario (task za4, below) is the target: run it as `java -jar
   workload-otel.jar otel --role server|client` (`mage
   workloadOtelBuild`). Its golden (`expect-pending.json` until
   this step promoted it to `expect.json`) puts an
   `otel` object (`trace_id`, `span_id`, the declared ids of
   `OtelScenario`) on every event inside a span: each client
   event of a call (connect, write, read, the last call's
   close — `HttpURLConnection` does them on the calling
   thread), and each server reply write and http event (the
   span is current from the start of `HttpServe#handle` until
   after `ex.close()`). The server's accept and request reads
   happen before `handle`, outside any span, and declare no
   ids. The client's http events assume an
   `HttpURLConnection` hook (decision #22's v1 set) that
   records the response inside `OtelFetch#call` once
   `getResponseCode` returns — built by step 7.

   As built:
   - **When to read**: only once the app has initialized OTEL's
     context storage. `OtelTransformer` rewrites the static
     initializer of `io.opentelemetry.context.LazyStorage` (any
     app class loader) to end with `Hooks.otel(LazyStorage.class)`
     inside a catch-all — a loader that cannot see the bridge
     gets a linkage error there, which must not fail OTEL's
     initializer. `Otel.arm` then resolves `Span` and
     `SpanContext` in that loader through the public lookup and
     reads once, so the classes a read touches load inside the
     app's own OTEL start. The agent never initializes OTEL
     itself: `LazyStorage`'s initializer runs a ServiceLoader
     (jar I/O, jar locks) and freezes the storage wrappers, so
     doing it from a hooked I/O call could deadlock or change
     the app's OTEL. A live attach arms each `LazyStorage`
     already loaded — one caught between loading and
     initializing (OTEL loads it only to call into it, so a
     window, not a state) is then initialized on the attaching
     thread. Arming locks only to dedupe and publish, never
     across the resolve and first read: on attach that read may
     wait for an app thread's `LazyStorage` initializer, which
     itself ends in arming (review of 4b4; `OtelAttachRaceTest`
     forces that order). Up to eight copies (class loaders) are
     read, a ninth is refused and counted as a fail; readers are
     held weakly — a `ClassValue` on `LazyStorage` keeps a
     reader alive exactly as long as its class, so the agent
     pins no undeployed web app.
   - **Hot path**: one volatile read without OTEL; with it, one
     handle call (`Span.current().getSpanContext()` composed),
     after the stack walk and before the enter stamp (outside the
     bracket's window). The span context is compared by identity
     with the thread's last one (held weakly), so `getTraceId`,
     `getSpanId` and the hex parse run once per span and thread.
     Ids are numbers in the ring: trace id as two longs, span id
     as one, all zero for no span. Cost (an `enter`/`exit` loop
     without ring or stack walk, JDK 25, a loaded host): ≈90 ns
     per bracket without OTEL, ≈145–155 ns armed, in a span or
     not — some 55–65 ns, against the ≈3 µs stack walk every
     bracket pays (S1: ≈8 µs), so inside decision #25's budget.
   - **Ring v4**: a BRACKET inside a span is 72 bytes instead of
     48 (the three id words appended), so brackets of an app
     without OTEL cost what they did; HTTP's fixed part gains the
     three words (zeros for none). v3 was never released; the
     bump makes a mismatched agent and tracer fail loudly.
   - **Join**: a syscall joined to a bracket, and an http event
     with a stack, get `otel` (`trace_id` 32, `span_id` 16
     lowercase hex digits) when the record had a span;
     `schema.Event.Validate` allows `otel` with attribution
     java only.

   Tests: `mage agentTest` — `OtelTest` (bridge: hex ids, an
   armed copy's span read and closed, nested spans, an OTEL
   without its API jar arms nothing and throws nothing, BRACKET
   and HTTP records carry the ids, a throwing reader is no span
   and drops the thread's cache, a ninth copy is refused and
   counted), `OtelTransformerTest` (the real `LazyStorage`
   rewritten, verified and run: armed at the first context use,
   not at class load; a loader without the bridge in sight
   keeps a working OTEL) and `OtelAttachRaceTest` (an attach
   arming a copy whose initializer runs on an app thread: both
   finish, one copy armed — it hung before the fix), all on the
   workload's vendored OTEL 1.67.0 jars in loaders of their own;
   Go — `internal/agentspool` (long BRACKET, HTTP ids, half-set
   ids malformed), `internal/correlate` (`javaotel_test.go`),
   `internal/schema` (`TestOtelRoundTrip`, rejects),
   `internal/normalize` (`otel_test.go`); integration —
   `TestOtelHTTP` against the promoted golden, with mutated
   spans caught, and `TestHTTPPlain` asserts no `otel` without
   OTEL. Not yet: an OTEL distribution whose context storage
   never initializes `LazyStorage` is not seen (untested with
   the OpenTelemetry Java agent).
9. Scenarios first, as always: `method-flow`,
   `http-plain`, `http-tls`, `http-redaction`, `otel`.
   Done for the first four (task ya4), ahead of steps 5–7, and
   for `otel` (task za4), ahead of step 8. `method-flow`
   (solo, `--dir`) issues
   each I/O op from its own method — `MethodFlow$Journal#create`,
   `#append`, `#seal` on `main` at recursion depth 24,
   `MethodFlow$Reader#readBack` on `method-flow-worker`,
   `MethodFlow$Recorder#record` on `method-flow-sink`,
   `MethodFlow$Sweeper#sweep` back on `main` — so each declared
   op names its causing method. The HTTP scenarios run
   `HttpFetch#call` (JDK HttpClient, HTTP/1.1) against
   `HttpServe#handle` (JDK HttpServer, handler on the dispatcher
   thread) on 127.0.0.1: GET, POST and a 404 for `http-plain`
   and `http-tls`, two calls carrying planted secrets for
   `http-redaction`. `http-tls`'s certificate is an EC P-256
   self-signed one made in memory (`SelfSignedCert`, with a
   small DER encoder, `Der`, since the JDK has no public
   certificate builder); the public half goes to the client
   through `<dir>/http-tls.crt`. Each golden is split: the
   loadable `expect.json`, checked against real traced runs
   (JDK 25), and `expect-pending.json` with the `attribution`,
   `java` and `http` declarations (format below and in
   [testing.md](testing.md#m3-scenarios-ahead-of-the-agent)).
   Steps 5 and 7 add those fields to `normalize.ExpectedEvent`,
   then replace each `expect.json` with its pending file and add
   the integration tests. Step 5 did its part (task 2b4):
   `ExpectedEvent.java` (`thread`, `method`) loads, and the rule
   is settled as the goldens assumed — `java.method` is the
   innermost frame outside the JDK's own packages (`java.`,
   `javax.`, `jdk.`, `sun.`, `com.sun.`), as `class#method`
   (`normalize.CausingMethod`); `TestJavaMethodFlow` checks
   `method-flow`'s pending file directly. Step 7 (task 3b4) added
   `ExpectedHTTP` and promoted `method-flow`'s and the HTTP
   scenarios' pending files to `expect.json`; step 8 (task 4b4)
   promoted `otel`'s, the last one.
10. Live attach (done, task ge4; decision #2). Until then the
    agent's `agentmain` existed but nothing loaded it: S1 used a
    throwaway `Attach.java`. `internal/javaattach` speaks
    HotSpot's attach protocol from Go — no JDK on the tracer
    host, every step under a context deadline. It opens the
    JVM's cwd and its root's `/tmp` as directory fds (`/tmp` by
    `openat2` with `RESOLVE_IN_ROOT|RESOLVE_NO_SYMLINKS`) and
    works relative to them only; if `.java_pid<nspid>` is
    missing there, it creates `.attach_pid<nspid>` in the cwd
    (else `/tmp`; `O_EXCL|O_NOFOLLOW`, an existing one accepted
    only as a regular file of root or the JVM's user), sends one
    SIGQUIT through a pidfd and polls for the socket; checks it
    is a socket, connects through `/proc/self/fd/<tmp>/…` and
    refuses unless `SO_PEERCRED` names the target's pid and euid
    (the security review's confused-deputy fixes); sends protocol
    1's `load instrument false <jar>=spool=<dir>` and judges the
    reply (status line, then `return code: 0`, or the JVM's
    message — `Dynamic agent loading is not enabled` becomes
    `ErrDynamicLoadingDisabled`). SIGQUIT kills what does not
    handle it, so a process without `libjvm.so` mapped, without
    SIGQUIT in `SigCgt` (a JVM still starting), or with
    `-XX:+DisableAttachMechanism` in its command line or
    `JAVA_TOOL_OPTIONS`/`JDK_JAVA_OPTIONS` is refused unsignalled
    (`-Xrs` JVMs start their listener at startup and attach
    without a signal). `internal/cli/attach.go` adds `iormesh
    attach -pid -agent -spool [-timeout]` and `run -agent JAR`
    (needs `-spool`): the latter loads into the session's
    starting processes from a goroutine while the main one keeps
    handling records, prints one line per pid, then polls the
    spool so the new rings are marked before `-ready`. A load
    the JVM confirmed but that left no `<spool>/<pid>.ring` (the
    agent disabled itself; checked only for a JVM in the
    tracer's mount namespace) is reported as such. The tracer
    now prints `java agent of pid N joined, K natives probed`
    when it marks a JVM — what `iormesh attach`'s user and
    `TestJavaLiveAttach` wait for. Scenario `live-attach` and
    the three tests: [testing.md](testing.md#live-attach-task-ge4);
    user-facing caveats: [java-attach.md](java-attach.md).
    Checked on 17.0.19 (by hand), 21.0.12, 25.0.4 and 27-ea+35
    (`IORMESH_ATTACH_JDKS`). Not built: attaching to JVMs a
    pattern session finds on exec (they are still starting) and
    across mount namespaces (M7, #14).

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
| `start`     | launches `run` as the transient unit `iormesh-<project>.service` |
| `stop`      | `systemctl stop` of that unit: SIGTERM, last segment, end |
| `status`    | the unit's state plus `<trace-dir>/<project>/state.json` |
| `upgrade F` | verifies staged binary `F`, replaces itself (#1, #28) |
| `uninstall` | stops every unit, removes `/opt/iormesh` |

Built by task 9b4 (`internal/cli`: `start.go`, `control.go`,
`systemd.go`, `state.go`):

```go
// unitArgs is systemd-run's argv. --service-type=exec makes systemd-run
// return only once the binary is exec'd; --collect lets a failed unit
// vanish, its state.json and journal keeping what happened.
func (cfg startConfig) unitArgs(self string) []string {
    return append([]string{
        "--unit=" + cfg.unit, // iormesh-<project>.service
        "--description=iormesh tracer, project " + cfg.run.project,
        "--collect", "--quiet", "--service-type=exec",
        "--property=RuntimeMaxSec=" + seconds(cfg.run.duration+runtimeGrace),
        "--property=CPUQuota=" + cfg.cpuQuota,   // 100%: one core (#25)
        "--property=MemoryMax=" + cfg.memoryMax, // 1G
        "--property=KillSignal=SIGTERM",
        "--property=TimeoutStopSec=" + seconds(stopTimeout), // 30s
        "--", self, "run",
    }, cfg.runArgs...)
}
```

- `start` takes `run`'s flags plus `-cpu-quota`, `-memory-max`
  and `-wait` (default 2m). It validates them as `run` would,
  so a bad flag fails the start, not the journal, and forwards
  the ones set as single `-name=value` argv elements, paths made
  absolute (the unit runs in `/`). Without `-ready-owner` it
  passes `SUDO_UID:SUDO_GID` (none under root's own sudo).
  Through the sudoers rule (`SUDO_UID` set, binary under
  `/opt/iormesh` or the install a symlink) `Main` runs only
  `run`, `start`, `stop`, `status` and `uninstall`; any other
  subcommand is refused (`sudoAllowed`, fail closed).
  Its run is root, so start is confined (`confine.go`, also
  applied to the installed binary's `run` under sudo): no
  `-out`, `-cpuprofile` or `-ready`, `-trace-dir` only
  `/opt/iormesh/traces` (where `state.json` lives too), `-spool`
  only `/run/iormesh/spool/<project>`, `-ready-owner` only the
  sudo caller, no `$` in a forwarded value
  ([security.md](security.md)). A live unit for the project is
  refused — flags change by stop and start (#19) — and a failed
  one reset.
- Every systemd call is an argv through the `commander`
  interface; the unit tests replace it with a fake, so they
  never start, stop or list a host unit. Unit names are built
  only from labels `internal/project` accepts, with the
  `.service` suffix spelled out (a label may contain dots).
- `state.json` is written by the run (atomic rename, 0644), not
  by `start`: `running` once the probes are attached, with pid
  and incarnation, `$INVOCATION_ID`, targets, start and end-by
  times; `ended` with the end reason before the segment writer
  closes (after that its lock is free and the next run of the
  project owns the file). `start` polls `systemctl show` and
  the file until the state carries the unit's invocation id —
  the run attached — or the unit is gone (error, see
  `journalctl -u`) or `-wait` passed (error, the unit keeps
  running). `status` reports a `running` state whose process
  is gone as `dead`: its active minute was never published.
- `stop -project P` refuses a unit that is not live, stops it
  (systemctl waits for the drain, the end event and the last
  publish) and prints the recorded end — and fails when
  `state.json` does not say `ended` (the run was killed after
  the stop timeout, or never attached: its last minute is
  unpublished; task eb4). `status [-project P]
  [-json]` covers one project or every one with a loaded unit
  or a `state.json` under `-trace-dir`, with the count of
  events segments waiting in `ready/`; an unknown project or a
  corrupt or foreign state is an error. `uninstall` takes no
  path — it runs under the sudoers rule — stops every
  `iormesh-<project>.service` first and keeps `/opt/iormesh`
  if one will not stop.
- Tests: argv construction, flag forwarding and the sudo owner,
  rejected flags with no systemd call, the live-unit refusal,
  the wait (attach, early exit, early end, timeout), stop,
  status (running, dead, ended, corrupt, unknown, foreign) and
  uninstall, all against the fake; `TestSystemdUnit` runs the
  lifecycle on the real systemd as root under a unique project
  name, stopping and resetting its unit in `t.Cleanup`.

Segment writer (`internal/segment`, built by task 8b4):

```
internal/segment/writer.go     # Writer: New, Event, rows, Tick, Close
internal/segment/rotate.go     # rotate, publish (tables first), abort
internal/segment/stream.go     # one parquet file: footer, sync, rename
internal/segment/dir.go        # names, ready/ owner, stale-active sweep
internal/segment/watchdog.go   # statfs: percent + free-bytes floor
internal/segment/zstdcodec.go  # ior's one-encoder zstd codec
internal/cli/segment.go        # -trace-dir flags, tee, tick, endReason
```

```go
// rotate publishes the active segment and opens the next. The disk is
// checked first; over the limit nothing rotates and the tracer ends
// the trace. A segment without a row is not published.
func (w *Writer) rotate(now time.Time) error {
    if err := w.checkDisk(); err != nil {
        return err
    }
    if w.empty() {
        w.start, w.rotateAt = now, now.Add(w.cfg.Interval)
        return nil
    }
    if err := w.publish(); err != nil { // tables, then events, fsync ready/
        return err
    }
    w.seq++
    return w.openSegment(now)
}
```

- A segment's files are written in the project directory as
  `.active-<session>-<what>.parquet` and published by `os.Rename` into
  `ready/` (same filesystem: atomic) after the footer is written
  and the file fsync'd; `ready/` is fsync'd after the table
  renames and again after the events rename.
  Names: `<fqdn>-<session>-<start-ts>-<seq>.parquet`, tables
  `<same base>.<table>.parquet`, published before the events
  ([schema.md](schema.md#parquet-mapping)).
- Rotation runs in `Tick`, which the run loop calls on its
  200 ms ticker — never on the per-event path, which only
  validates the event and appends its fields to the open row
  group's column buffers (`segment/eventcols.go`, task ee4: the
  hand-written mapping replaced `parquet-go`'s reflection, which
  cost ~3× the NDJSON sink per event; `newEventColumns` checks it
  against the library's schema for `schema.Event`, and a test
  holds it to the library's `Deconstruct`). A minute (from the
  segment's opening) or 64 MiB (bytes written plus the open row
  group's buffers) rotates. The tables keep `parquet-go`'s
  `GenericWriter`: their rows are rare.
- Compression: ior's `zstdCodec` (one encoder, 1 MiB window)
  passed to the tables' writers as `parquet.Compression`, not the
  library's pooled `parquet.Zstd`; the events file's encoder calls
  its `Encode` directly, page by page.
- `ready/` is created 0755 and `Lchown`ed to `-ready-owner
  UID:GID` (`start` passes `SUDO_UID`/`SUDO_GID` on to `run`);
  a `ready` that is not a real directory fails the start, as does
  a trace or project directory not owned by the euid or writable
  by group or others (docs/security.md). The directories it
  creates are set 0755 whatever the umask; an existing one is
  never chmod'ed (`segment.MakeDirs`). `New` takes an exclusive
  `flock` on `<project>/.lock` (one run per project directory)
  before sweeping stale `.active-<session>-*` files, and holds it
  until `Close` has published.
  Published files are 0644 root-owned: the owner of `ready/`
  can read and unlink them, which is all rsync
  `--remove-source-files` needs.
- `Watchdog.Check()`: `statfs` on `-trace-dir`; trips at
  `-disk-limit` percent used (df's used/(used+avail)) or under
  `-disk-min-free`. It runs in `preflight` before the BPF load
  (failing the run), in `segment.New`, every 5 s in `Tick` and
  before each rotation. A trip in the loop returns
  `ErrDiskLimit`; `endReason` turns it into end reason
  `disk_limit` with no error, `finish` writes the end event and
  closes the writer, which publishes the last segment — exit 0.
- On SIGTERM: the loop ends as `stop`, `finish` writes `end`
  and the writer's `Close` publishes the active segment.
- The writer is closed after the tables (`outputs.close`): the
  stack table's worker may still write rows after the event
  stream closed, so `segmentSink.Close` leaves the writer open.
- Tests: round trip of every nested shape (compared as NDJSON
  lines), zstd on every column chunk, the minute boundary, the
  size cap, idle segments, publish-is-a-rename (same inode,
  0644), tables beside their segment, stale active files, the
  `ready/` owner and symlink, the watchdog's limits (table),
  a trip that ends cleanly with an intact last segment, startup
  over the limit touching nothing; in `internal/cli` the loop's
  disk-limit end through `finish`, the real-partition startup
  check and the tee. The events encoder is held to
  `parquet-go`'s `Deconstruct` over random events and read back
  across several row groups (`eventcols_test.go`); its hand-written
  metadata is checked against the pages it describes, and its
  output for determinism (`eventmeta_test.go`); pyarrow and DuckDB
  read it back value for value with
  `IORMESH_PARQUET_CHECK=<venv>` (`indep_test.go`, a venv with
  `pip install pyarrow duckdb`).
  `BenchmarkWriterEvent` and `BenchmarkWriterJoinedEvent` are
  the per-event cost next to `schema`'s `BenchmarkNDJSONSink`
  (task ee4).

### Controller side

Registry, `~/.config/iormesh/servers.yaml` (`$XDG_CONFIG_HOME`
honored; the same path on macOS), written by `iormeshctl add` and
`rm` (`internal/ctl`, task cb4):

```yaml
servers:
  - host: app1.example.org
    projects:
      - name: shop
        target: {pattern: "*java*orders-service*"}
        flags: ["-duration", "4h"]
      - name: billing
        target: {listen_port: 8443}
  - host: 10.0.4.4                 # enrolled by expansion (M6)
    hop: 1                         # rounds from a hand-added host
    projects:
      - name: shop
        target: {listen_port: 5432}
expand:                            # M6; absent = allow nothing
  allow:
    cidrs: [10.0.0.0/8]            # canonical, no host bits
    hosts: ["192.168.7.*"]         # address-text globs; prefer cidrs
  max_nodes: 16                    # registered hosts included
```

```sh
iormeshctl add -project shop -pattern '*java*orders-service*' \
    app1.example.org -duration 4h      # flags after HOST pass through
iormeshctl rm -project billing app1.example.org   # one project
iormeshctl rm app1.example.org                    # the whole host
```

- Every server has at least one project (#21: the project is a
  manual label kept with the entry); removing the last project
  drops the host. Host + project twice is an error, as is `rm` of
  an unknown host or project.
- A target is a pattern or a listening port, never PIDs: an entry
  outlives the processes. Tracer flags the entry or deploy own
  (`-project`, `-pattern`, `-listen-port`, `-pid`, `-out`) are
  refused after HOST — usually an iormeshctl flag in the wrong
  place.
- Hosts are `[user@]name` from `[A-Za-z0-9._-]`, never starting
  with `-` (an ssh option); IPv6 literals need an ssh_config
  alias. Project labels follow the tracer's rule
  (`internal/project`).
- The file is written atomically (temp file, fsync, rename, fsync
  of the directory), 0600 in a 0700 directory. It is refused when
  it, or its directory, is someone else's or group- or
  other-writable, and when it is over 1 MiB. Pattern and flags
  may not hold control characters. Unknown keys are an
  error, not dropped: M6's `expand:` section (allowlist,
  `max_nodes`) and a host's `hop` are part of the schema (task
  sb4, `internal/federate/expandcfg.go`) rather than tolerated,
  so `add` never loses them.
- `rm` tears the deployment down before it edits the registry
  (task eb4; see `rm` after `collect` below).

`deploy`, as the commands it runs (all through one multiplexed
SSH connection per host; built by task db4, `internal/ctl/deploy.go`):

```sh
iormeshctl deploy [-project P] [-dist DIR] [-key FILE] [-restart] HOST...

ssh H uname -m                           # x86_64|aarch64 -> amd64|arm64
ssh H command -v rsync                   # preflight: fail fast (#12)
ssh H command -v systemd-run
ssh H test -x /opt/iormesh/bin/iormesh   # installed? else bootstrap, #1
# verify dist/iormesh-linux-$ARCH + .minisig locally (#28), then:
ssh H pwd                                # the home, e.g. /home/ops
ssh H mkdir -p -m 0700 /home/ops/.iormesh/staging
rsync -ct dist/iormesh-linux-$ARCH dist/iormesh-linux-$ARCH.minisig \
    H:/home/ops/.iormesh/staging/
ssh H sudo -n /opt/iormesh/bin/iormesh upgrade \
    /home/ops/.iormesh/staging/iormesh-linux-$ARCH
# per project:
ssh H systemctl show --property=ActiveState --value -- iormesh-shop.service
ssh H sudo -n /opt/iormesh/bin/iormesh stop -project shop  # live, changed
ssh H sudo -n /opt/iormesh/bin/iormesh start -project shop \
    '-pattern=*java*orders-service*' -duration 4h
ssh H sudo -n /opt/iormesh/bin/iormesh status -project shop
```

- Every host on the command line must be registered (and carry
  `-project` if given) before any host is contacted, and every
  selected entry's flags must be ones the server's `start`
  accepts: no entry-owned flag (`-project`, `-pattern`, `-pid`,
  …, possible in a hand-edited registry), no `-out`,
  `-cpuprofile` or `-ready`, `-trace-dir` and `-spool` only at
  their pinned paths (`internal/cli/confine.go`). Hosts
  deploy one after the other; a failed host is reported and the
  next one still deployed, and the command then exits 1.
- First install on a host has no binary to `upgrade` with — that
  bootstrap is decision #1, the operator's own sudo or
  configuration management; deploy stops at the `test -x` with
  that message and never tries to install.
- The build is verified on the controller before each host's
  push (per host, not once per arch, so a build replaced
  mid-run is caught at the next host):
  `release.VerifyFile` with the key `iormeshctl verify` pins
  (`~/.config/iormesh/minisign.pub`), which also requires the
  signed name to be `iormesh-linux-$ARCH` (no other arch's build
  renamed into place). A missing binary or signature, a bad or
  foreign signature: nothing pushed, nothing run under sudo.
- Staging is `~/.iormesh/staging` on the server, never `/tmp`
  or `/opt`, named absolute (the home from `pwd`, which must be
  of `[A-Za-z0-9._/-]`): the path is handed to sudo, and a
  relative one would rely on sudo keeping the working directory
  (`Defaults runcwd` changes it). rsync `-ct` without
  `-p`/`-o`/`-l` leaves the files the login user's own, which is
  what `upgrade` requires of a staged file. The staged files are
  kept; `-c` compares checksums, so the next push of the same
  build transfers nothing while a staged copy damaged in place
  is sent again.
- `iormesh upgrade F` verifies `F` and `F.minisig` again
  against `/opt/iormesh/etc/minisign.pub`
  (`internal/cli/upgrade.go`), the same build is a no-op there,
  and its refusals (older build, bad signature, layout) come
  back verbatim in deploy's error — and no project is started.
  `-allow-downgrade` is never passed.
- Under sudo runs only `/opt/iormesh/bin/iormesh` with `upgrade`,
  `start`, `stop` and `status`; `sudo -n` fails instead of
  prompting when the rule is missing. Every argv element is
  single-quoted by `SSH.Run`; the target is one
  `-pattern=`/`-listen-port=` element, so a pattern starting
  with `-` is no flag.
- Restart (#19): a live unit (`systemctl show`, unprivileged) is
  stopped and started again unless the controller's record
  (`~/.local/share/iormesh/deployed.json`, 0600: the start argv
  and build version deploy last launched per host and project)
  shows the same argv and version — then it is left alone,
  which makes a re-deploy of the same build and entry a no-op.
  A new build restarts the session: upgrade replaces the file,
  not the running process, and a deployed fix must not leave
  the old binary running. `-restart` restarts anyway. A failed
  stop (or one whose run did not end cleanly) starts nothing; a start
  failing after the stop is reported as "session stopped, not
  restarted". `iormeshctl rm` drops the host or project from
  the record too.
- Tests (`internal/ctl/deploy_test.go`): a fake `Transport`
  records every call's argv; exact sequences for two hosts of
  two arches, the idempotent re-deploy, `-restart`, a new build
  and a changed entry; refusals — unregistered host, unknown
  project or a flag the server refuses (no call at all),
  unknown arch, rsync missing, no installed tracer, an odd
  home, missing/tampered/foreign/renamed build (nothing pushed;
  checked per host), upgrade refused (no start), start failing
  (reported, the other project and host still deployed), stop
  or start-after-stop failing on a restart; `rm` pruning the
  record.

`collect`, as the commands it runs (built by task eb4,
`internal/ctl/collect.go`; no sudo anywhere):

```sh
iormeshctl collect [-project P]... [-store DIR] [HOST...]

ssh H true                                   # reachable? else H fails
# per project:
ssh H test -d /opt/iormesh/traces/shop/ready # exit 1: never started, skip
rsync -rt --remove-source-files --exclude='.*' --include='*.parquet' \
    --exclude='*' -e 'ssh …' -- \
    H:/opt/iormesh/traces/shop/ready/ \
    ~/.local/share/iormesh/traces/shop/H/
```

- No HOST collects the whole registry; `-project` repeats. A
  host or project the registry lacks — or a named host that
  carries none of the projects — fails before any host is
  contacted. A host named twice is collected once.
- Only `ready/` is fetched, and of it only the non-dot
  `*.parquet` files directly in it (`federate.FetchFlags`,
  shared by `SSH.Fetch` and the real-rsync test): a
  subdirectory, symlink or dot file planted there is neither
  pulled nor removed. The tracer writes the active
  segment as `.active-*` at the project directory's top and
  renames it into `ready/` whole and synced, tables before
  events, so rsync only ever sees final files; and
  `--remove-source-files` deletes a server file only after it
  transferred. Collect keeps no state: run repeatedly during a
  live session, each run moves what was published since the
  last, and between runs the server holds only unsynced
  segments plus the active one. `ready/` is the deploying
  user's (`-ready-owner`), so reading and deleting there needs
  no privilege.
- Local layout, the build's input:
  `<store>/<project>/<host>/<fqdn>-<session>-<start-ts>-<seq>[.<table>].parquet`,
  store `~/.local/share/iormesh/traces`. One directory per
  registry host, not one per project: segment names come from
  the server, and a shared directory would let one host
  overwrite another's. Project and host are checked again
  (`project.Valid`, `federate.ValidHost`) before they become
  path components. The store and project directories are
  created 0700 and refused, not repaired, when foreign-owned
  or writable by others (whoever can write them could swap a
  host directory for a symlink), checked before anything is
  created inside them and, for the store, before any host is
  contacted; Fetch forces the host
  directory to 0700. The build reads `*.parquet` and skips
  dot files, rsync's in-flight temporaries.
- `test -d` answers "no ready/" only with exit status 1;
  anything else (ssh's 255) fails the project.
- Failures: an unreachable host or a failed transfer is
  reported by host and project; the other projects and hosts
  are still collected, and the command exits 1 naming the
  failed hosts. A partial transfer loses nothing: what moved
  is local, the rest stays in `ready/` for the next run.
- Tests (`internal/ctl/collect_test.go`): exact call sequence
  over the whole registry, identical on a second run;
  `-project`/HOST selection and refusals with no call at all;
  unreachable host, failed fetch, missing `ready/` and a
  `test -d` failing with 255; an open store, a store that is a
  file and an open project directory refused; and
  `TestCollectLiveSession`, real rsync with `FetchFlags` over
  a temp "server" with an active file at the project top,
  published segments in `ready/` and a planted symlink,
  subdirectory and dot file — two collects around a rotation
  move exactly the published segments, never the active file
  or the planted entries.

`rm`, as the commands it runs (`internal/ctl/rm.go`, task eb4):

```sh
iormeshctl rm [-project P] [-store DIR] [-force] HOST

ssh H true                               # unreachable: warn, unregister
ssh H test -x /opt/iormesh/bin/iormesh   # exit 1: just unregister
# per removed project:
ssh H systemctl show --property=ActiveState --value -- iormesh-shop.service
ssh H sudo -n /opt/iormesh/bin/iormesh stop -project shop   # if live
# ... collect's test -d and rsync for the project ...
# only when the host leaves the registry:
ssh H test -d /opt/iormesh/traces
ssh H find /opt/iormesh/traces -mindepth 1 -maxdepth 1 -type d -print
# ... stop and collect every other project dir found ...
ssh H find /opt/iormesh/traces -mindepth 2 -type f \
    '(' -path '*/ready/*' -o -name '.active-*' ')' -print   # must be empty
ssh H sudo -n /opt/iormesh/bin/iormesh uninstall
```

- The stop comes before the sync so the last minute is
  published and collected too. `iormesh stop` fails when the
  unit is gone but `state.json` does not say `ended` (killed
  after `TimeoutStopSec`, or never attached): its active
  minute is unpublished, and rm stops there.
- `uninstall` runs only when the host leaves the registry
  (`rm HOST`, or its last project). It would stop and delete
  every project under `/opt/iormesh/traces`, so rm first
  stops and collects the ones the registry does not list
  (started by hand, or dropped by an earlier rm while the host
  was unreachable); a directory whose name is no project label
  is not collected — it cannot be a local path component.
  Then whatever is still in a `ready/` or active refuses the
  uninstall, the files named (the first ten).
- `test` answers "no" only with exit status 1; any other
  failure — ssh's 255 on a dropped connection — is an error,
  never "not installed" or "nothing published"
  (`remoteTest`). `Transport.Run` wraps the remote status in
  an `*exec.ExitError` for that.
- The registry and deploy's record change only after every
  step succeeded; a failed or unclean stop, a failed sync or
  check, leftovers or a failed uninstall exit 1 with the
  registry as it was and, before the uninstall, nothing
  uninstalled. `-force` turns an unclean stop and leftovers
  into warnings and uninstalls: the operator accepts losing
  those files. Rerunning rm is safe: a stopped session is not
  live any more, and what synced is local.
- Tests (`internal/ctl/rm_test.go`): exact sequences for
  `rm HOST` with a live session, `rm -project` keeping the
  host and removing its last project, the sweep of an
  unlisted project (an odd name skipped); leftovers refusing
  and `-force` uninstalling; a failed sync, an unclean stop,
  `test -d`/`test -x` failing with 255, or a failed uninstall
  leaving the registry unchanged; `-force` past an unclean
  stop; the unreachable and the uninstalled host. In
  `internal/cli`, `TestStopUncleanEnd`.

Packages: `internal/federate/registry.go`, `transport.go` (the
`Transport` interface of M7, coded against from M4 on), `ssh.go`
(wraps the `ssh`/`rsync` binaries — no SSH library, the
operator's `~/.ssh/config` must just work), `merge.go`; the
commands in `internal/ctl` (`collect.go`, `rm.go`, which never
call the merge), `cmd/iormeshctl` only dispatching.

The SSH transport:

- `Run(ctx, argv...)` — `ssh [opts] -- HOST 'a' 'b' …`: ssh joins
  its command words into one string for the remote shell, so each
  argv element is single-quoted (`'` as `'\''`); an argument
  stays one argument and nothing in it expands remotely. That is
  POSIX quoting: the deploying user's login shell on a server
  must be a POSIX shell (csh/tcsh read `!` and newlines inside
  single quotes).
- `Fetch(ctx, remoteDir, localDir)` — `rsync -rt
  --remove-source-files --exclude=.* --include=*.parquet
  --exclude=* -e 'ssh [opts]' -- HOST:DIR/ LOCAL/`: a move of
  the non-dot segment files directly in DIR, each deleted
  remotely only once transferred. `-rt`,
  not `-a`: without `-p` the server's modes never reach the
  controller (a 0755 `ready/` would open the local dir), and
  without `-l` a symlink the server planted is skipped, not
  recreated. The remote dir must be an absolute path from
  `[A-Za-z0-9._/-]` without `..`; the local dir is made absolute
  (no `host:` misread) and forced to 0700 after every fetch.
- Options on every call: `BatchMode=yes` (key or agent only, no
  prompt to hang on), `StrictHostKeyChecking=yes` (the host key
  must already be in known_hosts — no trust on first use, never
  `no`), `ControlMaster=auto`, `ControlPersist=60s` and
  `ControlPath=<dir>/%C` — one multiplexed connection per host.
  The dir is `<user cache dir>/iormesh/ssh`, or `~/.iormesh/ssh`
  on macOS, whose `~/Library/Caches` leaves too little of a unix
  socket's 104-byte path; a dir too long for one is refused up
  front. It is 0700 and refused if it is a symlink, someone
  else's, or open to others.
- Tests record the argv through an injected runner, run the
  quoting through a local `sh`, and run `Fetch`'s real rsync
  flags between two local dirs (a 0755 source with a 0666 file
  and a symlink); nothing reaches a host.
  `internal/buildgate` checks iormeshctl links no cgo package and
  cross-compiles for darwin/arm64.

### The merge

Built in task fb4 as `internal/federate` (cgo-free): `key.go`,
`half.go`, `fuse.go`, `source.go`, `merge.go`; the clock bounds
(`clock.go`, `bound.go`, `order.go`) in task gb4. The tracer's
`tcp_state` records carry the four `tcp_sock` fields as
`args.snd_una`, `rcv_nxt`, `bytes_acked`, `bytes_received`
(docs/schema.md, all four or none), captured in task ve4:
`tcp_state_event` grew from 88 to 112 bytes (schema version 2),
the four fields appended after the addresses. `sock.c` reads
them with CO-RE probe reads from `struct tcp_sock` in both
places a record is made — `inet_sock_set_state` and the accept
exit's snapshot — four reads per record, a few records per
connection, so the cost is negligible. The snapshot reads an
unlocked child that softirq may be updating (the client's first
segment), so it re-reads up to three times until two reads
agree and otherwise marks the record `TCP_SEQ_TORN` (in M1's
reserved word, now `seq_status`) with no fields; the merge keys
a server half from its locked state-change records and falls
back to the snapshot only when it has nothing else
(`TestSockSeqStress` races the two over 300 connections). BPF reads them in every
record; the correlator (`carriesSeq`, `internal/correlate/
sockets.go`) passes them on only from `ESTABLISHED` on (see
docs/schema.md), and the normalizer drops them (random ISNs).
`TestSockSeqFields` (root) pins the counters of a loopback
exchange and the key on every record of both ends;
`TestTCPFamilies` splits each real loopback trace into two
pretend hosts and requires `federate.Halves` and `Fuse` to pair
the ends by key alone.

```go
// ISNPair is decision #26's key, oriented the same on both hosts:
// the connecting side's ISN first. NAT never rewrites it.
type ISNPair struct{ Client, Server uint32 }

// keyFrom rebuilds the pair from one record taken at ESTABLISHED
// or later (S2): the connecting side's bytes_acked already counts
// its own SYN, the accepting side's does not; bytes_received never
// counts the peer's SYN. All mod 2^32.
func keyFrom(role string, f seqFields) ISNPair {
    own := f.SndUna - uint32(f.BytesAcked)
    if role == RoleServer {
        own--
    }
    peer := f.RcvNxt - uint32(f.BytesReceived) - 1
    if role == RoleServer {
        return ISNPair{Client: peer, Server: own}
    }
    return ISNPair{Client: own, Server: peer}
}

// Half is one end of a TCP connection as one host recorded it:
// everything under one (host, conn_id).
type Half struct {
    Host, Project, ConnID string
    Procs       []string       // process node ids holding it
    Role        string         // "client" | "server" | "" (unknown)
    Local, Peer netip.AddrPort // differ across NAT; display only
    Key         ISNPair        // valid when HasKey
    HasKey      bool
    KeyConflict bool           // its records rebuilt different keys
    Established uint64         // wall ns of the first keyed record
}

// Source is one host session's trace, whatever its format: the
// NDJSON trace directory (NDJSONDir) or one collected parquet segment
// (Segment, task ib4).
type Source interface {
    Name() string
    Graph(ctx context.Context) (schema.Graph, error)
    Events(ctx context.Context, fn func(*schema.Event) error) error
}

func Halves(events []schema.Event) []Half             // as Merge collects them
func Fuse(halves []Half, window time.Duration) Fusion // Conns, Unresolved
func Merge(ctx context.Context, sources []Source, opt Options) (Result, error)
```

How a half is read from a host's events: the role comes from a
`connect` (or a `SYN_SENT` record) versus an `accept`/`accept4`
on the same conn_id; the key from every `tcp_state` record whose
state has the handshake complete (`ESTABLISHED` and later; `CLOSE`
only on an accepted socket — a child reset in the accept queue —
or after the half was keyed, never on a refused connect). A
conn_id with both a connecting and an accepting end on one host
is a loopback pair the correlator fused already, and a conn_id
without any `tcp_state` (unix, pipe, UDP) is no TCP half; the
merge leaves both alone. A server end whose `accept` the trace
never saw (accepted before the session attached) has no role
evidence and stays `no_role`.

Rules, each a table-driven test (`fuse_test.go`,
`half_test.go`):

- a client half fuses with the server half whose `Key` is equal;
  the 4-tuples are not compared (NAT rewrites them);
- fusion needs a mutual single match: two candidates for one
  half leave every half involved unresolved as `ambiguous`,
  never a coin toss; candidates whose `Established` times differ
  by more than `window` do not count. `Established` is wall
  clock, not boot ns — two hosts' boot clocks share no epoch,
  while NTP keeps wall clocks far inside the window (default
  2 minutes: wider than any NTP offset, inside one ~275 s ISN
  clock wrap) — so fusing needs no clock bound (the bound is
  computed afterwards, from the fused connections). `Established`
  is the earliest keyed record's time. The window assumes
  NTP-disciplined hosts: wall clocks skewed by more than it leave
  both ends `no_match`;
- no role (`no_role`), no keyed record (`no_key`), records that
  rebuild different keys (`key_conflict`), or a key nobody else
  has (`no_match`: sequence-randomizing firewall, SYN proxy,
  untraced far end) leave the half unresolved — no 4-tuple
  fallback, that would be a guess;
- port reuse: the same 4-tuple six times gives six distinct keys
  and six edges (S2's TIME_WAIT and RST reuse phases);
- fused edges get `State: "fused"` and a merged conn_id — the
  hash of the key and both ends' hosts and local ids — that both
  hosts' conn ids map to (`Result.Renames`, for rewriting
  events); an unfused id two hosts happen to share is
  requalified per host, so two connections never pose as one;
- projects do not matter to the join: sources of several
  projects merge together, and `Conn.CrossProject` marks a fused
  edge whose ends two projects traced — on two hosts or, for two
  sessions on one host, over loopback.

The merged graph is the union of the host graphs (sources read
in name order, everything sorted, `Graph.Validate` before it is
returned), so the same traces always give the same bytes;
`testdata/mesh.golden` pins a NAT'ed, port-reusing, two-project
mesh with every unresolved reason (`no_match`, `no_key`,
`no_role`, `key_conflict`, `ambiguous`). A source without events
takes its host from its graph's one host node; with sockets but
no single host node it is rejected.

Clock offsets are built (task gb4): `clock.go` turns each fused
connection into messages, `bound.go` intersects them,
`order.go` orders instants by the result.

- **Messages.** Every receive pairs with the send that queued
  its last byte, matched by stream offset `seq − (iss + 1)`.
  The data records carry `args.tcp_seq`, the sequence number of
  the call's first byte (captured since task xe4, see "tcp_seq
  capture" below). The
  send's instant is its enter, the receive's its exit. The SYN
  is one more client→server message: the later of the client's
  first `connect` enter and its `SYN_SENT` record, against the
  earlier of the server's accept exit and its first `tcp_state`
  record (the child socket exists only once the SYN arrived).
  No server record precedes the SYN-ACK, so the handshake
  bounds one side only.
- **Never a guessed pair.** Sends whose offset ranges overlap
  (exact duplicates from two sessions on one host count once),
  a range that crosses 4 GiB, or a stream that may have passed
  4 GiB (data bytes or a `tcp_sock` counter at 2³² or more)
  give no data messages: an offset there could name two sends.
  So does a host whose trace reports any `drop`: lost records
  make the byte sums that rule out a 4 GiB lap undercount. A
  receive whose byte no traced send holds gives nothing.
  Still open: a stream past 4 GiB whose traced bytes stay
  below it and whose counter-bearing `tcp_state` records all
  came early (a long-lived connection traced in part) is not
  caught.
- **Bounds.** θ = clock_B − clock_A (A < B by name) is below
  `recv_B − send_A` and above `send_B − recv_A`, each at the
  message's own instant. The merge keeps one `ClockBound` per
  host pair and window (`Options.ClockWindow`, default 1
  minute, on A's clock) — `Result.Clocks`, sorted — with the
  raw `Lower`/`Upper` (each may be absent), the conn ids that
  set them and the message count.
- **Drift allowance.** θ moves within a window, so an early
  upper bound and a late lower bound can leave an interval
  that misses θ later in the window. Each bound therefore
  carries `Drift` = `Options.DriftPPM` (default 1 ppm, the
  NTP assumption of S2's "Limits") × the window length — 60 µs
  for a minute — and `Interval()` widens the raw bound by it
  on both sides; `Width()` and `Empty()` use the widened
  interval. A clock that NTP steps or slews harder than the
  assumed rate breaks it: raise `DriftPPM` for such hosts. An
  empty interval stays as it is (`Empty()`), witnesses named,
  never clamped. A pair without a fused connection has no
  bound; neither does a host with itself.
- **Where it surfaces.** The build's view model keeps one
  `Clock` per host pair (task ib4: windows, the narrowest and
  widest two-sided width, one-sided and empty windows counted),
  and the `build` summary prints it; the M5 views will show
  the same.
- **Fusion stays clock-free.** The bound is computed after
  `Fuse`, from the fused connections only, and never picks
  partners: it is proven by those connections, so using it to
  choose them would argue in a circle. The 2-minute window
  remains a guard against ISN-clock wrap; hosts skewed beyond
  it stay `no_match` and get no bound.
- **Order.** `NewTimeline(r.Clocks).Order(x, y)` says
  `Before`, `After` or `Unordered` for two `Stamp`s. One host
  compares its own stamps directly; across hosts x precedes y
  only when y − x strictly exceeds every offset of the
  widened interval. It is sound under the drift assumption,
  not complete: the message that set a bound sits exactly on
  it, so its own send and receive compare `Unordered`, and
  stamps closer than the drift allowance stay unordered too.
  Two hosts that never talked are related through a chain of
  hosts that did, their intervals summed; the instant is
  carried from clock to clock as an interval, and a hop counts
  only when that whole interval lies in one window — no
  extrapolation across time. The chain is chosen blind to time
  (breadth-first, fewest hops, first by name), so it may lack
  a window at the instant while another chain has one: then
  the answer is `Unordered`. Raw timestamps never break a tie.
  The sequence view (M5) builds its cross-host order on it.
- **Open: cycle consistency.** Each pair is intersected on its
  own; bounds around a cycle of three or more hosts (A–B, B–C,
  C–A) that contradict each other — no offsets satisfy all
  three — are not detected yet (a negative-cycle check over
  the hosts per window would).

Tests (`clock_test.go`, `order_test.go`) inject skew into
synthetic traces: +1.23 s, −0.99 s and 0 come back in exactly
the tightest-gap interval; a 10 ms step a minute later gives
two windows with exact intervals, and one window over both is
empty and flagged; no fused connection gives no bound; 90 s
skew fuses and is recovered, 3 minutes fuses nothing; the
handshake alone is one-sided; a stream past 4 GiB or a host
with drops leaves the handshake only; a receive spanning two
sends pairs with the later; three hosts A–B–C order A against
C through B; an offset drifting within the allowance after
its bound never yields a false order (and unpadded it would);
a message's own send and receive stay unordered.
On real traces the rig does the same (task ye4,
`integrationtests/clockskew_test.go`): each emulated host's
tracer stamps `wall_ns` skewed by the test-only
`IORMESH_TEST_CLOCK_SKEW`, and the boot clock the hosts share is
the truth — see [testing.md](testing.md).

**tcp_seq capture (task xe4).** The tracer fills `args.tcp_seq`
in the syscall hooks it already runs (`internal/c/tcpseq.c`),
not in `tcp_sendmsg`/`tcp_recvmsg` kprobes: a kprobe would add a
program run to every TCP send and receive on the host, traced or
not, and share `bpf_prog_active` with the dispatchers (8e4).

- **Which field.** A send appends at `tcp_sock.write_seq` (not
  `snd_nxt`, which lags behind queued data); a receive copies
  from `copied_seq` (not `rcv_nxt`, which runs ahead by the
  unread bytes).
- **Enter.** Behind the sampling and fold hooks — a folded or
  sampled-out call pays nothing — the enter of `write`,
  `writev`, `sendto`, `sendfile64`, `read`, `readv` and
  `recvfrom` walks its fd to the socket with fileident.c's
  typed walk (`bpf_get_current_task_btf`, `bpf_rdonly_cast`; no
  helper calls), and on a TCP socket parks the socket and the
  field's value by tid (`seq_park_map`, LRU, entries kept and
  emptied rather than deleted). A kernel without the kfunc
  captures nothing.
- **Exit and the race.** The hooks hold no socket lock, and
  another thread may send on the same socket meanwhile. The
  exit re-reads the field through the parked socket: the field
  only grows as bytes are queued (consumed), so `end − start ==
  ret` proves that every byte in between was the call's own
  and the call moved exactly `[start, end)`. Anything else —
  two senders interleaving, a send split around another, a
  `MSG_PEEK`/`MSG_OOB` read, kTLS, a TFO send — gives no
  `tcp_seq`. Not covered either: a `read`/`write` the fold held
  at its enter on a hot streak parks nothing, so if it then
  turns out slow (> 1 ms, the read waiting on the peer after a
  burst) it is sent alone without `tcp_seq`; parking held
  enters too would put the walk back on every folded call. A
  stale park (an exit missed) is still exact when
  the check holds; the residue is a field *assignment* inside
  the window (connect, disconnect, `TCP_REPAIR`) whose jump
  equals `ret`, 1 in 2³², and an untraced `dup2` of another
  socket onto the fd mid-call while exactly `ret` bytes moved on
  the parked one.
- **Record.** When the check holds, the exit record itself is
  a `tcp_seq_event` (a `ret_event` plus the socket and the
  sequence number, 48 bytes) in place of the `ret_event`, so a
  call still costs two ring records. The correlator
  (`streamseq.go`) passes it on only for a stream data kind
  that moved bytes on the socket the fd is bound to — the
  record's `sk` must be that socket — and only once the event
  has a conn id; the normalizer drops it (random ISNs).
- **Tests.** `internal/streamseq_root_test.go` (root; a child
  re-run of the test binary does the traffic, since the tracer
  never traces itself): all seven kinds on one connection, each
  pinned to the bytes before it from the handshake's
  `snd_una`/`rcv_nxt`; two threads sending blocking 64 KiB
  buffers on one socket, where every `tcp_seq` given must
  locate exactly the caller's bytes in the received stream
  (about 110–180 of 200 calls carry one; with the check
  disabled the test fails); and a hot loop whose folded records
  carry none. `TestTCPFamilies` splits a real Java loopback
  trace into two pretend hosts and requires
  `federate.ClockBounds` to bound them from both sides around
  0 (≈ 64–145 µs raw width).
- **Cost.** Unchanged per event on `syscall-burst`; on an
  unfolded TCP loop +3 % tracer instructions per event and
  ~500 kernel instructions per data call in the traced process
  (s3-overhead.md, "Follow-up (xe4)").

### Service database

`internal/servicedb` (task hb4), sqlite through a pure-Go driver
(`modernc.org/sqlite` — keeps the controller cgo-free; the
buildgate's darwin/arm64 `CGO_ENABLED=0` build proves it):

```
internal/servicedb/db.go       # Open: 0600 file, 0700 dir, migrations
internal/servicedb/schema.go   # migrations[i]: user_version i -> i+1
internal/servicedb/observe.go  # Observe(sources, federate.Result)
internal/servicedb/traffic.go  # per-conn window, http, dns from events
internal/servicedb/builder.go  # fused / unresolved / same-host pairs
internal/servicedb/store.go    # Import: validate, upsert, supersede
internal/servicedb/rules.go    # naming rules: glob + port -> service
internal/servicedb/query.go    # Endpoints (unnamed first)
internal/servicedb/edges.go    # Interactions: who talks to whom
internal/ctl/services.go       # iormeshctl services list|edges|name|rules|unname
```

```sql
CREATE TABLE conn (                -- one row per observed TCP connection
  host TEXT, conn_id TEXT,         -- the anchor end: client half if traced,
  PRIMARY KEY (host, conn_id),     --   else the server half (stable ids)
  role, state, reason, merged_id,  -- fused | unresolved + the merge's reason
  project, client_{host,project,app,comm},
  server_{host,conn,project,app,comm},
  ip, port, hostname, tls,         -- the endpoint as the client saw it
  first_seen, last_seen);          -- wall ns
CREATE TABLE http_request (        -- per conn and source (v2): method,
  ..., source, requests,           --   scheme, host, path; a count per
  first_seen, last_seen,           --   source, summed at read time
  FOREIGN KEY (host, conn_id) REFERENCES conn ON DELETE CASCADE);
CREATE TABLE naming_rule (         -- human input: never touched by build
  id INTEGER PRIMARY KEY, host_glob TEXT, port INTEGER, -- 0: any
  service TEXT, UNIQUE (host_glob, port));
```

The build path is two calls the build (task ib4) makes after
`federate.Merge`: `Observe(ctx, sources, result)` reads the same
sources once more for what the merge drops — http and dns events
and each connection's last record — and adds the same-host pairs
the host's correlator fused (no halves, so not in the merge's
result); `DB.Import(ctx, snapshot)` folds that in, in one
transaction, after validating every observation (a corrupt one
rejects the whole snapshot).

- **Idempotent.** Rows are keyed by the anchor end's host-local
  conn id, which is the same on every rebuild, and every merged
  column is a MIN, a MAX or keep-if-unknown
  (`COALESCE(NULLIF(new, ''), old)`): importing the same mesh
  twice changes no byte.
- **Exact request counts, per source.** One count per
  connection could only keep the larger part when a pooled
  connection's requests span segments and the older ones were
  deleted. So the count is kept per source (`federate.Source`'s
  name, the unit a build reads; a parquet source should be one
  segment) and summed at read time: a deleted segment's count
  stays, a re-read one rewrites the same number. The client's
  view (its events carry the host) and the server's view count
  the same requests and are kept apart; per connection the
  reads take the larger view's sum, never both. The count is
  exact while one view's segments were all imported at some
  point, and a lower bound otherwise.
- **Fused wins.** A fused verdict never reverts to unresolved; a
  connection first seen from its server end alone (only that
  project built) is taken over by the fused row once both
  projects are built together — its window, TLS hint and
  requests move over — and a later server-only build does not
  add it back.
- **Hostname.** The client's HTTP host when that is a name;
  else the latest DNS lookup by the client's processes that
  returned the endpoint's address before the connection's first
  record on the client's own clock — a best guess, since one
  address can serve many names (a CDN, virtual hosting).
- **Opening.** The path is made absolute first (the driver
  would read `file:` as a URI and `:memory:` as no file);
  transactions are `BEGIN IMMEDIATE` and each migration step
  re-reads `user_version` inside its transaction, so two runs
  queue instead of racing. Reading verbs use `OpenExisting`
  and never create the database.
- **Names are resolved at read time** in Go (`path.Match`, the
  rule's glob against the endpoint's hostname and its IP,
  case-insensitive): a port-specific rule beats an any-port one,
  then the longer glob, then the older rule. A new rule applies
  retroactively without rewriting anything; a duplicate (glob,
  port) is refused rather than overwriting a human's decision.
- **Who talks to whom.** `Interactions` labels an endpoint by its
  service (else `hostname:port`, else `ip:port`) and a client by
  the service its own endpoints are all named, else its
  app id or process name — `iormeshctl services edges -from
  orders` answers "what does orders talk to".
- A half without a role (the merge's `no_role`) is stored but
  names no endpoint: its far end may be a client's ephemeral
  port.

### Build, refresh and show

Built by task ib4 (all cgo-free), hardened after its first review
against segments a compromised server sends:

```
internal/segment/reader.go      # ParseName, ReadEvents, CheckEvents, ReadTable
internal/federate/segments.go   # Segment (a Source), TableFile, LoadSegments
internal/federate/hosts.go      # vetHosts: one host identity per host dir
internal/federate/rebuild.go    # a session's graph from its events (#3)
internal/termsafe/              # trace text made safe for a terminal
internal/viewmodel/             # Model V1, Tally, New, the text views
internal/ctl/build.go           # iormeshctl build
internal/ctl/check.go           # the memory-capped check child, host pins
internal/ctl/show.go            # iormeshctl refresh, iormeshctl show
```

```sh
iormeshctl build [-project P]... [-store DIR] [-db FILE] [-builds DIR]
iormeshctl refresh [-project P]... [-registry FILE] [...] [HOST...]
iormeshctl show [summary|services|edges|health] [-project P]... [-json]
```

- **Hostile input.** A server may be compromised, and every
  collected byte is its choice. The reader refuses a file over
  `segment.MaxFileSize` (twice the writer's 64 MiB cap) and a
  footer over `MaxFooterSize` (8 MiB) or longer than the file
  before parquet-go allocates what the file declares (a footer
  length of 0xF0000000 would be 3.75 GiB), skips the page index
  and bloom filters, and turns the decoder's panics — in the
  footer's column loader as in the pages — into errors naming
  the file. Page sizes and zstd output inside the file are not
  bounded in process: parquet-go sizes them from the file and
  running out of memory is fatal to Go. So every segment is
  decoded first by `iormeshctl __check-segment SPACE PATH`, a
  child of the same executable whose address space is capped
  (`RLIMIT_AS`: what it mapped at start plus 1 GiB; the GC's soft
  limit at half; 2 minutes at most). A hostile file kills the
  child and fails that segment only. Decoding is deterministic,
  so a file the child decoded needs the same memory in the
  build's own process, where it is read again. On macOS
  `RLIMIT_AS` may not hold; the child still keeps a crash out of
  the build.
- **Reading a segment.** `segment.ReadEvents` decodes the events
  file 512 rows at a time into one reused batch; the decoder's
  buffers for a row group come on top. Each row is checked like
  an NDJSON line (`Event.Validate`; empty lists the decoder
  returns become absent ones first, the contract's equality);
  the footer, parsed once per read, must say `events`, this
  schema version and — the caller's `want` — the project,
  FQDN, session and seq of the file's name and directory.
  `CheckEvents` is the child's read: it also requires one host
  id in the file and at least one event. Table files read
  through `ReadTable`; the build reads the methods tables its
  conversations' frames need (task mb4, M5 "Conversations and
  exports").
- **One source per segment.** `LoadSegments` lists
  `<store>/<project>/<host>/*.parquet` (every project directory
  without `-project`; dot files skipped, other entries reported
  as ignored). Each events segment is a `federate.Segment`, named
  `<project>/<host>/<file>` — stable wherever the store lives —
  because the service database counts requests per source and
  only a segment is immutable and may later be deleted (task
  hb4).
- **Host identity** (`hosts.go`). A server names its own files
  and writes its own events, so it could plant
  `hostB.example.org-…` files with hostB's host id in its own
  `ready/`; the merge and the service database (keyed by host
  id, additive) would take them as hostB's. The store directory
  is the one thing a server cannot choose, so each host identity
  (host id, FQDN) is tied to one directory. The first build pins
  each directory to the identity it saw
  (`~/.local/share/iormesh/hosts.json`, 0600, never
  overwritten; a host reinstalled under the same registry name
  needs its entry removed by hand). Bad, then: a segment whose
  host id is not its session's; a session whose directory is
  pinned to another identity, or whose host id or FQDN is
  pinned to another directory — only the claimant, so a new
  server imitating a pinned host cannot get the genuine host's
  sessions dropped (ib4's second review,
  `TestPinnedHostKeptAgainstNewDir`); and, among the unpinned
  rest, every session of a directory holding two identities and
  every session of an identity found under two directories (the
  forgery cannot be told there). One machine registered under
  two names collides with this rule.
- **Clocks and errors.** The reader refuses an event whose
  `boot_ns` or `wall_ns` is past int64 or whose `wall_ns +
  latency_ns` overflows it: the service database stores int64
  and would reject the window, failing the whole build's import
  instead of the one segment (`TestBoundTimes`,
  `TestBuildHostileClockSegment`); servicedb also saturates the
  sum. Every error and warning `iormeshctl` prints passes
  through `termsafe` (`errText`): ssh's stderr, request paths
  and host names in an error come out quoted
  (`TestErrorsAreTerminalSafe`).
- **The graph is rebuilt** (decision #3, the server keeps none)
  once per session, segments in seq order, by the correlator's
  rules (`rebuild.go`, `rebuild_fds.go`): host, process (newest
  comm), runs_on; app and incarnation from an `attach` with
  `args.pattern`; forked from a `fork`, where the child also
  inherits a copy of the parent's fds; opened for successful
  opens and creat, and the new name of a rename that moved a
  file the process held open (opens, closes, dup, dup2, dup3,
  fcntl `F_DUPFD`/`F_DUPFD_CLOEXEC`/`F_SETFD`/`F_GETFD`, forks,
  the fd-creating accept, pipe, socketpair and epoll calls,
  unlinks, renames, and an exec closing the close-on-exec fds
  and the unknown ones above stdio, followed through the
  events); socket and connected for every conn_id, fused for a
  pipe or socketpair, a conn_id with both a connect and an
  accept on the host, or mirrored address pairs (a loopback UDP
  flow); signaled for a delivered signal from a traced sender.
  The `fork` and `attach` events and `F_SETFD`'s `args.flags`
  are task ze4's: before it a segment-built graph lacked app
  nodes, incarnation and forked edges, a child's inherited fds
  and `F_SETFD`'s close-on-exec. Now every judged integration
  run rebuilds its graph from its segments
  (`federate.RebuildGraph`) and must get its `graph.json`
  exactly (`assertRebuiltGraph`, M4's acceptance for
  single-host scenarios); `internal/correlate`
  `TestRebuiltGraphIsTheCorrelators` drives the correlator
  through forks, `F_SETFD`/`F_GETFD`, execs, pipes, attaches and
  a pid reused within the session, and holds the rebuild to its
  graph. The rebuild keeps one fd table per pid, as the
  correlator does: a fork or attach replaces the pid's table, so
  a dead incarnation's leaked fds never move with a later
  rename; a fork or exec touches only its own table. Still out
  of reach (rebuild.go's header): an open with an unreadable
  path (no event, yet a new fd in the correlator) and a rename
  by another process between an exec's record and its exec
  event. Every segment of
  a session returns the session's graph; the merge's union keeps
  one copy. `TestSegmentMergeGolden` writes
  `testdata/mesh.golden`'s traces as rotated segments and merges
  them to the same bytes as the NDJSON merge;
  `TestRebuildCorrelatorExport` rebuilds the correlator's own
  export golden (`internal/correlate/testdata`) to its graph.
- **Bad segments.** A segment the check fails, or whose host
  identity is in doubt, is left out before anything else reads
  it. The build goes on with the rest, names each — by its store
  name, never the absolute path — in the summary and in
  `model.json`, and exits 1 after writing the outputs; the file
  stays in the store. A read failing later (a file changed under
  the build) drops that segment and rebuilds its session's graph
  without it.
- **Passes.** Check (the child), graph, merge (the run's tally
  rides along: `talliedSegment`), `servicedb.Observe` (the
  conversations' http events ride along: `recordedSegment`) and,
  when a kept conversation has a server request, the
  conversations' file pass — five reads of every segment, none
  holding events. What grows with
  the input is the merge's per-connection state (keys, the data
  records' stream offsets for the clock bounds), the graph, the
  service database rows and one tally per session.
- **Outputs.** `<builds>/<project>[+<project>…]/graph.json`
  (`schema.WriteGraphFile`) and `model.json` (indented JSON),
  both written atomically; the builds directory is refused when
  someone else owns it or others can write it, and the build's
  own directory is forced to 0700. Many projects together get
  `projects-<hash>`. Build never contacts a server, and the
  merge is sorted, the import idempotent, the model free of
  clocks and local paths — so a rerun writes the same bytes.
- **The model** (`internal/viewmodel`, V1 of
  docs/visualization.md): `Run` (projects, hosts, sessions,
  segments, events, time range, fused and unresolved TCP
  halves, graph size), `Health` (lost, skipped runs,
  unattributed, attach gap, each session's end, a `Clock` per
  host pair, bad segments, ignored entries), `Services` and
  `Edges` from the service database after the import (filtered
  by `Filter.Projects`, so the whole history of the projects,
  not only the segments still in the store; an edge is `fused`,
  `unresolved` or `mixed`). `show` reads `model.json` alone.
  Every text view passes every string through
  `termsafe.String` first (a string with a control character is
  printed Go-quoted), so an escape sequence in a comm, hostname
  or file name never reaches the terminal raw; `iormeshctl
  services` uses the same function.
- **`refresh`** collects (the registry's hosts, or HOST...) and
  then builds every host directory of the projects; a host
  whose collect failed is reported, the store still built, and
  the command exits 1.
- Tests: `internal/segment/reader_test.go` (round trip through
  the writer at batch and row-group edges, tables, names;
  damaged files — empty, garbage, truncated, footer cut, pages
  zeroed; 3000 single-byte flips weighted to the footer and
  footer lengths past the file, none panicking; `FuzzReadEvents`
  for `go test -fuzz`); `internal/federate/segments_test.go`
  and `rebuild_test.go` (the golden mesh from segments, the
  rebuild against the mesh traces' and the correlator's export
  graphs and per rule, fd copies and exec, bad segments, a
  session of two host ids, a checker failure, host spoofing:
  an identity under two directories, two identities in one, a
  pinned directory); `internal/viewmodel/new_test.go` (tally,
  clocks, services and edges, determinism, escape sequences in
  every text view); `internal/ctl/build_test.go` and
  `check_test.go` — a two-host, two-project mesh written as
  rotated segments: summary, graph and model goldens
  (`testdata/build_*.golden`), byte-identical rebuilds, a naming
  rule shown by name, request counts exact over repeated builds
  and after deleting segments, a damaged segment, refusals,
  every `show` view, `refresh` over a fake transport, the check
  child (a good segment, garbage, and on Linux an address space
  too small to decode in), host pins, an open builds directory.

### The microservices scenario

`microservices` (task jb4) landed ahead of the multi-host work,
on loopback. `Microservices` parses one role's options. `Service`
holds the topology: the roles, the calls each serves and the
hops it makes. `ServiceNode` runs one service from boot to
retirement. `DataStore#save` writes a record the
create-tmp-then-rename way, and `ServiceConfig` provisions and
loads the config. A server's request work (orders' downstream
calls, every server's record) runs through `HttpServe.Work`
on the HttpServer's dispatcher thread, inside the request, as a
synchronous REST service does it. `Scenario.roles()` lets the
scenario name its four roles instead of client/server/solo.

The address options are built (task 1f4). A server binds
`--bind`, and its certificate (`SelfSignedCert.generate(ip)`)
names that address as its only SAN. A caller dials each callee
at `--<callee>-host`. Both are IPv4 literals parsed by
`TcpHosts.ipv4`, defaulting to 127.0.0.1. `ServiceNode` takes
the callees as `InetSocketAddress`es. A caller still pins the
certificate from its own `--dir`, and the deployment puts it
there: the rig shares one `--dir`, and `internal/acceptance`
copies the public file between the hosts. On the Go side,
`Host.Spool` with `Harness.StartWorkloadOn` gives each emulated
host its own agent spool, which only its tracer drains.
`mergeHosts` merges any number of host traces and keeps each
host's own event order. `microservices_hosts_test.go` spreads
the services over three hosts against `expect-spread.json`.
Testing notes: [testing.md](testing.md#m4-microservices-scenario-task-jb4).

### The two-host and NAT'ed pairs

`tcp-two-host` and `tcp-nat` (task kb4) are `TcpHosts`, M0's
client and server with `--bind`/`--host` IPv4 literals. The rig
the microservices run will reuse is built:
`integrationtests/multihost.go` adds `Host`,
`Harness.StartWorkloadIn` (a workload in a host's network
namespace) and `Harness.TraceHosts` (one tracer per host, each
with its own machine id, all attached before the workloads are
released across hosts in order); `multihost_test.go` builds the
three namespaces and the docker-style DNAT and merges the host
traces with `federate.Merge`, renaming each event's conn id by
`Result.Renames` before the merged golden check.
Testing notes: [testing.md](testing.md#m4-two-host-and-nated-pairs-task-kb4).

**Done when**: PLAN.md's M4 acceptance holds, driven by
`integrationtests/multihost_test.go` on two network namespaces
plus one run on two real hosts. That run is `mage m4Acceptance
HOST1 HOST2` (task lb4): `internal/acceptance` holds the
sequence, one check per criterion and a preflight refusing an
unbootstrapped, busy or third host; `fake_test.go` drives it
against fake hosts and a fake iormeshctl. Runbook and status:
[testing.md](testing.md#m4-acceptance-on-two-real-hosts-task-lb4).

## M5 — views, query, eval

The three surfaces — local site, terminal UI, plain text — and
the view model they share are designed in
[visualization.md](visualization.md), including the build order
(V1 lands with M4, V2–V4 here). V1 is built (task ib4, M4
"Build, refresh and show"): `internal/viewmodel` with run,
health, services and edges, and its text views. Extend the model
and its goldens first (conversations and processes, V2); a
renderer never reads the merged graph directly — `graph.svg`, a
drawing of the graph export itself, is the one exception (PLAN.md
M5 "Deterministic SVG export"). Conversations are built (task
mb4, "Conversations and exports" below), per-thread lifelines,
thread stories and processes too (task 0f4, "Threads and
processes").

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

// Sequence renders one conversation of the view model: one
// lifeline per service, one arrow per request and response, file
// ops as notes on the lifeline of the thread that did them.
func Sequence(c viewmodel.Conversation) []byte
```

**Conversations and exports** — V2, built by task mb4 (cgo-free;
the model, its rules and the views in
[visualization.md](visualization.md), "V2 as built"):

```
internal/viewmodel/conversation.go # Conversation, Step, caps, Order*/Hop*
internal/viewmodel/record.go       # Recorder: the http pass, Plan
internal/viewmodel/link.go         # pair by req_seq, by clock; nest
internal/viewmodel/fileops.go      # the file pass, fd → path, Methods
internal/viewmodel/steps.go        # the steps, depth first; ordering
internal/viewmodel/names.go        # lifelines named as servicedb does
internal/viewmodel/sequence.go     # show conversations, show sequence
internal/render/                   # Graph, Sequence: the SVG exports
internal/ctl/conversations.go      # the passes and the frames in build
internal/query/                    # + Filter.Ref, ParseRef (-ref)
```

- **Passes.** The http events ride along with `servicedb.Observe`'s
  read (`recordedSegment`, conn ids renamed by the merge); `Plan`
  keeps the newest 2¹⁸ of them (sorted and cut whenever twice that
  piled up, so memory stays bounded and the kept set is the one a
  single sort would keep), links them and picks the 100 newest
  roots. Only then is the store read once more, for the file work
  of the threads that served a kept request (and the fd → path
  table of their processes), and each methods table holding a
  kept step's frame once — checked in the memory-capped child
  like a segment; a table that fails joins the bad segments and
  its frames stay `method#<id>`.
- **Linking** is per request, never per guess: the req_seq pair
  must be unique on both ends; nesting needs both spans owned by
  the thread (`http.held` from the agent's HTTP flag 16, set by
  the synchronous libraries and never by Netty; a platform thread
  known by name — the correlator now names a virtual thread's
  request `carrier` as it does a syscall's) and looks at the 8
  latest server requests of the thread starting no later; the
  clock join compares at most 64×64 unpaired requests of one
  connection, skips a connection with a startless unpaired end,
  and takes a mutual unique match only; past the 2¹⁸ cut a
  request starting before the oldest kept completion plus the
  widest clock offset joins nothing (`Recorder.edge`). What a rule cannot prove
  stays apart, and says so (`hop`, `ordering`).
- **Exports.** `build` writes `graph.svg` atomically and the
  `sequence/` directory whole under a temporary name, swapped in,
  so a conversation of an earlier build never lingers next to the
  new ones. Text goes through one escape (`esc`: `termsafe.String`,
  then the XML specials).
- Tests: `internal/viewmodel/conversation_test.go` (ordering
  across hosts with a known skew — proven, within the bound's
  width, raw-later-but-earlier, no bound, outside the window;
  clock join proven, too wide, no bound, two candidates; an
  unordered step; duplicate req_seq, carrier, async, outside the
  span; a startless end on a clock-joined connection; file work;
  caps; escapes), `guard_test.go` (a Netty loop's overlapping
  requests, two virtual threads on one carrier and an unknown
  thread nest nothing and own no file work; no join near the
  cut); `internal/correlate/javahttp_thread_test.go` (a virtual
  thread's http event `carrier` under its carrier's name, held
  only with a start, a lost thread record unnamed);
  `internal/render/render_test.go` (two renders byte-identical,
  well-formed XML, no raw control character, a class from the
  data written as `other`, the column cap); `internal/ctl/conversations_test.go`
  — a four-host store (gateway → orders → inventory and billing,
  billing's clock 30 ms ahead, the billing hop HTTP/2): the list,
  the sequence text and JSON, `sequence/0001.svg` and `graph.svg`
  against `testdata/conv_*.golden`, byte-identical rebuilds; the
  same store without the billing connection's data records (no
  bound: the hop unpaired, billing's request its own
  conversation); `query -ref` of the rename step.

**Threads and processes** — the rest of V2, built by task 0f4
(cgo-free; the model and views in
[visualization.md](visualization.md), "V2 as built"):

```
internal/viewmodel/conversation.go # + Lifeline, Step.Lane/ToLane
internal/viewmodel/steps.go        # each step's two ends (end), lanes
internal/viewmodel/stories.go      # ThreadStory, storyBook, storyOrder
internal/viewmodel/process.go      # Process: projected from report
internal/viewmodel/threadview.go   # show thread [SERVICE/THREAD]
internal/viewmodel/processview.go  # show processes
internal/render/sequence.go        # one column per lifeline
internal/report/                   # + Thread.Carried, Call.Carrier
```

- **No new pass.** The emitter that writes a conversation's steps
  knows each step's two threads and their instants on their own
  hosts' clocks; it files both ends in the conversation's
  lifelines and in a story book shared across the conversations.
  The processes come from the rollup `build` already collects
  (`viewmodel.Input.Report`), which now counts a carrier thread's
  virtual-thread events (`carried`) and flags their calls.
- **Lifeline identity** is service, host, pid, tid, thread name and
  the carrier flag: a virtual thread's I/O on a carrier and the
  carrier's own work are two lifelines, and two threads of one name
  stay apart.
- **Story order** is per lifeline, by its own host's clock and then
  emission order; `storyOrder` judges each entry against the one
  before (causal inside one owned span, else time-ordered when the
  clock moved on, else unordered). A step past `MaxSteps` counts in
  its lifelines' `more_entries`, as do entries past 200.
- **Renderer**: `render.Sequence` heads each column with service and
  thread (class `carrier`, dashed, for a carrier's lifeline); a lane
  index outside the list is drawn on the first column, and a model
  without lifelines (built before) one column per service.
- Tests: `internal/viewmodel/lifeline_test.go` (a virtual thread's
  requests on their own lifelines beside the carrier's own, its
  story never causal; story ordering ties and spans; the entry cap;
  the processes projection — incarnations, carried counts, escapes,
  no rollup; thread views terminal-safe, unknown thread);
  `internal/render/render_test.go` (columns per lifeline, the
  carrier lane, an out-of-range lane); `internal/report`
  (`TestReportCarrier`); `internal/ctl/threads_test.go` — the
  four-host store's `show thread`, `show thread
  orders:8080/worker-3` (one owned span: every entry after the
  first causal) and `show processes` against
  `testdata/{thread_list,thread_story,processes}.golden`, an unknown
  thread; the conversation goldens gained the lifelines.

**Query** (`internal/query`, `iormeshctl query`):

```sh
iormeshctl query --project shop --thread 'http-*' \
    --from 12:00:00 --to 12:00:05 --max-bytes 200000
iormeshctl query --project shop --conn conn:4be1… --follow
iormeshctl query --project shop --file '/etc/orders.conf'
iormeshctl query -ref shop/orders/orders.example.org-o1/20
```

Built by task pb4 (cgo-free):

```
internal/query/filter.go     # Filter, Validate, ParseTime/Resolve
internal/query/match.go      # the predicates and their state
internal/query/write.go      # the byte cap, the truncation record
internal/query/query.go      # Run: (follow pass,) filter, encode
internal/ctl/query.go        # flags, store → checked sources, merge
internal/segment/reader.go   # + ReadMethods, CheckMethods
internal/termsafe/           # + JSON: escape a line for a terminal
```

- **Input is the store**, read exactly as build reads it:
  `LoadSegments` with every segment decoded first in the
  memory-capped child and every host directory held to its pin,
  then `federate.Merge` again so the slice speaks the merged
  conn ids (both ends of a fused connection answer to one
  `-conn`) and knows each process's app. Not the build's
  `model.json`: it is a bounded summary without events. Query
  writes nothing — no import, no new pin, no build output. A
  segment that fails is left out, named on stderr, and the
  command exits 1 after writing the slice.
- **Filters** (all AND; globs are `path.Match`, `*` stops at
  `/`): `-from`/`-to` on each event's own `wall_ns`, as its host
  recorded it — not corrected for clock skew between hosts (RFC
  3339, or `HH:MM:SS[.frac]` UTC on the UTC date of the earliest
  segment's start; `-to` exclusive); `-host` the store's host name or the
  session's FQDN; `-app` the app's target pattern (the merged
  graph's app node) or the comm; `-thread` `java.thread` or the
  decimal tid; `-conn` a merged conn id, exactly; `-follow`
  widens it to every event of each thread that used the
  connection, between that thread's first and last event on it;
  `-file` `args.path`/`args.new_path`, plus the fd instance
  (`pid`, `pid_incarnation`, `fd`, `fd_gen`) a successful `open`,
  `openat`, `openat2` or `creat` of a matching path returned,
  until its close. Only that fd instance is followed: not a
  path kind's dirfd (`newfstatat`, `unlinkat`), not a `dup` of
  it, not the copy a forked child inherits; the trace records no
  process exit, so an fd never closed stays tracked to the end
  (an exec keeps it, as the kernel does without `O_CLOEXEC`).
  `-method` a Java
  frame of `java.stack`, as `class#method` or the full frame
  text, through the session's methods tables (the union of its
  segments' tables, each checked in the child like a segment).
  `-ref` one event, by a conversation step's ref
  (`<session>/<seq>`, task mb4). A malformed glob or ref,
  `-follow` without `-conn`, an empty window or a cap below the
  record's size is a usage error (exit 2) before anything is
  read.
- **Output** is schema NDJSON in source order (store name, a
  session's segments in seq order — the `(host, seq)` order),
  each line `termsafe.JSON`-escaped: C1 controls, DEL and bidi
  format runes become `\u` escapes, the line still decodes to
  the same event. `-max-bytes` (default 1 MiB) caps the whole
  output, the record included. A cut result ends in
  `{"schema_version":1,"event":"truncated","args":{"emitted":N,"remaining":M}}`
  ([schema.md](schema.md#query-slices-m5)); what was written is
  always a prefix of the matches, and `remaining` is exact (the
  scan runs on). The order is the store's, not time's: a cut
  favours the projects and hosts that sort first, so narrow the
  filter (`-host`, a window) rather than lean on the cap.
- **Memory**: events stream as in build; the filter state (open
  fds of a matching path, followed threads) and the matching
  method ids are capped at 2²⁰ entries each — over it the query
  fails asking for a narrower filter, so a hostile trace cannot
  grow them without bound.
- **No predicate pushdown** onto the parquet columns yet: every
  row is decoded in the check child anyway before the controller
  may read a file, so skipping pages in the second read would
  not spare the first. Pushdown comes with a cheaper check.

**Summary rollup** (`internal/report`, `show report`; task qb4):

```
internal/report/collect.go     # Collector.Add: processes, threads, methods
internal/report/collect_io.go  # files (fd instances), connections, requests
internal/report/build.go       # Build: merged ids, peers, service labels, caps
internal/report/text.go        # WriteText, termsafe throughout
internal/ctl/report.go         # methods tables, report.json, show report
```

- The collector rides on the merge's read of each segment, as the
  tally does (`reportSegment` wraps both), so the rollup costs no
  pass of its own; `Build` then applies `federate.Result.Renames`
  (connections speak query's ids) and labels server addresses with
  the service database's endpoints.
- Causing methods need the methods tables, which build now reads
  for every loaded session: checked in the child, footer held to
  name and place, at most 2²⁰ rows over the build — a table that
  fails or would pass the cap is a bad segment (left out, named,
  exit 1), and its session's I/O names no method.
- A loopback pair is one host-local conn id held by two ends, a
  cross-host pair one merged id: either way the other end is found
  by id, so `peers` needs no extra merge state. An entry is one
  end — the conn id and the fd instance holding it — so a process
  calling its own server, or writing to its own pipe, gets two
  entries, each the other's peer (review fix: keyed by conn id
  alone, the two merged into one server entry with doubled bytes).
  An http event has no fd: the client's carries the request host,
  the server's none, which picks the end.
- Tests: goldens of the rollup as JSON and text (a gateway → orders
  loopback pair, config read, create-tmp-then-rename, a failed open,
  an untraced callee; a process connected to itself and to its own
  pipe), twice for determinism; the caps (`partial`, each event
  counted once,
  `more files`), terminal safety, no method without a table; in
  `internal/ctl`, `report.json` and `show report` goldens of the M4
  mesh, a damaged methods table, and `show report` without a
  report.

**Eval** (`eval/`, `internal/eval`, `cmd/iormesheval`; built by
task qb4):

```yaml
# eval/questions/microservices.yaml (excerpt)
scenario: microservices
threshold: 0.8          # pinned with the questions: 8 of 10
questions:
  - id: order-file-writer
    question: >-
      Which Java method (class#method) wrote the file order-42.json,
      and through which temporary file?
    answer_contains: ["DataStore#save", "order-42.json.tmp"]
    answer_excludes: ["ServiceConfig#provision"]
  - id: gateway-chain
    question: >-
      List the services the gateway's checkout touches, in the order
      they are first reached, starting with the gateway itself.
    answer_sequence: ["gateway", "orders", "inventory", "billing"]
```

```
internal/eval/questions.go  # Set, LoadSet (strict YAML), Score (matchers)
internal/eval/tools.go      # Ctl (build, show, query), SourceTree, SplitArgs
internal/eval/run.go        # Command model, ParseReply, Toolbox
internal/eval/ask.go        # Session: turns, prompt, Result
internal/eval/cli.go        # flags, opt-in refusal, results file
magefile_eval.go            # mage evalTrace (root), mage eval
```

- The set sits in one file with its scenario and threshold
  (the sketch had a scenario per question). The matchers are
  `answer_contains` (all), `answer_sequence` (in order),
  `answer_excludes` (none of), `answer_regex` and
  `answer_not_regex`, all case-insensitive; a set without a
  matcher on some question, an unknown key or an empty matcher
  string is refused. `TestQuestionSetGrounded` holds every
  contains and sequence string to the scenario's `expect.json`.
  The exclusions name the wrong candidates (other methods, paths,
  services, an order reversed), so listing everything is no way to
  the threshold: `TestListingEverythingFails` scores every
  candidate string of the set and the declarations, sorted and
  reversed, against each question and requires a failure, and
  `TestAnswersPass` requires the concise right answers to pass.
- A run builds the store with `iormeshctl build` into a scratch
  database and builds directory (never the operator's), reads
  `iormeshctl show report`, and asks each question with a fresh
  transcript. Each turn's prompt is the protocol, the rollup, the
  source listing, the transcript and the question; the reply's
  first `QUERY`, `SOURCE` or `ANSWER:` line is the action. A
  query runs `iormeshctl query` with the harness's store,
  database, projects and a 32 KiB cap, plus the model's flags if
  they are slice flags only; `SOURCE` reads through `os.Root`
  below `workload/src`. Every iormeshctl run has a timeout (5
  minutes; a query that hits it is the model's to read) and a
  scratch `HOME` and `XDG_*_HOME`, so no default path reaches the
  operator's own files. Refusals and errors are the model's to
  read; a failing model command fails the question, its stderr
  going to the terminal only — never into the kept results file.
- `eval/results/<date>-<scenario>.json` keeps per question the
  answer, turns, actions and failures, and the score; the model
  appears only by its `-model-name` label (a command line may
  carry credentials). Below the threshold the command exits 1.
- `mage evalTrace` runs `TestMicroservices` once as root with
  `IORMESH_EVAL_EXPORT` (an existing store is replaced only when it
  holds nothing but `<project>/<host>/*.parquet`): a run that passed
  its golden copies its
  segments to `eval/store/<project>/local/` (git-ignored), owned
  by the sudo user. `mage eval` needs `IORMESH_EVAL_MODEL_CMD`
  (and optionally `IORMESH_EVAL_STORE`, `IORMESH_EVAL_MODEL_NAME`)
  and refuses without it.
- Tests (all in `mage test`, no model, no network): the set's
  grounding and the loader's refusals, every matcher with
  negatives, reply parsing, argument splitting and the slice-flag
  allowlist, the source tree's escapes (`..`, absolute,
  symlink), the iormeshctl argv through a stub, a scripted
  session's result golden (a passing answer after a query and a
  source read, a reordered sequence, a model that never answers),
  a failing and a slow model command, and the opt-in refusal.

**Done when**: PLAN.md's M5 acceptance holds.

## M6 — mesh expansion

`internal/expand` (cgo-free, task sb4). The whole feature is a
pure function plus a loop that calls existing M4 code:

```go
// Plan decides which unresolved peers to enroll in one round.
// It never touches the network.
func Plan(in Input, allow Allowlist, lim Limits) Proposal

type Input struct {
    Merge    federate.Result   // Unresolved halves, fused Conns, Graph
    HostDirs map[string]string // host_id -> store dir (registry host)
    Registry federate.Registry // hosts, their Hop, the expand: section
    Projects []string          // which may propose; "" = all
}

type Peer struct {
    Addr  netip.Addr // the address the clients reached, never a name
    Port  uint16     // becomes the tracer's -listen-port
    Conns int        // how many connections reached it
    Via   []Via      // which traced apps, on which hosts, how often
    Other int        // conns set aside per connection (listed once)
}

type Candidate struct {
    Peer
    Hop  int    // 1 + the smallest hop of the hosts that reached it
    Rule string // the allowlist rule that allows it
}

type Skip struct {
    Peer
    Reason   string // see below
    Evidence string // the fact behind it, printed with it
}

type Proposal struct {
    Deploy  []Candidate
    Skipped []Skip
    Limits  Limits // {MaxHops, MaxNodes}
    Nodes   int    // registered hosts before the round
}
```

The rules, in the order Plan applies them to each unresolved
half (`internal/expand/doc.go` has the reasoning):

- `unjoined`: the merge said ambiguous, no_key, no_role or
  key_conflict — the far end may be traced.
- `inbound`: a server half; the peer's port is ephemeral.
- `via_unregistered`: the revealing host is no longer
  registered.
- `local`: loopback, link-local, multicast, no address.
- `enrolled`: a registry host written as that address, or an
  address a traced host owns — the visited set, where A→B→A
  ends. It spans every project in the store.
- `nat`: a fused connection proves the address translates to
  a traced server.
- `ipv6`: the registry takes no IPv6 literal (alias by hand).
- `not_allowed`: no allowlist CIDR or address glob matches.
- `hop_limit`: hop > `-hops` (default 1: the first ring).
- `node_cap`: registered + new hosts would pass `max_nodes`
  (default 16); nearest, then busiest, peers go first.

Only a client half the merge left `no_match` can become a
candidate. Peers are addresses: DNS names are never matched or
proposed (a name the client looked up is a guess about which
machine answered). Halves with the same peer and verdict group
into one entry, and a peer is listed once: an ambiguous
connection to it outranks a candidate, other per-connection
skips fold into the candidate's `other_conns`. Output is
sorted, so a plan is deterministic.

The allowlist's host globs match the address text and may hold
only digits, lowercase hex, `.`, `:` and glob syntax — prefer
CIDRs, since `10.0.0.1*` matches .1, .10–.19 and .100–.199.
CIDRs must be canonical and not IPv4-mapped. Only NAT a fused
connection proves is skipped: a VIP, load balancer or NAT with
an untraced backend, or a SYN proxy before a traced server
(`no_match`), is proposed by its own address, so such ranges —
and container bridges like Docker's 172.17.0.0/16, which name a
different container on every host — belong outside the
allowlist.

Residual for `--apply`: the visited set cannot see a host
registered by name whose traces were never collected, nor the
addresses a multi-homed traced host never used as a local end;
either can be proposed again by an address. tb4 can catch it
after the deploy, when the new host's host_id equals a
registered host's.

`iormeshctl expand [-project P]... [-hops N] [-json]` merges
every project in the store (the same checked segment load as
`build`, against the pinned host identities, but recording no
new pins), lets only `-project`'s connections propose, prints
`deploy` and `skipped` with evidence and stops; it contacts no
server and writes nothing. A segment left out of the merge is
named on stderr and makes it exit 1 after printing: that host's
addresses are missing from the visited set. `expand --apply`
(task tb4) runs M4's `add` + `deploy` for each candidate,
recording `Hop` in the registry entry so the next round can
enforce the limit.

Tests (table-driven, no network; `internal/expand`): allowlist
CIDR and glob matching, v4-mapped addresses; hop counting
across rounds (limit 1 stops at the first ring, the nearest
revealer sets the hop); node cap and its tie-break; an
already registered or traced address is enrolled (A→B→A), also
from another project; NAT'ed VIPs; ambiguous/no_key/no_role/
key_conflict never a target; inbound, loopback, link-local,
multicast, IPv6 and unregistered revealers skipped; a peer
listed once; the plan is identical for any order of the
merge's halves; text output escapes trace-derived names.
`internal/ctl` runs `expand` over a real store: no pins
written, bad segments exit 1, `-project` narrows proposals.

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
