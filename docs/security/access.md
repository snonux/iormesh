# Access and trust boundaries

Part of [Security & scope](../security.md).

- **Read-only tracing.** Privileged (root) for eBPF, but
  observation only — no tampering with what the target does or
  the data it handles. Observation still costs something: the
  Java agent rewrites bytecode at I/O boundaries, and probes —
  kernel tracepoints and uprobes in the JDK's libraries — take
  time, so overhead is budgeted, measured, and capped by the
  tracer's cgroup limits.
- **The LLM eval sends trace data only where the operator
  says.** `mage eval` (milestones.md M5) puts the rollup and query
  slices of a trace into a model's prompt. It has no default
  model: without an explicit `IORMESH_EVAL_MODEL_CMD` it refuses
  to run, and `mage test` never runs it. It is meant for the
  workload's traces, with a local model unless sending them out
  is fine; the model's replies can run only slice flags of
  `iormeshctl query` on the eval's own store and read only the
  workload's source. No word of a prompt can make a model CLI
  attach a laptop file: the harness escapes `@` mentions and
  turns Claude Code's file mentions off (task wm4,
  [testing/focused-checks.md](../testing/focused-checks.md#llm-introspection-eval-task-qb4)).
- **The operator's SSH key is the only way in.** The tracer
  listens on nothing and connects to nothing; control flows only
  through SSH (or `kubectl exec`). Servers hold no iormesh
  credentials.
- **The registry is the deploy boundary.** Only hosts in
  `~/.config/iormesh/servers.yaml` are ever contacted. The file
  is written 0600 in a 0700 directory and refused when it or its
  directory is foreign-owned or group- or other-writable —
  whoever edits it chooses where the operator's key pushes a
  root-run tracer. Host names are checked before they reach ssh
  (no leading `-`, no shell or `host:path` characters), and
  remote commands go as single-quoted argv, never as
  concatenated shell text; that quoting needs a POSIX login
  shell on the server. Mesh expansion (M6) widens the boundary
  only through the same file: its allowlist (`expand:`) lives
  there, a registry without one allows nothing, and
  `iormeshctl expand` without `--apply` contacts no server.
  Peers are proposed by the address a traced client reached,
  never by a name it looked up; a connection the merge could
  not place (ambiguous, keyless) proposes nothing, and an
  address a fused connection proves to be NAT in front of a
  traced server is listed, never enrolled. Any other front — a
  VIP, load balancer or NAT with an untraced backend, a SYN
  proxy — is proposed by its own address: keeping such ranges
  (and container bridges like 172.17.0.0/16) out of the
  allowlist is the operator's job. Expand writes nothing, not
  even the host pins `build` records.
- **A server controls trace contents, nothing else on the
  controller.** Collect copies with `rsync -rt`: remote modes
  and symlinks are not carried over, and the local trace
  directory is forced to 0700; only the non-dot `*.parquet`
  files directly in `ready/` are taken, never a subdirectory or
  a dot file. A file over 1 GiB is skipped (`--max-size`, the
  cap the pod path's fetch stream applies per file), so one
  oversized segment cannot fill the controller's disk; it stays
  on the server and `iormeshctl rm` lists it as a leftover (task
  gi4). rsync checks the size the server's own rsync declares,
  so this bounds a server's files, not a server whose rsync
  binary lies; there is no cap on total bytes per collect yet.
  Each host gets its own directory
  (`~/.local/share/iormesh/traces/<project>/<host>/`), so one
  server cannot overwrite another's segments by naming files
  after it; project and host come from the registry, never from
  the server — rm's sweep before an uninstall collects only
  projects the registry or deploy's record lists for the host,
  and treats any other project directory the server reports as
  leftovers (rm refuses over them without `-force`, task
  hi4) — and the directories above are refused when
  foreign-owned or writable by others. The file names are the
  server's: a segment whose FQDN or session holds anything but
  letters, digits, `-`, `.`, `_` and `:` is ignored as a stray,
  so no server-chosen name puts shell or terminal text into a
  ref (task 3i4). Only the store and what is below it are
  checked: a `-store` under a directory others can write
  without the sticky bit is unsafe whatever its own mode.
  Collect needs no sudo: `ready/` belongs to the deploying
  user.
- **The local site treats trace text as hostile.** Every string
  a build shows came from a server, and the site
  (`iormeshctl open`, milestones.md M5) is opened in the operator's
  browser. Its page loads nothing and runs only its own code: a
  `default-src 'none'` CSP allowing just the inline script and
  stylesheet by hash; the model embedded as an inert JSON block
  whose `<`, `>`, `&` and U+2028/9 are escaped, so no string can
  close it; strings put on the page only as text nodes and
  attributes the script picks (never `innerHTML`); control and
  bidi characters shown escaped; the offered `query -ref`
  command shell-quoted, as it is in the TUI. Site folders are
  0700, files 0600 — they name production hosts, processes and
  files — and `latest` is followed only to a folder directly
  beside it ([visualization.md](../visualization.md) "The local
  site").
- **The controller's rsync must be 3.4.0 or newer, or a
  patched distribution build** (decisions.md #39). The
  receiving rsync parses what the server sends, and older ones
  let a malicious sender write outside the destination or read
  the receiver's memory (CVE-2024-12087, CVE-2024-12088,
  CVE-2022-29154). A distribution build of 3.2.4 or newer with
  the two 2024 fixes backported (el9's `rsync-3.2.7-1.el9_8`)
  closes the same holes; the guide's
  [chapter 2](../guide/02-prerequisites.md#the-laptop-the-controller)
  checks it. macOS ships rsync 2.6.9 (or openrsync):
  install a current one (Homebrew `rsync`) and put it first in
  `PATH`.
- **rm never deletes uncollected traces silently.** Before
  `sudo iormesh uninstall` it stops and syncs off every project
  under `/opt/iormesh/traces`, registered or not, and refuses
  while a segment is left or a session did not end cleanly
  (`iormesh stop` fails then); only `rm -force` uninstalls over
  them.
- **Host keys are verified, never trusted on first use.**
  iormeshctl runs the system `ssh`/`rsync` with
  `StrictHostKeyChecking=yes` and `BatchMode=yes`: an unknown or
  changed server key fails the run (decisions.md #12). Agent, port
  and X11 forwarding are switched off on every call
  (`ForwardAgent=no`, `ClearAllForwardings=yes`, `ForwardX11=no`,
  `ForwardX11Trusted=no`; `ClearAllForwardings` alone leaves X11
  on), whatever `~/.ssh/config` says: expansion points the
  operator's key at hosts found from traffic, which get a login
  and nothing more.
  A host cannot stall a run for the others: ssh drops a dead
  session after three missed keepalives
  (`ServerAliveInterval=15`, `ServerAliveCountMax=3`), rsync a
  transfer idle for 60 s (`--timeout=60`), and each server host
  gets an hour before iormeshctl reports it failed and moves on.
  A pod entry gets the same hour; inside it each kubectl get,
  debug or short exec gets 2 min and the fetch stream 2 min of
  silence, so a stalled exec cannot hold the fetch lock (task
  pi4).
  Its control sockets — live, authenticated sessions — sit in a private
  0700 directory that is refused if a symlink, foreign-owned or
  open to others.
- **Expansion is controller-driven.** The mesh grows along
  observed connections, but only `iormeshctl` deploys — from the
  controller, with the operator's key. Tracers never deploy and
  never talk to each other, so there is no lateral credential to
  steal and no node-to-node auth to get wrong. Guardrails:
  - listing unresolved peers is the default; deployment needs an
    explicit `--apply`;
  - only peers matching the expansion allowlist are enrolled —
    everything else is listed, never contacted;
  - a hop limit, a max-node cap and a visited set bound every
    run;
  - `-apply` saves each enrolled peer to the registry before
    contacting it, and then runs deploy's own steps — the same
    Transport, the same one sudoers rule, no new sudo path; its
    only extra remote call is an unprivileged read of
    `/etc/machine-id`, and a machine the store already traces
    is unregistered again with nothing deployed;
  - a peer is bound by its listening port (the server side of
    an edge), and the controller's own SSH/rsync traffic never
    proposes one (`transport`);
  - a damaged segment refuses `-apply`: the visited set would
    miss that host's addresses;
  - each new host's ssh host key must already be in
    `known_hosts` — no trust on first use for expansion either:
    fetch it (`ssh-keyscan HOST`), verify the fingerprint out of
    band, add it, then `-apply` or `deploy HOST`; `-apply`
    prints that hint when ssh refuses a key;
  - `Enroll` re-checks every candidate before the registry
    changes — allowlist, hops, `max_nodes`, a routable IPv4
    unicast address, never a broadcast one — so it can never
    widen what the plan allowed;
  - a host_id is what a server says it is: a hostile server can
    claim another machine's id, as its traces' host_id or its
    `/etc/machine-id`. That can only keep a real host from
    being enrolled (it looks known already) — it cannot widen
    the deploy boundary, which the allowlist and registry draw.
- **Rehearsed first.** Expansion rules — allowlist, hop limit,
  cycle termination — are proven against the test workload in a
  sandbox before they are ever used on real hosts.
