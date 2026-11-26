# The merge

Part of [M4 — controller](../m4.md), in [Implementation
guide](../../implementation.md).

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
only on an accepted socket (a child reset in the accept queue),
after the half was keyed, or on a client whose `bytes_acked`
counts its own SYN; never on a refused connect, whose is 0). The
last case keys a client whose `SYN_SENT → ESTABLISHED` record a
skipped run lost (task qf4): `snd_una − bytes_acked` and
`rcv_nxt − bytes_received` stay constant from `ESTABLISHED`
through the `CLOSE`, by FIN or by RST (`TestSockSeqAbortClose`),
and such a half counts as established at its `SYN_SENT` record,
so a late `CLOSE` stays inside the window. Records are read in
the order of the host's boot clock, which all its sessions share. A
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
  sessions on one host, over loopback;
- two sessions tracing one process (task ei4): each gives the
  socket its own conn id, since the id hashes the establish time
  its own BPF program stamped. Before the join, halves of one
  host with equal role, key and 4-tuple and `Established` inside
  the window fold into one (`fold.go`): it keeps the smallest id,
  lists the others as `Half.Aliases`, and `Result.Renames` maps
  them where the kept id goes, so the far end fuses instead of
  turning `ambiguous`, and the graph keeps one socket node.

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
  or a range that crosses 4 GiB give no data messages: an
  offset there could name two sends. A receive whose byte no
  traced send holds gives nothing. Beyond that a message must
  be shown to pair bytes of one 4 GiB lap (`clockproof.go`,
  task jf4; the rule is milestones.md M4's, "Clock
  reconciliation from the traffic itself"):
  - *Stream check*, per direction: the sums of every data
    record (folded and `tcp_seq`-less ones included) and every
    `tcp_sock` counter stay below 4 GiB, no recorded send or
    receive ends past the sender's sum (`sumsCover`: such an
    offset proves bytes the sums missed), no `tcp_state`
    record of the sender counts more `bytes_acked` than its sum
    plus its SYN and FIN (`ackedCovered`: the kernel saw bytes
    the sums missed), and no drop of the
    sending half's sessions (keyed by host and project) may
    have hidden a send. Each drop is written after the records
    it stands for were lost, so a drop before the socket opened
    on the sending host (its connect or accept enter, compared
    on the boot clock) does not taint. Always tainting: an
    `unattributed` drop (its folded calls run past its
    instant), `skipped_runs` on the client end (a skipped
    `task_newtask` leaves an untraced child holding a socket
    that may predate connect), and an end with no opening call.
    Drops on the receiving host only lose receives. Not a
    proof: bytes reach the socket with no record and no drop
    through untraced `splice` and io_uring (`sendmsg` and
    `sendmmsg` are traced since task oh4; a `sendmmsg` or
    `recvmmsg` whose sum is `bytes_partial` fails the check),
    through a child forked between the untraced
    `socket()` and `connect()`, an exec that keeps the socket
    (its fd binding is dropped), or an fd passed by
    `SCM_RIGHTS` or `pidfd_getfd`; and only the drops of the
    sessions that reported the half are read, so a session
    that traced a holder but recorded none of its calls is the
    same blind spot. `sumsCover`, `ackedCovered`, the
    receiver's sums and any counter at 4 GiB or more catch
    part of it; with the final snapshot below every end of a
    session whose targets exited has a `bytes_acked` read
    after all its calls, so what can still pass unseen is
    untraced bytes no more than the data still unacknowledged
    at that reading. A final snapshot of a session that ended
    otherwise (duration, stop, disk limit, error; the end
    events are kept per host and project) is no witness for
    `ackedCovered`: the app may have sent after the last kept
    record.
  - *Counter proof*, per message, immune to drops and untraced
    calls: a `tcp_state` record of the sender at or after the
    send's exit (its start is read in the enter hook, after the
    enter's timestamp) with `bytes_acked` < 512 MiB (unsent
    plus unacknowledged data stays under 3 GiB, `sk_sndbuf`
    being an int and the overshoot two size goals of at most
    2²⁹), and one of the receiver at or after the receive's
    exit with `bytes_received` < 4 GiB, put both offsets below
    2³². A stale socket binding after a lost CLOSE would report
    a later connection's low counters under the same conn id,
    but those records rebuild another key: the half is a
    `KeyConflict` and fuses with nothing.
  - *Final snapshot* (task sf4): every end needs a counter
    record after its last call for the counter proof. A socket
    closed during the session has its `CLOSE`; for one still
    open, `finish` (`internal/cli/run.go`) has the tracer walk
    `sock_owner_map` before it closes and run the raw
    tracepoint program `iormesh_sock_final` once per entry
    through `BPF_PROG_TEST_RUN` with the `struct sock` address
    as its argument (`bpf.Tracer.FinalSockets`; never
    attached, so `TestEveryHandWrittenProgramIsAttached` lists
    it as run). The program checks the owner like a state
    change does (the socket file's inode is the registered
    generation; an orphan is believed), skips `TCP_CLOSE`,
    and writes a `tcp_state_event` with `oldstate`
    `TCP_STATE_FINAL` (256) — the tuple and the four fields,
    read once, unlocked — into the
    one-slot `sock_final_map`, which userspace reads after the
    run. The correlator emits it as `tcp_state` with
    `args.sock_snapshot: "final"` on the socket's conn and fd
    instance, only for a socket it knows, with the same tuple
    and fields `carriesSeq` passes. The merge reads its
    counters only (`timing.state`), and only when its four
    fields rebuild the fused connection's key (`keyedCounts`:
    since it keys nothing, a stale owner's record could not
    become a `KeyConflict`, and a torn read fails the check
    too); it keys nothing (`isFinal`) and dates neither SYN
    nor handshake. The walk takes each key once and stops
    after 4 × max entries steps (a key deleted mid-walk
    restarts the kernel's iteration). No snapshot exists for
    an owner entry the LRU map evicted or a socket still in
    `SYN_SENT`/`SYN_RECV`. Walking the
    owners rather than the kernel's tables reaches every
    network namespace, a pod's included: a `bpf_iter` over
    tcp and `INET_DIAG` both list one namespace, and
    `tcp_info` has no `snd_una`/`rcv_nxt`. A walk that fails
    is a stderr warning, not a failed trace.
  Lb4's run 2 (M4-10) had an `attach_gap` before each socket
  and skipped runs after it on both hosts: the stream check
  fails, the counter proof keeps both data messages.
  Still open, besides the stream check's residue: an end whose
  last counter record was lost (a skipped `CLOSE` run, which
  a `skipped_runs` drop records) relies on the stream check
  for the messages after its last surviving record.
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
handshake alone is one-sided; a stream past 4 GiB leaves the
handshake only; drops before, inside and after the connection,
of every reason and on either host, take exactly the
directions they may have touched, and counters after a message
prove it despite them (`clockproof_test.go`, with lb4's run 2
trimmed into `testdata/lb4-run2`); a receive spanning two
sends pairs with the later; three hosts A–B–C order A against
C through B; an offset drifting within the allowance after
its bound never yields a false order (and unpadded it would);
a message's own send and receive stay unordered.
On real traces the rig does the same (task ye4,
`integrationtests/clockskew_test.go`): each emulated host's
tracer stamps `wall_ns` skewed by the test-only
`IORMESH_TEST_CLOCK_SKEW`, and the boot clock the hosts share is
the truth — see [testing.md](../../testing.md).

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
  `writev`, `sendto`, `sendfile64`, `sendmsg`, `sendmmsg`,
  `read`, `readv`, `recvfrom`, `recvmsg` and `recvmmsg` (the
  msghdr calls since task oh4) walks its fd to the socket with
  fileident.c's typed walk (`bpf_get_current_task_btf`, `bpf_rdonly_cast`; no
  helper calls), and on a TCP socket parks the socket and the
  field's value by tid (`seq_park_map`, LRU, entries kept and
  emptied rather than deleted). A kernel without the kfunc
  captures nothing.
