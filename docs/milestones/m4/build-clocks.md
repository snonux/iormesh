# M4 build and clock reconciliation

Part of [M4 — Controller (`iormeshctl`): push, collect, merge
locally](../m4.md), in [Milestones](../../milestones.md).

- **Build**: merge the collected segments offline by stable
  ids — connection ids, the ISN-pair join key (decision #26),
  clock reconciliation — fusing the
  outbound side on host A with the inbound side on host B into
  single cross-host edges. An outbound edge whose far end has
  no tracer stays in the graph, marked *unresolved* (M6 feeds
  on these). Build is project-aware too: when project A's app
  calls project B's app over TCP and both are built together,
  the same join fuses both sides into one cross-project edge,
  giving the complete picture across both. All views (M5) run
  locally on the controller, from the build's output. From M4
  on, `build` ends by printing a plain-text summary of the
  merged mesh (services, edges, health, unresolved peers);
  the site and the TUI follow in M5.
  The merge is built (task fb4, `internal/federate`): it reads
  each host session through a format-neutral source (NDJSON
  trace directories, and parquet segments since task ib4), fuses only a
  mutual single ISN-pair match inside a wall-clock window
  (default 2 minutes — boot clocks of two hosts share no epoch),
  and keeps every other TCP end unresolved with its reason
  (`no_role`, `no_key`, `key_conflict`, `no_match`,
  `ambiguous`). Two sessions of one host tracing one process
  give its socket two conn ids (each hashes its own BPF
  establish stamp); the merge folds such halves — same host,
  role, ISN pair and 4-tuple, inside the window — into one
  before the join, so they fuse rather than turn `ambiguous`
  (task ei4). The `tcp_state` schema carries the four
  `tcp_sock` fields, and the tracer captures them (task ve4,
  `tcp_state_event` 88 → 112 bytes, one CO-RE read per field
  and record — negligible), so real traces now fuse: the
  integration test rebuilds one key from both ends of a real
  connection.
  `build` and `refresh` are built (task ib4, `internal/ctl/
  build.go`, `internal/federate/segments.go`): `iormeshctl
  build [-project P]...` (default: every project in the store)
  reads each events segment as its own merge source —
  `internal/segment`'s reader, strict like the NDJSON one — so
  the service database's per-source counts stay exact; the
  server keeps no graph (#3), so each session's graph is
  rebuilt from its events by the correlator's rules. What the
  graph knew that no event said is in the trace since task
  ze4, as appended optional kinds and fields (schema v1 stays
  append-only): a `fork` event per new traced process (on the
  child, with `args.parent_pid` and `args.parent_incarnation`
  — the forked edge, and the point where the child inherits
  its parent's fd table, which the rebuild copies from its own
  view of the parent), an `attach` event per process the
  session took on (a `-pid` or port target, with
  `args.pattern` for a pattern's incarnation — the app node
  and its incarnation edge), and `fcntl(F_SETFD)`'s argument in
  `args.flags`; once per process, so decision #25's budget
  does not notice them. A graph rebuilt from a single-host
  run's segments equals that run's graph.json — the
  integration suite checks it on every run (M4 acceptance:
  the merged mesh equals the single-host result). The build merges,
  calls `Observe` and `Import`, builds the view model (V1:
  run, health, services, edges — docs/visualization.md) and
  writes `~/.local/share/iormesh/builds/<project>[+<project>
  …]/graph.json` (the merged graph export) and `model.json`,
  then prints the summary; `iormeshctl show` prints it again,
  or the services, edges or health view, from `model.json`
  alone. A rerun over the same segments writes the same bytes.
  A segment that cannot be read, whose footer disagrees with
  its name or directory, or whose events do not validate is
  skipped whole (its session's graph rebuilt without it),
  named in the summary and the model, and the command exits 1
  after writing the outputs: one damaged file does not hide
  the rest of the mesh, and the loss is never silent.
  A server may be compromised, so a collected segment is
  hostile input (ib4's review): the reader bounds the file
  (128 MiB) and its footer (8 MiB) before parsing, turns
  decoder panics into errors, and each segment is decoded
  first in a child process with a capped address space, so a
  file built to exhaust memory fails itself, not the build.
  Each host identity (host id, FQDN) belongs to one store
  directory — the one thing a server cannot choose: the first
  build pins it (`hosts.json`). A session whose directory is
  pinned to another host, or whose host id or FQDN is pinned
  to another directory, is bad — only that claimant: the
  pinned host's own sessions stay good. Among unpinned hosts,
  a directory holding two identities, or an identity under
  two directories, makes all sessions involved bad (which one
  is forged cannot be told). Event clocks past int64 (an
  overflowing wall_ns + latency_ns) make their segment bad, not
  the build's import fail. Text from the traces — in views and
  in every error — is printed terminal-safe.
  `refresh` is collect then build; a host whose collect fails
  is reported (exit 1) and what the store holds is built
  anyway.
  **Build window and prune** (task yh4, owner-approved
  2026-11-22): `build -from T -to T` or `-last DUR` builds only
  the segments whose time range overlaps the window, T as
  query's (RFC 3339, or a UTC time of day on a store within one
  UTC day: **A time of day's date** below), and echoes the
  resolved window. A segment's range comes
  from its name — its start, a minute before, to its one-minute
  interval and a minute after — so a window takes whole
  segments and never decodes one outside it once its host
  directory is pinned; an unpinned directory is checked whole,
  so the host vetting, and what the build pins, are a full
  build's. The range bounds when a segment was written, not its
  events' wall clocks: a call blocked past the slack is written
  later than it was stamped, and a `-to` can miss it. The
  service database holds every import ever made, so a windowed
  build reads its services and edges back narrowed to the
  connections (and per-segment request rows) whose seen range
  overlaps the window (`servicedb.Filter` From/To, wall ns);
  the import itself is never narrowed. A windowed build is a
  different build: it writes
  `<projects>[@NS]~window/` (one per project set and namespace,
  overwritten by the next windowed build, read by `show`,
  `open` and `tui -windowed`, task hk4) and never replaces the
  full build; its model records the window (`run.window`), so
  its site and TUI say which window they show. `prune
  -older-than DUR [-apply]` is the store's only delete: it
  takes what `-last DUR` would never select, regular files
  directly in a valid `<project>/<host>/` whose names parse as
  segments, a table file only once its session keeps no newer
  segment; without `-apply` it lists, with it it deletes and
  prints each file. Auto-cleaning the store stays out of scope:
  prune is the operator's explicit command.
  **A time of day's date** (task 1l4): `-from`/`-to` take RFC
  3339, a date and time of day in UTC
  (`YYYY-MM-DDTHH:MM:SS[.frac]`), or a bare UTC time of day.
  One rule places a bare time of day for `build`, `query` and
  `query -window-segments` alike: on the UTC date the listed
  events segments of the projects start on (their names,
  readable or not), when they all start on one date; a store
  whose segments start on more than one UTC date refuses it as
  a usage error that names the dates and asks for the date.
  Why refuse, not anchor: the earliest date moves when `prune`
  deletes the oldest day, and the latest moves when a new day
  is collected, so any anchor lets the same command silently
  mean another day; refusing on a multi-day store means a bare
  time of day only ever resolves where one date is possible.
  The listed segments, not the readable ones, decide, because
  build must place the window before it decodes anything; an
  unreadable segment's name therefore counts too. An instant
  needs no date, so it never lists the store. `build` echoes the
  resolved window on its summary and `query` on stderr (stdout
  is the slice).
  **Prune forgets** (task gk4): `prune -apply` also makes the
  service database forget the files it deletes, so a full build
  afterwards shows no service, edge or request count that only
  deleted segments held. The database records which sources
  (segments) named each connection (`conn_source`, schema
  version 4, seeded from the request rows' sources);
  `servicedb.DB.Forget` drops a source's request rows and every
  connection no other source names, and narrows the rest's
  window to their remaining sources. Naming rules, verdicts,
  hostnames and TLS hints stay. Order: the database forgets
  first, in one transaction, then the files go. A failed
  database update deletes nothing and exits non-zero; a failed
  delete after it leaves a file the database forgot, which the
  next build that reads it imports again — so the database
  never counts a file that is gone without prune saying so. No
  database yet (no build) is nothing to forget, and prune does
  not create one. Rows of a database from before version 4
  without requests carry no source and are kept. A build
  running alongside prune can import a segment again after it
  was forgotten; prune is meant to run between builds.
- **Clock reconciliation from the traffic itself**: every
  fused connection gives send-before-receive constraints in
  both directions (send = send-syscall enter, receive =
  receive-syscall exit, the two ends of a message matched by
  TCP sequence offset, which NAT leaves alone); together they
  bound the clock offset per host pair. The merge records the
  bound and its width — S2 measured ≈10–25 µs on veth with
  interactive traffic (load-dependent), 82–104 µs with single
  exchanges only — computed per time window so clock drift
  over a long session shows up instead of emptying the
  interval; an empty intersection is reported, never hidden.
  This needs every send and receive record to carry its TCP
  sequence range, not just its byte count — captured in the
  data calls' own syscall hooks (task xe4), not in new
  `tcp_sendmsg`/`tcp_recvmsg` kprobes. Cross-host order
  is derived from happens-before (per-thread order plus
  send→receive edges), never from raw wall-clock comparison.
  Built (task gb4, `internal/federate`): send and receive
  records carry `args.tcp_seq` (the sequence number of the
  call's first byte; the tracer fills it since task xe4: the
  socket's `write_seq`/`copied_seq` read at the call's enter
  and again at its exit, given only when the two differ by
  exactly the call's return, or by one more for a receive that
  also consumes the peer's FIN, task wi4 — so concurrent
  senders or readers on one socket, MSG_PEEK, kTLS and the like
  give none, never a guess; one ring record per call as
  before, the exit record carrying it; decision #25's burst
  figure unchanged, +3 % tracer instructions per TCP data
  event — s3-overhead.md,
  "Follow-up (xe4)"),
  the SYN adds one client→server edge (connect enter before
  the server's accept exit). Offsets repeat every 4 GiB, so a
  data message counts only when it is shown to pair bytes of
  one lap (task jf4, `internal/federate/clockproof.go`), by a
  stream check or a counter proof. The stream check covers a
  direction: the byte sums of all its data records (folded
  ones too) and the `tcp_sock` counters stay below 4 GiB, no
  recorded offset ends past the sender's sum, and no drop of
  the *sending* host may have hidden a send — a drop's records
  were lost at or before its own instant, so one before the
  socket opened there (connect or accept enter) hid none;
  unattributed drops, skipped runs on the connecting end (a
  skipped `task_newtask` leaves a child untraced holding a
  socket that may predate connect) and ends without an opening
  call always taint. Receiver-side drops only remove receives.
  The msghdr data calls count since task oh4: `sendmsg` and
  `recvmsg` carry their return as bytes and `tcp_seq` by the
  same exact-move rule; `sendmmsg` and `recvmmsg` return a
  message count, so their exit sums the messages' `msg_len`
  from the caller's mmsghdr array (a bounded loop capped at
  the kernel's own 1024-message clamp) and checks `tcp_seq`
  against that sum. el9's 5.14 verifier accepts the loop: on
  `iom1` (5.14.0-741.22.1.el9_9, task rj4, 2026-11-23) it
  processes 42,078 instructions for the exit handler
  (`TestMmsgVerifierHeadroom`) against the 1M limit, and the
  whole root BPF gate passes there — docs/testing.md, "el9
  runbook and runs". A sum the
  tracer could not complete (a faulting read, the cap, a lost
  park) is marked `args.bytes_partial`, carries no `tcp_seq`
  and fails its direction's stream check — never a short sum
  passed off as the whole.
  A `recvfrom`, `recvmsg` or `recvmmsg` with `MSG_PEEK`
  copies bytes but consumes none, so it must not count twice
  with the receive that later consumes them (task ok4): the
  tracer captures the call's flags argument at its enter (the
  three share `fd_size_event`; `recvmmsg` moved there from
  `fd_event`, giving up the unused file identity word), the
  correlator marks the event `args.peek` (always true when
  set; never with `tcp_seq`) and keeps its `bytes`, and the
  receiver's stream sums — the merge's and the report's —
  skip it. The report's per-thread and per-method bytes skip it
  too (task cl4): they are bytes moved, not bytes copied, so a
  peek-then-read counts once everywhere in the rollup.
  The stream check is not a proof: it trusts the sums to hold
  every byte, and bytes escape them with no drop recorded —
  untraced `splice`, io_uring; a fork
  between the untraced `socket()` and `connect()`; an exec
  keeping the socket (its fd binding is dropped); an fd passed
  by `SCM_RIGHTS` or `pidfd_getfd`; and it reads only the drops
  of sessions that reported the half. What limits the damage:
  an offset past the sender's sum proves untraced bytes and
  fails the check, and so does a sender's `bytes_acked` above
  its sum plus SYN and FIN (task sf4) — the kernel's own count
  of the stream; the receiver's sums are a second witness, and
  any counter record at 4 GiB or more fails it.
  The counter proof covers one message and ignores drops and
  untraced calls: a `tcp_state` record of the sender after the
  send's exit with `bytes_acked` below 512 MiB (the kernel
  keeps less than 3 GiB queued: `sk_sndbuf` is an int) and one
  of the receiver after the receive's exit with
  `bytes_received` below 4 GiB put both offsets below 2³²,
  where equal mod 2³² means equal. A stale socket binding after
  a lost CLOSE cannot feed it another connection's low
  counters: those records rebuild another key, and the
  conflicting half fuses with nothing. Every end gets a
  counter record after its last call (task sf4): a socket
  closed in the session has its CLOSE, and for every socket
  still registered when the session ends the tracer takes a
  final snapshot — `tcp_state` with `args.sock_snapshot:
  "final"` (an appended optional arg, schema v1), the socket's
  state, tuple and the four `tcp_sock` fields. So the counter
  proof covers every message of a direction whose sender
  acknowledged under 512 MiB and whose receiver received under
  4 GiB, whatever the trace missed, and the stream check
  decides only longer directions (or an end whose CLOSE run
  the kernel skipped, recorded as `skipped_runs`); there,
  when the session ended because its targets exited, a
  `bytes_acked` read after every call leaves unseen only
  untraced bytes no more than the data still unacknowledged
  at that reading. A session stopped while its app still ran
  (duration, signal, disk limit, error) gives its final
  snapshots no say in that sum check — the app may have sent
  after the last record the trace kept — only in the counter
  proof. The snapshot is taken in
  the tracer's `finish`, after the last records were handled
  and before the tracer closes: userspace walks
  `sock_owner_map` (each key once, the walk capped) and runs a
  raw tracepoint program per entry
  through `BPF_PROG_TEST_RUN` (Linux 5.10; el9 5.14 has it),
  which checks the owner's inode generation as a state change
  does, skips `TCP_CLOSE`, and reads the socket's four fields
  once, unlocked. Walking the owners, not the kernel's socket
  tables, reaches every network namespace — a `bpf_iter` over
  tcp and `INET_DIAG` each list one, which misses a pod's
  sockets, and `tcp_info` lacks `snd_una`/`rcv_nxt`, which the
  schema's all-four rule needs. The merge reads only its
  counters, and only when its four fields rebuild the fused
  connection's key: a final snapshot never keys a half, so a
  stale owner's record could not become a key conflict as a
  state change does, and the key check drops it — and a torn
  read with it. It dates neither SYN nor handshake. No final
  snapshot exists for a socket whose owner entry the LRU map
  evicted, nor for one still in `SYN_SENT` or `SYN_RECV` at
  the end (a TFO child accepted mid-handshake, say); those
  ends rely on their CLOSE or the stream check. A snapshot
  that cannot be taken is a warning, not a failed trace. The
  merge keeps one bound per
  host pair per 1-minute window (`Result.Clocks`), one-sided
  when only one direction has edges, empty and flagged when
  they contradict. The offset drifts within a window, so each
  bound is widened by an assumed drift rate (default 1 ppm,
  NTP-disciplined hosts: 60 µs per side per minute); a clock
  stepped or slewed harder breaks the assumption. The `build`
  summary and the view model print them per host pair
  (windows, widths, one-sided and empty ones; task ib4); M5's
  views show them too (the health view's clock bounds). The
  bound never feeds back into fusion — it is proven by the
  fused connections, so it cannot choose them; the fusion
  window stays a wrap guard, and hosts skewed beyond it get
  neither edge nor bound.
  Cross-host order (`Timeline`) is decided only when the
  widened bound — summed along a chain of hosts when two never
  talked directly — leaves no doubt; otherwise, including a
  message's own send and receive, it stays unordered.
  Contradictions around a cycle of three or more hosts are not
  detected yet.
