# S2 spike notes — experiments 1–3 (tasks 6b4, 7b4)

Raw notes; the write-up is `docs/spikes/s2-cross-host.md` (task 7b4).
Throwaway code, nothing here is production.

## Rig

`sudo -n spikes/s2/rig.sh OUTDIR` builds

```
s2hostA 10.0.1.1 --veth-- 10.0.1.254 s2nat 10.0.2.254 --veth-- 10.0.2.2 s2hostB
```

The client in s2hostA dials the VIP 10.0.1.254:8080. s2nat DNATs that
to 10.0.2.2:8080 (nft, prerouting). Pass `dnat-snat` also SNATs to
10.0.2.254:40000-40999 with a random port, so no address or port is the
same on both ends. Each pass builds fresh namespaces and deletes them
afterwards (EXIT trap). The host's own interfaces and firewall are never
touched: the veth ends are created directly inside the namespaces.

Per pass:

- `isn.bt` (bpftrace): `sock:inet_sock_set_state` and the
  `inet_csk_accept` fexit in hostA and hostB print `snd_una`, `rcv_nxt`,
  `bytes_acked` and `bytes_received` from `struct tcp_sock`. The
  `__nf_conntrack_confirm` fexit in nat prints the original and reply
  tuples and the originator's `td_end`.
- tcpdump on both ends provides the ground-truth ISNs from the SYN and
  SYN-ACK.
- `workload.py`: the server delays every accept by 50 ms, so the request
  (and usually the client's FIN) is already queued when accept() returns.
  The client runs four phases (30 connections per pass):
  - plain: 12 connections with request sizes 0, 1, 100, 1460, 5000 and
    65536;
  - reuse-tw: 6 connections from source port 45678 with the normal FIN
    close. The client closes first and sits in TIME_WAIT; the next
    connect from the same port takes the tuple over through the kernel's
    TIME_WAIT reuse (`tcp_twsk_unique`). No RST is sent (0 RSTs in the
    captures of runs 1–3, whose "reuse" phase was this one: the first
    version set SO_LINGER 0 only after the FIN exchange, which changes
    nothing);
  - reuse-rst: 6 connections from source port 45679, aborted with
    SO_LINGER 0 right after the request, so close() sends an RST (6 RSTs
    in each capture) and leaves no TIME_WAIT;
  - pingpong (added for experiment 3, task 7b4): 4 connections of 20
    request/response rounds of 32 bytes each;
  - tfo: 6 connections with MSG_FASTOPEN. The first fetches the cookie,
    the other 5 carry data in the SYN.
- `clock.bt` (experiment 3): one bpftrace instance per host namespace,
  each with its own test-only clock offset, prints send-enter and
  recv-exit times keyed by TCP sequence numbers; `clock.py` intersects
  the send-before-receive constraints.
- `join.py` checks that hostA's and hostB's SYN/SYN-ACK sequence numbers
  agree, checks every derivation against tcpdump (the k-th connection on
  a reused tuple against the k-th SYN of that tuple, not any of them),
  then joins on the ISN pair and on conntrack.

Kernel 7.2.15-200.fc44, bpftrace 0.24.2. Runs 1–3 had 24 connections per
pass (no reuse-rst phase) and gave the same results; the only difference
was run1's artefact, explained below. Run 5 is the version of review
round 1; its numbers are quoted below unless noted. Runs 7 and 8 (task
7b4) add the pingpong phase, 34 connections per pass, and reproduce every
experiment 1–2 result on the larger set: ISN join 34/34 unique in both
passes, role-aware rebuild 34/34 from ESTABLISHED on, conntrack by tuple
22 unique / 12 ambiguous, snapshot under DNAT+SNAT 24/34 agree (10 wrong
server tuple), `td_end0 - 1 == client iss` 29/29 without SYN data.

## Experiment 1 — ISN pair

Survives NAT: yes. Under DNAT and under DNAT+SNAT with a random port,
every (SYN seq, SYN-ACK seq) pair in hostA's pcap is also in hostB's
(30/30 per pass), and the 4-tuples differ on every connection (0/30
identical). The client's `(iss, irs)` equals the server's `(irs, iss)` for
30/30 connections in both passes. All matches were unique: no pair was
ambiguous, none was missing.

Formulas, checked against tcpdump at every event, per pass (run 5):

| side | event | `snd_una-1, rcv_nxt-1` | guide's `-bytes_*-1` | role-aware rebuild |
|---|---|---|---|---|
| client | CLOSE→SYN_SENT | 0/30 | 0/30 | 0/30 |
| client | SYN_SENT→ESTABLISHED | 24/24; TFO 1/6 | 0/30 | 30/30 |
| client | FIN_WAIT1, FIN_WAIT2, CLOSE (FIN or RST) | mostly wrong | 0/30 | 30/30 |
| server | LISTEN→SYN_RECV | 0/30 | 0/30 | 0/30 |
| server | SYN_RECV→ESTABLISHED | 24/24; TFO 1/6 | 30/30 | 30/30 |
| server | accept exit (snapshot) | only if nothing received yet | 30/30 | 30/30 |
| server | CLOSE_WAIT, LAST_ACK, CLOSE (FIN or RST) | ~0 | 30/30 | 30/30 |

The key cannot be read at the first transition of either side: at
CLOSE→SYN_SENT `snd_una`/`rcv_nxt` are still 0 (and the local port too),
and the child at LISTEN→SYN_RECV is not initialised yet (all fields 0).
For M4 probe placement: take it at ESTABLISHED or any later record, never
at the opening transition.

The rebuild that holds, all arithmetic mod 2³²:

```
active  (connect): iss = snd_una - bytes_acked
passive (accept):  iss = snd_una - bytes_acked - 1
both:              irs = rcv_nxt - bytes_received - 1
```

- Off-by-one on the active side: the guide's hypothesis subtracts 1 for
  the SYN on both sides, which is wrong for the connecting side. The
  SYN-ACK goes through `tcp_ack` → `tcp_snd_una_update`, so the active
  socket's `bytes_acked` already counts its own SYN (1 right after
  ESTABLISHED, with no data). The passive child is cloned with
  `snd_una = iss + 1` and `bytes_acked = 0`. `bytes_received` never counts
  the peer's SYN on either side.