- **Exit and the race.** The hooks hold no socket lock, and
  another thread may send on the same socket meanwhile. The
  exit re-reads the field through the parked socket: the field
  only grows as bytes are queued (consumed), so `end − start ==
  ret` proves that every byte in between was the call's own
  and the call moved exactly `[start, end)`. One exception
  (task wi4): the read that drains the last data and consumes
  the peer's FIN behind it — the final read of a "write, then
  close" exchange — moves `copied_seq` by `ret + 1`, since the
  FIN takes a sequence number (`tcp_recvmsg`'s `found_fin_ok`).
  A receive passes with `ret + 1` when the socket's state says
  the FIN arrived and the socket is still open (`CLOSE_WAIT`,
  `LAST_ACK`, `CLOSING`) and `end` is `rcv_nxt`: the extra
  number is then the last one received, so the data bytes are
  still `[start, start + ret)`. The state is read first:
  `tcp_data_queue` stores the FIN's `rcv_nxt` before `tcp_fin`
  sets the state, so a FIN state implies an `rcv_nxt` read
  after it counts the FIN. The state, not `sk_shutdown`,
  because a local `shutdown(SHUT_RD)` sets `RCV_SHUTDOWN` too,
  and `skc_state` is already in the trimmed el9 BTF. Not
  `CLOSE`: a reset ends there without a FIN, and a skipped
  urgent byte could then pass as one; userspace drops
  `tcp_seq` after a `TCP_CLOSE` record anyway. So the final
  read on a socket that shut its own side first (`FIN_WAIT2`,
  which the FIN closes) still gets no `tcp_seq`. Anything
  else — two senders interleaving, a send split around
  another, a `MSG_PEEK`/`MSG_OOB` read (a skipped urgent byte
  also moves `copied_seq` one further, but with no FIN), kTLS,
  a TFO send — gives no `tcp_seq`. Not covered either: a
  `read`/`write` the fold held at its enter on a hot streak
  parks nothing, so if it then turns out slow (> 1 ms, the
  read waiting on the peer after a burst) it is sent alone
  without `tcp_seq`; parking held enters too would put the
  walk back on every folded call. (Its native stack and Java
  mark are kept: the owed enter sends them, rwagg.c, task
  ti4.) A stale park (an exit missed) is still exact when the
  check holds; the residue is
  a field *assignment* inside the window (connect, disconnect,
  `TCP_REPAIR`) whose jump equals `ret`, 1 in 2³², an untraced
  `dup2` of another socket onto the fd mid-call while exactly
  `ret` bytes moved on the parked one, and, for the FIN case,
  a CPU that reorders loads: the state and `rcv_nxt` loads
  are kept in order by the compiler only, with no read
  barrier, which suffices on x86 (the main target) but not
  strictly on arm64.
- **Record.** When the check holds, the exit record itself is
  a `tcp_seq_event` (a `ret_event` plus the socket and the
  sequence number, 48 bytes) in place of the `ret_event`, so a
  call still costs two ring records. The correlator
  (`streamseq.go`) passes it on only for a stream data kind
  that moved bytes on the socket the fd is bound to — the
  record's `sk` must be that socket — and only once the event
  has a conn id; the normalizer drops it (random ISNs).
- **The mmsg calls (task oh4).** `sendmmsg` and `recvmmsg`
  return a message count, so their enter also parks the
  mmsghdr array by tid (`internal/c/mmsg.c`, `mmsg_park_map`)
  and their exit reads `msg_len` of the first `ret` entries —
  one 4-byte probe read each in a bounded loop capped at 1024,
  the kernel's own `UIO_MAXIOV` clamp (bounded loops exist
  since 5.3, `bpf_loop` only since 5.17; el9's 5.14 verifier
  accepts this one, processing 42,078 instructions for the
  exit handler against the 1M limit,
  `TestMmsgVerifierHeadroom` on `iom1`, task rj4; see
  testing.md, "el9 runbook and runs") — and checks the
  sum, not `ret`, against the parked sequence field. The exit record is
  an `mmsg_event` (56 bytes: a `ret_event` plus the sum, the
  socket and sequence number, and flags) for every such call
  that moved messages. A sum it could not complete — a
  faulting read, the cap, no park — is flagged; the correlator
  marks the event `args.bytes_partial`, it carries no
  `tcp_seq`, and the merge's stream check fails its direction
  (`streamProof`). The cases no kernel reaches (the cap, a
  fault) are pinned by a C harness of the real mmsg.c with the
  cap lowered (`internal/generate/mmsg_harness_test.go`, with
  mutations); `internal/msgcalls_root_test.go` runs the four
  calls on TCP (bytes and `tcp_seq` from the handshake) and
  the mmsg sums between two UDP sockets.
- **Tests.** `internal/streamseq_root_test.go` (root; a child
  re-run of the test binary does the traffic, since the tracer
  never traces itself): all seven kinds on one connection, each
  pinned to the bytes before it from the handshake's
  `snd_una`/`rcv_nxt`; two threads sending blocking 64 KiB
  buffers on one socket, where every `tcp_seq` given must
  locate exactly the caller's bytes in the received stream
  (about 110–180 of 200 calls carry one; with the check
  disabled the test fails); a hot loop whose folded records
  carry none; a read that drains the data and the peer's FIN
  together, which carries one (`TestStreamSeqFinRead`, no
  `tcp_seq` before task wi4), and its negative, a read past an
  urgent byte, which carries none (`TestStreamSeqUrgentSkipNoSeq`,
  failing with the FIN-state check removed). `TestTCPFamilies`
  splits a real Java loopback trace into two pretend hosts and
  requires
  `federate.ClockBounds` to bound them from both sides around
  0 (≈ 64–145 µs raw width).
- **Cost.** Unchanged per event on `syscall-burst`; on an
  unfolded TCP loop +3 % tracer instructions per event and
  ~500 kernel instructions per data call in the traced process
  (s3-overhead.md, "Follow-up (xe4)").
