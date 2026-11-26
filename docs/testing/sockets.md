# Socket edge cases

Part of [Testing strategy](../testing.md).

## msghdr data calls (task oh4, 2026-11-22)

`TestMsgCallsTCP` and `TestMsgCallsUDP`
(`internal/msgcalls_root_test.go`, in `mage bpfRootTest`) run
through the seq child of the tcp_seq tests. On TCP: a `sendmsg`
of two buffers and a `sendmmsg` of two messages, then a
`recvmsg` and a `recvmmsg` of two messages, each pinned to its
bytes and to `tcp_seq` from the handshake's `snd_una`/`rcv_nxt`;
the mmsg calls' `ret` stays their message count. Between two
UDP sockets (unconnected since task nk4): a `sendmmsg` of three
datagrams and the `recvmmsg` taking them carry the datagrams'
byte sum, the
single `sendmsg`/`recvmsg` their size, and no record carries
`tcp_seq`. Red first on the 7.2 laptop: before the change both
failed (`sends: calls moved [] bytes, want [7 11]`, `udp
sendmmsg moved 0 bytes, want 60`). The incomplete sums no
kernel produces — the 1024-message cap, a faulting read, a lost
park — are pinned by `TestMmsgHarness`, the real
`internal/c/mmsg.c` compiled against simulated maps, ring and
user memory with the cap lowered to 4, plus its mutations
(`TestMmsgHarnessCatchesMutations`). `TestMmsgVerifierHeadroom`
loads the four mmsg handlers on the running kernel and logs the
instructions its verifier processed (`verified_insns`): on the
7.2 laptop 821 for each enter and about 42k for each exit (the
loop), below its bound of 100k, a tenth of the 1M limit. That
is 7.2's verifier; on el9 (task rj4) the count is the same,
42,078 — see "el9 runbook and runs" below.


## MSG_PEEK receives (task ok4, 2026-11-24)

`TestPeekCountedOnce` (`internal/peek_root_test.go`, in `mage
bpfRootTest`) runs through the seq child too: the parent sends
one 12-byte chunk, the child peeks at it with `recvfrom`,
`recvmsg` and `recvmmsg` (each `MSG_PEEK`) and then reads it.
Each peek keeps its 12 bytes, is marked `args.peek` and carries
no `tcp_seq`; the read carries the chunk's range; the receives'
`StreamBytes` sum is 12. Red first on the 7.2 laptop: before the
change the peeks were unmarked and the receives counted 48
bytes. `recvmmsg`'s enter moved from `fd_event` to
`fd_size_event` to carry its flags; no CO-RE relocation was
added (`TestEl9RecordLayout` already relocates the handler).
Not yet run on el9.


## The msghdr calls' datagram peer (task nk4, 2026-11-24)

`TestMsgCallsUDP` now runs its UDP sockets unconnected: every
send names the receiving socket in `msg_name` (each `sendmmsg`
message too), every receive asks for the sender there, so the
peer is only in the msghdr. Each of the four events must name
the other socket as `remote_addr:remote_port`. Red first on the
7.2 laptop: before the change all four failed (`udp sendmmsg
names no peer, want 127.0.0.1:46226`, likewise sendmsg, recvmmsg
and recvmsg). `TestMsghdrAddrHooks` (internal/generate) pins
that the four calls park their msghdr and read it as one, and
that bind, connect, sendto and recvfrom do not. The mmsg exits'
verifier count on 7.2 grew from 41,943/41,964 (sendmmsg/recvmmsg)
to 42,554/42,579; `TestEl9RecordLayout` still relocates them
clean against the el9 BTF. Not yet run on el9.


## Final socket snapshot (task sf4, 2026-11-21)

`TestSockFinalSnapshot` (`internal/sockfinal_root_test.go`, in
`mage bpfRootTest`) opens a loopback connection in the test
process, exchanges 5 and 7 bytes and leaves it open. It walks
`sock_owner_map` and test-runs `iormesh_sock_final` for each
entry, as `bpf.Tracer.FinalSockets` does. Each end must give one
`TCP_STATE_FINAL` record with its owner, its tuple and the
counters the exchange implies: client 6 acked (its SYN too) and
7 received, server 7 and 5. Both ends' fields must rebuild one
ISN pair. Once both sockets closed, neither gives a record, nor
does an address nobody registered. It passes on the 7.2 laptop
and, with the sock root tests, on `iom1` (5.14.0-741.22.1.el9_9;
static test binary as in nf4, staged under `/root/sf4` and
removed). Mutations caught: no counter read (all zero) and the
owner's inode check inverted (no record).