- FIN: `bytes_received` and `bytes_acked` count the FIN like `rcv_nxt` and
  `snd_una` do (the client's `bytes_acked` went 1 → 2 at FIN_WAIT2 with no
  data sent). The rebuild therefore holds in every state up to CLOSE.
  iormesh can take the key at any record of the connection from
  ESTABLISHED on: the ESTABLISHED transition, the accept snapshot, or the
  CLOSE transition (also after an RST: 6/6 on both sides).
- TFO: data in the SYN breaks the direct `-1` form (1/6 correct, and that
  one was the cookie-fetching connection with no SYN data). The rebuild
  still holds (6/6), because `bytes_acked` / `bytes_received` include the
  SYN payload. Evidence: 5 client SYNs carried payload.
- Port reuse: in both reuse phases the 6 connections that share a client
  4-tuple got 6 distinct ISN pairs, so the pair separates reused tuples
  without a time window. How the ISNs step differs by path (client deltas
  per pass, identical in runs 1–5):
  - reuse-tw (TIME_WAIT takeover): client ISN deltas 65539, 65540, 65639,
    66999, 70539 — not clock growth: `tcp_twsk_unique` sets the new `write_seq
    = tw_snd_nxt + 65535 + 2`, i.e. previous ISN + 1 (SYN) + request size + 1
    (FIN) + 65537 (sizes 0, 1, 100, 1460, 5000 give exactly the deltas seen).
    This path is deterministic from the old connection, not drawn from the
    clock. Server ISNs follow the clock: about 785k apart under DNAT (one ISN
    clock tick is 64 ns, ≈ 50 ms accept delay); under SNAT each reuse lands on
    a new random port, so the server's tuple and its ISN hash differ and the
    deltas are unrelated.
  - reuse-rst (no TIME_WAIT): client ISNs come from the clock, ≈ 1.567M
    apart (≈ 100 ms between connects). Under DNAT the server's tuple
    repeats too and its ISNs step the same way; under SNAT they are
    unrelated.
  Either way both ends see distinct pairs. A collision would need a
  reused tuple whose new ISN pair repeats an old one, which neither path
  produces within the 2³² wrap of the clock (≈ 275 s per wrap at 64 ns,
  so pairs from connections minutes apart can collide in principle;
  untested, and the pair should be combined with a coarse time window).
