# S2 spike notes — experiments 1–2 (task 6b4)

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
  The client runs three phases:
  - plain: 12 connections with request sizes 0, 1, 100, 1460, 5000 and
    65536;
  - reuse: 6 connections from source port 45678, closed with RST, so the
    4-tuple repeats within milliseconds;
  - tfo: 6 connections with MSG_FASTOPEN. The first fetches the cookie,
    the other 5 carry data in the SYN.
- `join.py` checks every derivation against tcpdump, then joins on the ISN
  pair and on conntrack.

Kernel 7.2.15-200.fc44, bpftrace 0.24.2. Three full runs (run1 to run3,
all passes each) gave the same results. The only difference was run1's
artefact, explained below.

## Experiment 1 — ISN pair

Survives NAT: yes. Under DNAT and under DNAT+SNAT with a random port, the
sequence numbers in hostA's pcap equal those in hostB's pcap, and the
4-tuples differ on every connection (0/24 identical). The client's
`(iss, irs)` equals the server's `(irs, iss)` for 24/24 connections in
both passes. All matches were unique: no pair was ambiguous, none was
missing.

Formulas, checked against tcpdump at every event (all n/n unless noted):

| side | event | `snd_una-1, rcv_nxt-1` | guide's `-bytes_*-1` | role-aware rebuild |
|---|---|---|---|---|
| client | SYN_SENT→ESTABLISHED | 18/18; TFO 1/6 | 0/24 | 24/24 |
| client | FIN_WAIT1, FIN_WAIT2, CLOSE | mostly wrong | 0/24 | 24/24 |
| server | SYN_RECV→ESTABLISHED | 18/18; TFO 1/6 | 24/24 | 24/24 |
| server | accept exit (snapshot) | 0–1/12 plain | 24/24 | 24/24 |
| server | CLOSE_WAIT, LAST_ACK, CLOSE | 0 | 24/24 | 24/24 |

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
  iormesh can take the key at any record of the connection: ESTABLISHED,
  the accept snapshot, or the CLOSE transition.
- TFO: data in the SYN breaks the direct `-1` form (1/6 correct, and that
  one was the cookie-fetching connection with no SYN data). The rebuild
  still holds (6/6), because `bytes_acked` / `bytes_received` include the
  SYN payload. Evidence: 5 client SYNs carried payload.
- Port reuse: the 6 connections that share a 4-tuple got 6 distinct ISN
  pairs. Linux ISNs for one 4-tuple grow with the clock (about 65k apart
  per connection here), so the pair separates reused tuples without a time
  window.
- Rig artefact (run1): an fexit trampoline does not fire for a call that
  was already in progress when it attached. The server was blocked in
  inet_csk_accept before bpftrace attached, so the first accept was lost
  (23 of 24 accept events). rig.sh now starts the server after attaching.
  This doesn't affect iormesh, which uses the accept syscall exit (a
  tracepoint) rather than an fexit on inet_csk_accept.
- Not tested: SYN cookies (the passive ISN is the cookie, which is still
  what goes on the wire, so it should work), connections over 4 GiB (the
  u64 counters are reduced mod 2³², so they should work), IPv6,
  sequence-randomising middleboxes (pf `modulate state`, ASA seq
  randomisation) and SYN proxies (these break the key by design).

## Experiment 2 — conntrack in the NAT namespace

- Insert events (fexit on `__nf_conntrack_confirm`, filtered to the nat
  namespace): 24 inserts for 24 connections. Each reused tuple got a new
  entry after the RST close. The original tuple equals the client's
  4-tuple. The reply tuple, reversed, equals the server's 4-tuple. Matched
  by original tuple: 18 unique, 6 ambiguous (the reuse phase: one original
  tuple, 6 inserts). Telling those apart needs insert timestamps compared
  with the client's connect time, and therefore clock reconciliation
  between the NAT host and the client host.
- Snapshot (`/proc/net/nf_conntrack` read once at the end): 19 entries,
  all in TIME_WAIT. Only the last of the 6 reused connections survives.
  Under SNAT with a random port each reuse had a different reply port, so
  a snapshot join attributes 5 of 24 connections to the wrong server
  tuple (19/24 agree with the ISN join). Under plain DNAT the reply tuple
  repeats as well (24/24 "agree"), but 6 connections still share one
  tuple on both ends. A snapshot also misses entries that expired before
  it was read. Only an event stream (ctnetlink events or a BPF hook) is
  complete.
- conntrack also tracks the ISN: right after the SYN, the originator's
  `td_end` is `iss + 1 + SYN payload`. `td_end0 - 1 == client iss` held
  for all 19 connections without SYN data. A tracer on the NAT host could
  therefore attach the ISN to the entry and confirm the ISN join, but it
  adds no information the two ends don't already have.
- Requirement: conntrack needs a tracer (root, BPF) or a conntrack netlink
  socket on the NAT host. Cloud NAT gateways, managed load balancers and
  kube-proxy on nodes iormesh doesn't trace give no access to it.

## Pick for decision #26 (recommendation; PLAN.md is updated by 7b4)

Use the ISN pair as the primary cross-host key: `(iss, irs)` on one side
equals `(irs, iss)` on the other. It needs nothing on the NAT host,
survives DNAT, SNAT and port rewriting, separates reused 4-tuples
without a time window, and can be read at any state record. It needs
`snd_una`, `rcv_nxt`, `bytes_acked` and `bytes_received` (4+4+8+8 bytes)
added to the TCP state record. The role (connect or accept) must be known
for the SYN correction, and M1's owner registration already provides it.

Treat conntrack as optional. It is complete only as an event stream on a
host iormesh traces anyway, and the ISN key already covers it. When no
ISN pair matches, the edge stays unresolved, as decision #26 already says.

The guide (docs/implementation.md, S2 experiment 1) has the reconstruction
off by one for the active side. Task 7b4 should fix the guide when it
writes the doc.