The sf4 review added the two guards' own cases to the test: an
owner entry planted for the live client socket with its inode plus
one, and one for a socket a refused connect left in `TCP_CLOSE`
with its fd still open, must give no record. Deleting the inode
check, or the `TCP_CLOSE` skip, now fails the test on 7.2. The
final snapshot now reads the four fields once instead of
re-reading them until two reads agree. The merge uses its counters
only when they rebuild the connection's key, which drops a torn
read as well as another connection's record (`keyedCounts`).
`ackedCovered` takes a final snapshot only from a session whose
targets exited (`TestClockAckedPastSum`'s duration, stop and
no-end cases). The walk over `sock_owner_map` takes each key once
and is capped (`TestOwnerKeysDedupesAndStops`).

End to end, `iormesh run -pid` traced a Python process that opens
a loopback connection after the attach, exchanges 5 and 7 bytes
and keeps it open. On both kernels the trace ends with two
`tcp_state` records marked `"sock_snapshot":"final"`, on the
connection's conn id and fd instances: `bytes_acked` 6 and
`bytes_received` 7 on the client, 7 and 5 on the server.

The unit tests: `internal/federate` (`TestClockFinalSnapshot`,
`TestFinalSnapshotDatesNothing`, `TestClockAckedPastSum`),
`internal/correlate` (`TestFinalSnapshotEvent`), `internal/cli`
(`TestFinishWritesFinalSnapshots`), `internal/bpf`
(`TestFinalRecords*`, `TestDecodeFinalRejects`), `internal/schema`
(`TestSockSnapshotFinal`) and `internal/normalize`
(`TestNormalizeDropsFinalSnapshots`). Each guard was mutated out
once and a test failed every time.


## Connections open before the session (task cm4, 2026-11-26)

`TestSeedPreopenedConnection` (`internal/preopen_root_test.go`,
in `mage bpfRootTest`) starts two children of the test binary
(`internal/preopen_child_test.go`). Each listens on loopback,
connects to itself and accepts, all before the tracer loads, and
keeps the listener open. The tracer traces the first child only,
with the state tracepoint and the data calls, and runs
`bpf.Tracer.SeedSockets` twice. Then both children move 5 bytes
one way and 7 back. Run through the correlator, the target's two
ends must give exactly one initial `tcp_state` event each:
`initial_client` on the connecting fd, `initial_server` on the
accepted one (its port the listener's), `ESTABLISHED`, the
loopback tuple and the four fields. Both ends and their reads and
writes must carry one conn id. Split onto two hosts, the ends
must give a client and a server half with one key, which the
merge fuses. The bystander child gets no event. It failed first
(red) with `fd 6: 0 initial tcp_state events, want 1`. Mutations
caught: the listener lookup dropped (both ends read as clients;
the keys then differ by one and both halves stay `no_match`), the
target check dropped (bystander events), and the
already-registered check dropped (two snapshots per end after
the second seeding). `TestEl9RecordLayout` relocates the two
iterators against the trimmed el9 BTF too; their context struct
is sock.c's own, so they need no type the file lacks. Passed on
the 7.3 laptop; the pod-mode loads (`TestPidNS*`) load the
iterators too.

The unit tests: `internal/correlate`
(`TestInitialSnapshotFusion`), `internal/federate`
(`TestHalvesFromEvents`' initial cases, `TestFuse`'s open-before
cases, `TestFuseFoldsSessions`, and `TestFuseMatchesReference`,
whose reference now knows `Before`), `internal/schema`
(`TestSockSnapshotInitial`), `internal/servicedb`
(`TestInitialSnapshotLocalPair`), `internal/report`
(`TestReportInitialSnapshotRoles`), `internal/cli`
(`TestSessionSeedsOpenSockets`, `TestPortSessionFollowsRestart`)
and `internal/bpf` (`TestDrainIter`).

**On el9 (2026-11-26): 44 of 44 pass.** On `iom1`
(5.14.0-741.22.1.el9_9.x86_64), at dc76ca6, the static
`internal.test` built on the 7.3 laptop ran every test of
`gatecmd.RootBPFTests` as root (staged under `/opt/cm4` with
`internal/testdata/btf`, removed afterwards), none skipped.
`TestSeedPreopenedConnection` passes, so el9's 5.14 loads and runs
both `iter/task_file` programs; `TestEl9RecordLayout` passes
against the host's own BTF. iom1 was started for the run and
stopped again.

The cm4 review found that a host with a million open files in
view would not have been seeded at all. The kernel's
`bpf_seq_read` returns `EAGAIN` once one read() visits
`MAX_ITER_OBJECTS` (1M) objects without output, and the seed
programs write none; `drainIter` ended there. It now reads on,
and the walk resumes. `TestDrainIter`'s EAGAIN case failed first
(`resource temporarily unavailable after 1 reads`). The listener
map is emptied after the walk too.