- Rig artefact (run1): an fexit trampoline does not fire for a call that
  was already in progress when it attached. The server was blocked in
  inet_csk_accept before bpftrace attached, so the first accept was lost
  (23 of 24 accept events). rig.sh now starts the server after attaching.
  This doesn't affect iormesh, which uses the accept syscall exit (a
  tracepoint) rather than an fexit on inet_csk_accept.
- Reset in the accept queue: in the reuse-rst phase, one child per pass
  was reset before accept() and accept() still returned it in TCP_CLOSE
  (`state=7` at the accept exit, `skc_num` already zeroed by
  `inet_put_port`, which is why isn.bt's accept probe filters by netns
  only). The rebuild holds for it too. iormesh's M1 accept exit skips
  such a socket ("never registered"), so that connection's server side
  has no key and its edge would stay unresolved — acceptable, but M4
  could emit the snapshot for it instead.
- Not tested: SYN cookies (the passive ISN is the cookie, which is still
  what goes on the wire, so it should work), connections over 4 GiB (the
  u64 counters are reduced mod 2³², so they should work), IPv6,
  sequence-randomising middleboxes (pf `modulate state`, ASA seq
  randomisation) and SYN proxies (these break the key by design).

## Experiment 2 — conntrack in the NAT namespace

- Insert events (fexit on `__nf_conntrack_confirm`, filtered to the nat
  namespace): 30 inserts for 30 connections. A SYN on a tuple whose entry
  is in TIME_WAIT (reuse-tw) or CLOSE (reuse-rst) reopens it: conntrack
  kills the old entry and confirms a new one. The original tuple equals
  the client's 4-tuple. The reply tuple, reversed, equals the server's
  4-tuple. Matched by original tuple alone: 18 unique, 12 ambiguous (the
  two reuse phases: one original tuple, 6 inserts each). Matched by tuple
  plus order (the k-th connection on a tuple takes the k-th insert): 30/30
  agree with the ISN join. Order is a stand-in for a time-window match,
  which across hosts needs the NAT host's clock reconciled with the
  client's.
- Snapshot (`/proc/net/nf_conntrack` read once at the end, joined from
  that file): 20 entries (19 TIME_WAIT, 1 CLOSE). Only the last
  connection of each reused tuple survives. Under SNAT with a random port
  each reuse had a different reply port, so the snapshot join gives the
  wrong server tuple for 10 of 30 connections (20/30 agree with the ISN
  join; runs 1–3, simulated from the last insert event: 5 of 24 wrong).
  Under plain DNAT the reply tuple repeats as well (30/30 "agree"), but
  then 6 connections share one tuple on both ends and the snapshot cannot
  tell them apart either. A snapshot also misses entries that expired
  before it was read. Only an event stream (ctnetlink events or a BPF
  hook) is complete.
- conntrack also tracks the ISN: right after the SYN, the originator's
  `td_end` is `iss + 1 + SYN payload`. `td_end0 - 1 == client iss` held
  for all 25 connections without SYN data (30 minus the 5 TFO SYNs with
  payload). A tracer on the NAT host could
  therefore attach the ISN to the entry and confirm the ISN join, but it
  adds no information the two ends don't already have.
- Requirement: conntrack needs a tracer (root, BPF) or a conntrack netlink
  socket on the NAT host. Cloud NAT gateways, managed load balancers and
  kube-proxy on nodes iormesh doesn't trace give no access to it.

## Pick for decision #26 (recommendation; PLAN.md is updated by 7b4)

Use the ISN pair as the primary cross-host key: `(iss, irs)` on one side
equals `(irs, iss)` on the other. It needs nothing on the NAT host,
survives DNAT, SNAT and port rewriting, separates reused 4-tuples
(TIME_WAIT takeover and RST reuse alike) without a time window, and can
be read at any state record from ESTABLISHED on. It needs
`snd_una`, `rcv_nxt`, `bytes_acked` and `bytes_received` (4+4+8+8 bytes)
added to the TCP state record. The role (connect or accept) must be known
for the SYN correction, and M1's owner registration already provides it.

Treat conntrack as optional. It is complete only as an event stream on a
host iormesh traces anyway, and the ISN key already covers it. When no
ISN pair matches, the edge stays unresolved, as decision #26 already says.

