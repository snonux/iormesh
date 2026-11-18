# S2 — cross-host join: result

Companion to [PLAN.md](../../PLAN.md) — the plan is the source of
truth; this is spike S2's written result. Raw notes, per-run
numbers and the throwaway code are in
[spikes/s2/](../../spikes/s2/NOTES.md). Kernel 7.2.15-200.fc44,
bpftrace 0.24.2, November 2026.

**Verdict: works.** The TCP initial-sequence-number pair is the
cross-host join key (decision #26, now decided). It survives DNAT
and DNAT plus random-port SNAT, separates reused 4-tuples, and
needs nothing on the NAT host. Conntrack works too but is weaker
and needs access to the NAT host. Clock bounds from the traffic
recover an injected skew inside an interval of ≈10–25 µs on
veth (runs 7–9, load-dependent).

## The rig

Three network namespaces on one machine, built by
`sudo -n spikes/s2/rig.sh OUTDIR` and deleted by its EXIT trap:

```
s2hostA 10.0.1.1 --veth-- 10.0.1.254 s2nat 10.0.2.254 --veth-- 10.0.2.2 s2hostB
```

The client in `s2hostA` dials the VIP `10.0.1.254:8080`; `s2nat`
DNATs it to the server at `10.0.2.2:8080`. A second pass also
SNATs to `10.0.2.254` with a random port from 40000–40999, so no
address or port of a connection is the same on both ends.

Each pass runs 34 connections in five phases:

| phase | connections | what it exercises |
|---|---|---|
| plain | 12 | request sizes 0 B to 64 KiB, normal close |
| reuse-tw | 6 | one source port, FIN close: TIME_WAIT takeover |
| reuse-rst | 6 | one source port, closed with RST: no TIME_WAIT |
| pingpong | 4 | 20 request/response rounds of 32 B each |
| tfo | 6 | TCP Fast Open; 5 carry data in the SYN |

The server delays every `accept()` by 50 ms, so the request (and
often the client's FIN) is already queued when iormesh would
first learn the passive socket's owner. tcpdump on both ends is
the ground truth. bpftrace stands in for the tracer: `isn.bt`
reads `struct tcp_sock` at every state change and at accept, and
watches conntrack inserts in the NAT namespace; `clock.bt` runs
once per host for experiment 3. `join.py` and `clock.py` do the
analysis.

## Experiment 1 — the ISN pair

NAT rewrites addresses and ports, never sequence numbers. In
every run and both passes, the client's `(iss, irs)` equalled the
server's `(irs, iss)` for every connection (34/34), every match
unique, none missing — while the 4-tuples differed on all of
them.

The pair is not read directly. The passive side reaches
`ESTABLISHED` before `accept()`, and data or FIN may already have
moved the counters, so it is rebuilt from four `tcp_sock` fields
at any record from `ESTABLISHED` on. All arithmetic mod 2³²:

```
active  (connect): iss = snd_una - bytes_acked
passive (accept):  iss = snd_una - bytes_acked - 1
both:              irs = rcv_nxt - bytes_received - 1
```

The hypothesis in the guide subtracted 1 for the SYN on both
sides; that is wrong for the connecting side, where it matched
no connection. The SYN-ACK goes through `tcp_ack` →
`tcp_snd_una_update`, so the active socket's `bytes_acked`
already counts its own SYN. The passive
child is cloned with `snd_una = iss + 1` and `bytes_acked = 0`.
`bytes_received` never counts the peer's SYN.

Checked against tcpdump at every recorded event:

| record | role-aware rebuild | direct `snd_una − 1` |
|---|---|---|
| client `SYN_SENT → ESTABLISHED` | all | fails with TFO data |
| server `SYN_RECV → ESTABLISHED` | all | fails with TFO data |
| server accept exit (data queued) | all | only if nothing received |
| any later state up to `CLOSE` (FIN or RST) | all | mostly wrong |
| client `CLOSE → SYN_SENT`, server `LISTEN → SYN_RECV` | none | none |

- **Where to read it**: at `ESTABLISHED` or any later record,
  never at the opening transition — `snd_una`/`rcv_nxt` are still
  0 there (and the child is not initialised).
- **FIN and RST**: `bytes_acked`/`bytes_received` count the FIN
  like `snd_una`/`rcv_nxt` do, so the rebuild holds through
  `CLOSE`, after an RST as well.
- **TCP Fast Open**: the direct form fails when the SYN carries
  data; the rebuild holds, because the byte counters include the
  SYN payload.
- **Port reuse**: six connections on one client 4-tuple got six
  distinct pairs, both on the TIME_WAIT path (the new ISN is the
  old `snd_nxt + 65537`, set by `tcp_twsk_unique`) and the RST
  path (a fresh clock ISN). No time window is needed to tell them
  apart. A collision needs the same tuple and the same pair after
  the ISN clock wraps (≈ 275 s at 64 ns per tick); the merge
  still bounds candidates by a coarse time window.
- **Role**: the SYN correction needs the socket's role. M1
  records no role field, but the role follows from the socket's
  first record (a connect or an accept), so the merge derives
  it.
- **Cost**: four extra fields the TCP state record must carry
  (added in M4; extends M1's 88-byte `tcp_state_event`) —
  `snd_una`, `rcv_nxt` (4 bytes each), `bytes_acked`,
  `bytes_received` (8 bytes each).

One child per pass was reset while still in the accept queue,
and `accept()` returned it already in `TCP_CLOSE`. The rebuild
holds for it too. M1's accept exit emits the snapshot for such
a socket but does not register it as an owned connection, so
that edge stays unresolved unless M4 takes the key from the
snapshot.

## Experiment 2 — conntrack on the NAT host

- **Events** (a BPF hook on conntrack's confirm, or ctnetlink
  events): one insert per connection; the original tuple is the
  client's 4-tuple, the reversed reply tuple the server's. The
  tuple alone is ambiguous for reused tuples (22 unique, 12
  ambiguous); adding order (the k-th connection on a tuple takes
  the k-th insert) agrees with the ISN join 34/34. Across hosts
  that order is a time-window match, which needs the NAT host's
  clock reconciled too.
- **Snapshot** (`/proc/net/nf_conntrack` read once): only the
  last connection of a reused tuple survives, and entries expire.
  With random-port SNAT it gave the wrong server tuple for 10 of
  34 connections.
- Conntrack knows the ISN as well (`td_end − 1` of the originator
  equals the client's ISN when the SYN carries no data, 29/29), so
  it adds nothing the two ends do not already have.
- It needs a tracer or a conntrack netlink socket on the NAT
  host. Cloud NAT gateways, managed load balancers and kube-proxy
  on nodes iormesh does not trace give no access.

## Experiment 3 — clock bounds

With θ = clock_B − clock_A, every message on a fused connection
is one inequality, since a receive completes after its send
started:

```
A→B bytes:  θ < recv_B − send_A     (upper bound)
B→A bytes:  θ > send_B − recv_A     (lower bound)
```

`send` is the send syscall's enter, `recv` the receive syscall's
exit — the timestamps iormesh records anyway. The interval is the
intersection over all messages.

**Matching the two ends needs no clock.** A byte has the same
sequence number on both hosts, so its stream offset
`seq − (iss + 1)` is the same number on both. Each receive pairs
with the send that queued its last byte. Connections pair by the
ISN pair. Two details matter:

- the receive offset is `copied_seq` at `tcp_recvmsg` entry plus
  the bytes returned. `copied_seq` after the call is one too far
  when the same call consumed the peer's FIN; the first run lost
  a third of its receives to that.
- a connection on a `struct sock` runs from its `SYN_SENT` /
  `SYN_RECV` transition to the next one, not up to `CLOSE`: the
  `FIN_WAIT2 → TIME_WAIT` path reaches state `CLOSE` while the
  last response still sits in the receive queue.

**Injected skew.** Namespaces share one kernel clock, so each
host's tracer instance adds a test-only offset to every
timestamp it emits — the stand-in for iormesh's
`-test-clock-offset`. One pass injects θ = +1.234567891 s, the
other θ = −0.987654321 s.

Results, 34 fused connections and 101–109 inequalities each way
per pass:

| run / pass | interval around θ | width | midpoint error | without ping-pong |
|---|---|---|---|---|
| 7 DNAT      | −8.6 / +7.9 µs | 16.5 µs | −0.4 µs | 83 µs |
| 7 DNAT+SNAT | −6.7 / +8.1 µs | 14.8 µs | +0.7 µs | 104 µs |
| 8 DNAT      | −5.5 / +5.4 µs | 10.9 µs | 0.0 µs | 82 µs |
| 8 DNAT+SNAT | −6.3 / +6.4 µs | 12.7 µs | 0.0 µs | 86 µs |
| 9 (review)  | 10–12 µs each side | 21–23 µs | — | — |

- θ lay inside the interval every time, for both signs of skew,
  and no single inequality was violated.
- **How tight**: the width is the smallest one-way gap
  (send enter to receive exit) in each direction added together:
  5–12 µs each way over veth through one forwarding namespace,
  depending on load.
- **What tightens it** is a receiver already blocked in `recv`
  when the bytes arrive. One request/response per connection
  gives a tight lower bound (the client waits for the answer,
  ~40–60 µs) but a loose upper bound (the request waits out the
  50 ms accept delay): plain and TIME_WAIT reuse connections
  alone leave intervals of about 50 ms, TFO alone 20–50 ms. The
  mix of all single exchanges reached 82–104 µs; the ping-pong
  rounds bring it to ≈10–25 µs (11–17 µs in runs 7–8, 21–23 µs
  in a review run 9 — the per-direction gap floor moves with
  machine load). The bound is only as good as the most interactive
  traffic between the two hosts.

For the merge this means: the bound comes for free with every
fused connection, it is honest (never wrong, only wide), and its
width is worth printing next to it, because it tells the reader
how far cross-host timestamps can be trusted.

## Limits — untested or breaking

Untested in this spike:

- **SYN cookies**: the passive ISN is the cookie, which is still
  what goes on the wire, so the key should hold.
- **Connections over 4 GiB**: the u64 byte counters are reduced
  mod 2³², so the rebuild should hold.
- **IPv6**: same fields, same arithmetic; not run.
- **Real hosts**: LAN latency, NIC offloads and interrupt
  coalescing raise the per-direction gap floor to tens or hundreds
  of µs, and the interval widens with it.
- **Clock drift**: the interval assumes one constant θ for the
  whole run. NTP-disciplined clocks drift by ppm — about 14 ms
  over a 4 h session at 1 ppm — so a single interval over a long
  run can come out empty. The merge needs bounds per time window
  (or an offset-plus-drift fit) and must report an empty
  intersection rather than hide it.
- **Suspend**: boot vs monotonic clock on a host that slept.

Breaking the ISN key by design: **sequence-randomising
firewalls** (pf `modulate state`, Cisco ASA sequence
randomisation), **SYN proxies** (the proxy completes the
handshake itself, so the two ends see different ISNs), and any
**L4 or L7 proxy that terminates the connection** (two
connections, not one — the route map's job, decision #15). In
all of these no pair matches and the edge stays unresolved —
never a guess.

## What changes in the plan

- Decision #26 is decided: the ISN pair is the key; conntrack is
  optional; no key match means an unresolved edge.
- The TCP state record must carry the four `tcp_sock` fields
  (added in M4; extends M1's 88-byte `tcp_state_event`); the role
  is derived from the socket's first record; the key is taken at
  `ESTABLISHED` or later.
- Send and receive records must carry their TCP sequence range
  (`tcp_sendmsg`/`tcp_recvmsg` hooks, M4).
- The merge's clock reconciliation uses the inequalities above,
  matched by sequence offset, and records the bound and its width.
