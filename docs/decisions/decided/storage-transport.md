# Decided — storage, transport and the join key

Part of [Decided](../decided.md), in [Decisions](../../decisions.md).

- **#3 Graph storage** — none on the server. The tracer writes
  parquet segments; the controller rebuilds the merged graph
  from them offline, on every build. There is no long-lived
  collector whose state would need to survive a restart. (The
  controller's service database is separate — #24.)
- **#4 Go eBPF library** — ior's stack (`aquasecurity/libbpfgo`,
  cgo, Mage, Docker generation host), adopted with its
  generation pattern. Mechanics: copy/adapt ior's
  `internal/generate`, recording the ior commit each copied
  file came from, so later ior fixes can be ported
  deliberately. Extracting a shared package into ior is the
  alternative to revisit once the copies diverge.
- **#12 Controller transport** — SSH with the operator's key;
  collect syncs over rsync, which must be present on servers
  (`deploy` fails fast if it is missing); server registry in
  `~/.config/iormesh/servers.yaml`. The controller never
  trusts a host key on first use: ssh runs with
  `StrictHostKeyChecking=yes` and `BatchMode=yes`, so a server's
  key must already be in the operator's known_hosts, and no
  password prompt can hang a run; keepalives, rsync's I/O
  timeout and a per-host deadline keep a stalled host from
  hanging one either (M4 "Collect"). The k8s fetch path is
  provisionally settled in #18: an exec-streamed tar, no
  `kubectl cp`.
- **#39 Patched distribution rsync on the controller** (owner,
  2026-11-27, task zm4) — the controller's rsync must be 3.4.0
  or newer, or a distribution build of 3.2.4 or newer with the
  January 2025 receiver fixes backported: CVE-2024-12087 (path
  traversal) and CVE-2024-12088 (`--safe-links` bypass). el9's
  `rsync-3.2.7-1.el9_8` and later qualify; its changelog names
  them by Red Hat's ids, RHEL-70208 and RHEL-70210, not by CVE.
  Reasons: the main target's own package is 3.2.7 with the fixes,
  every agent of the M8 trial stopped on a bare "3.4.0" rule, and
  one built rsync from an unnamed source instead; a distribution
  build is maintained, a self-built one is not. 3.2.4 is the
  floor because collect needs `--fsync`. The guide (ch. 2) gives
  a one-line check per distribution; the M4 acceptance preflight
  reads the package changelog of the rsync first in `PATH`.
  [security.md](../../security/access.md) keeps the reasons for
  the rule itself.
- **#16 Trace-file segmentation** — rotate once per minute,
  close-then-rename into `ready/`, collect-while-running via
  rsync with delete-after-sync (see M4). Compression is
  parquet-internal zstd — whole-file compression
  (`.parquet.zst`) would make segments unqueryable without a
  decompress step. Variable event arguments map to one
  nullable column per argument field (see "Trace output
  contract"). A size cap forces an early rotation for very hot
  sessions (settled by S3): a segment rotates at one minute or
  64 MB (≈ 3.5 minutes at the #25 reference rate of 18 MB per
  minute), whichever comes first, so a segment never outgrows
  what one `collect` moves comfortably.
- **#26 Cross-host join key** — the TCP initial-sequence-number
  pair (decided by S2, 2026-11-18): the client's `(iss, irs)`
  equals the server's `(irs, iss)`, which NAT leaves untouched.
  Each tracer records `snd_una`, `rcv_nxt`, `bytes_acked` and
  `bytes_received` with the socket's role on its TCP state
  records, at `ESTABLISHED` or later; the merge rebuilds the pair
  (connecting side `iss = snd_una − bytes_acked`, accepting side
  one less, both `irs = rcv_nxt − bytes_received − 1`, mod 2³²)
  and joins on it, inside a coarse time window that guards
  against ISN-clock wrap. The 4-tuple plus a time window breaks
  under NAT and port reuse and is not a fallback across hosts.
  Conntrack on the NAT host is optional — it needs a tracer or
  netlink access there and adds nothing the two ends lack. When
  no pair matches (sequence-randomizing firewalls, SYN proxies,
  an untraced far end), the edge stays unresolved, never a
  guess; L4/L7 proxies that terminate the connection stay the
  route map's job (#15). Details:
  [docs/spikes/s2-cross-host.md](../../spikes/s2-cross-host.md).