The guide (docs/implementation.md, S2 experiment 1) has the reconstruction
off by one for the active side. Task 7b4 should fix the guide when it
writes the doc.

## Experiment 3 — clock bounds (task 7b4)

The namespaces share one kernel clock, so rig.sh runs one `clock.bt` per
host and each adds its own test-only offset to every timestamp it prints
(the stand-in for iormesh's `-test-clock-offset`). Injected
θ = clock_B − clock_A: pass dnat offsets A 0 / B 1 234 567 891 ns
(θ = +1.234567891 s), pass dnat-snat A 987 654 321 / B 0 ns
(θ = −0.987654321 s). `clock.injected` records it; `clock.py` sees only
the shifted logs and the value to grade against.

Per message:

```
A->B bytes:  theta < recv_B - send_A     (upper bound)
B->A bytes:  theta > send_B - recv_A     (lower bound)
```

- `send` is the send syscall's enter (sendto/sendmsg/write), `recv` the
  recv syscall's exit (recvfrom/recvmsg/read), as iormesh records them.
- Matching needs no clock and no byte counting across hosts: NAT leaves
  sequence numbers alone, so a byte's stream offset `seq − (iss + 1)` is
  the same number on both ends. Each recv pairs with the send that queued
  its last byte (the latest-starting send any of its bytes came from).
  Connections pair across hosts by the ISN pair, rebuilt from the CLOSE
  record with the role-aware formula. 0 recvs without a matching send in
  runs 7–8.
- Pitfall found: `copied_seq` after `tcp_recvmsg` is one past the data
  when the same call consumes the peer's FIN (`found_fin_ok: ++*seq`).
  Run 6 used it and lost 29–33 recvs to off-by-one offsets. The stream
  offset of a recv is `copied_seq` at entry plus the bytes returned.
- Connection boundaries per struct sock: from SYN_SENT / SYN_RECV to the
  next one, not up to CLOSE. FIN_WAIT2 → TIME_WAIT runs `tcp_done`
  (state CLOSE) while the last response still waits in the receive queue,
  so recvs can follow CLOSE.

Results (34 fused connections per pass; 101–109 inequalities each way):

| run / pass | interval around θ (µs) | width | midpoint error | without pingpong |
|---|---|---|---|---|
| 7 dnat      | [−8.6, +7.9] | 16.5 µs | −0.4 µs | 83.2 µs |
| 7 dnat-snat | [−6.7, +8.1] | 14.8 µs | +0.7 µs | 103.5 µs |
| 8 dnat      | [−5.5, +5.4] | 10.9 µs | −0.0 µs | 82.1 µs |
| 8 dnat-snat | [−6.3, +6.4] | 12.7 µs | +0.0 µs | 86.2 µs |

- θ was inside the interval in all four, both signs of skew; no single
  inequality was violated (0 of ~210 per pass).
- The width is the sum of the smallest one-way gap in each direction
  (send enter to recv exit over veth + one forwarding namespace): 5–9 µs
  each, p50 8–11 µs. A review run 9 on a busier machine gave 10–12 µs
  per direction and 21–23 µs width: ≈10–25 µs, load-dependent.
- What tightens it is a receiver already blocked in recv when the bytes
  arrive. Single-exchange connections give a tight lower bound (the
  client waits for the response: ~40–60 µs) but a 50 ms upper bound (the
  request waits out the server's accept delay): plain and reuse-tw alone
  leave intervals of about 50 ms, tfo alone 20–50 ms; reuse-rst alone
  gives only an upper bound (the server never answers). Together they
  reach 82–104 µs because some connection in each direction caught a
  waiting receiver; the ping-pong rounds bring it to 11–17 µs (runs 7–8;
  ≈10–25 µs with run 9). Width after the first k connections, run 8 dnat: k=1 open (no data), 2: 50.4 ms, 10: 50.0 ms,
  20: 101 µs, 34: 10.9 µs.
- Not tested: real hosts (LAN latency, NIC offloads, interrupt
  coalescing — the gap floor grows to tens or hundreds of µs), clock
  drift over a long run (a constant θ assumed; NTP-disciplined clocks
  drift by ppm, ~14 ms over 4 h at 1 ppm, so a 4 h interval can come out
  empty — the merge needs windowed bounds or an offset-plus-drift fit),
  and suspend (boot vs monotonic clock).
