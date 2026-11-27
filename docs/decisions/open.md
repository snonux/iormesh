# Open — built on the default

Part of [Decisions](../decisions.md).

## Open — raised for M2 (built on the default)

- **#7 Event families, default-on vs opt-in** — signals, epoll
  registrations, mmaps and renames all add volume; reuse ior's
  trace-family concept and pick defaults per measured cost
  (S3). *Default: file, socket and lifecycle on; the rest
  opt-in.* S3 supports the default: the consumer's per-event
  cost, not the probes, limits throughput, so every family
  that fires at syscall frequency stays opt-in.
  M2 step 7 (task sa4) implements the default as iormesh's own
  families (ior's concept, iormesh's grouping:
  `internal/tracepoints`): `file` and `socket` on — socket now
  includes bind and the datagram calls sendto/recvfrom, since
  unix sockets and UDP are sockets — and lifecycle always on
  (its probes decide what is traced); `ipc` (pipes,
  socketpairs), `signal` (kill/tgkill/tkill and
  signal_generate), `poll` (epoll, poll) and `zerocopy`
  (sendfile, splice, tee, copy_file_range) opt-in through
  `iormesh run -trace-families ipc,signal,…|all`. The flag only
  adds: file and socket cannot be turned off (the scenario
  markers are opens). Task oh4 grows socket by the msghdr data
  calls sendmsg, recvmsg, sendmmsg and recvmmsg — in socket,
  not a family of their own: an opt-in family would leave the
  clock proof's byte sums open to them by default (M4), and
  they are socket data calls like sendto/recvfrom. Their
  events reuse the schema's rule that a syscall kind is its
  syscall's name (`sendmsg`, …, appended to schema v1).
  Task nk4 names their datagram peer as sendto/recvfrom do,
  for the DNS resolvers and QUIC servers that send through
  them unconnected: the exit reads the msghdr's `msg_name`
  and `msg_namelen` (recvmsg's kernel writes the sender's
  length back there) and sends the same address record, IPv4,
  IPv6 or AF_UNIX, never for a stream socket. For sendmmsg
  and recvmmsg it is the first message's address only: one
  record per call, a fixed 16-byte read with no loop, so the
  el9 5.14 verifier sees a straight line next to mmsg.c's
  bounded sum (the exits grew by about 600 processed
  instructions, to 42.6k of the 1M limit). A batch to several
  peers is named by its first; the rest are not guessed. Its
  whole byte sum goes to that first peer's flow or connection:
  a QUIC server's recvmmsg of datagrams from several clients
  adds them all to the first client's flow in the report
  (task hl4 pins this with two senders). Renames and mmaps are
  not families: the renames are the file surface's, mmaps are
  not traced yet.
  ior's restart fold (one row for a call a signal interrupted
  and the kernel re-executed) is not part of the signal family
  and not lifted: a restarted call stays two rows, the first
  with its restart code (-512…-516). Its kernel half — two
  probes never attached, and enter/exit hooks emitting records
  nothing read — was removed (task pf4); lifting it needs both
  of its signal probes and a userspace consumer.
- **#23 Target-pattern mechanics** — match on exec (argv/exe)
  vs comm; glob vs regex; concurrent matches (newest wins vs
  trace all vs error); restart semantics (wait for
  re-appearance vs end the session); and the listening-port
  target expansion needs (resolve once at start vs follow the
  port across restarts). Kernel comm's 15-char limit makes
  argv matching the primary source for Java apps. *Default:
  glob on argv, trace all matches, wait for re-appearance.*
  M2 steps 1–3 (task pa4) implement the default: the glob runs
  over argv and the exe path, never the comm, and the
  exec-to-attach window is written as an `attach_gap` drop.
  Task mh4 settles the listening-port target: *follow the port
  across restarts, conservatively.* Every host `expand -apply`
  enrolls is bound by port, so ending at the first restart lost
  the host. When the last traced process of a `-listen-port`
  session is gone, the session re-resolves the port's owners
  about once a second for a grace period (`-listen-grace`,
  default 2m; 0 ends at once, the old behaviour). A holder is
  adopted only if it runs the same executable (" (deleted)"
  ignored, so a package update still matches) under the same
  effective uid as a process the session started with: the same
  port alone is not the same app. Task qj4 tightens both halves:
  only start holders running the executable of the
  earliest-started holder (the one that most likely bound the
  port) are remembered, so another program that merely inherited
  the listener fd (a root helper) does not widen what is
  accepted. Their uids are kept as they are: a
  privilege-separated server (nginx, httpd: a root master and
  workers of the same binary under their own uid, all holding the
  listener) is followed whole, since the restarted workers fork
  before the next re-resolve and fork-following never picks up a
  process that already exists. And a holder is adopted only
  while its pid still has the start time the port listing read
  with its fd table, so a pid reused in between is never
  mistaken for the holder. Task kk4 widens the executable half
  for an upgrade, conservatively: a restart onto a new path (a
  JDK or package upgrade into a versioned directory,
  `.../java-25-openjdk-25.0.5/bin/java` to `.../25.0.6/...`) is
  adopted if its executable has the same base name, its
  effective uid is the same, *and* it runs in the same cgroup
  (on systemd the service's unit, on cgroup v2 the `0::` path,
  else the `name=systemd` one) as a remembered holder. The base
  name alone is not enough — `java` names every JVM of the user
  — and the cgroup alone is not either: a unit may run helpers.
  No readable cgroup, or the root cgroup `/` (which says
  nothing), leaves only the exact path. A different program
  (another base name), another uid, and qj4's inherited-fd
  helper (not remembered) stay refused; the service manager is
  never a holder. A manual restart of an upgraded binary from
  another login session is not followed (another scope): start
  a new session. An adopted holder
  gets an `attach` event of its own incarnation and the session
  goes on; stderr names it. No match within the grace ends the
  session `targets_exited`, stderr saying no new owner appeared.
  A service manager — a process whose executable is named
  `systemd`, pid 1 or a user manager, holding a socket-activated
  listener — is never a target, at start or later: tracing it
  would follow every service it spawns. Task lk4 settles a port
  only the manager holds (an idle socket-activated service, also
  under `expand -apply`): *wait for the activated service,
  conservatively.* The session starts with no target, pins the
  listening sockets the manager holds (their inodes), and
  re-resolves about once a second within `-listen-grace` (0
  keeps the old refusal at start). With no original owner there
  is no owner key to compare against, so the socket itself and
  the manager's unit layout identify the service: a holder is
  adopted only if it (1) holds one of the pinned sockets — still
  listening on the port, the very socket systemd hands to the
  service it activates, not merely the port — (2) is not itself
  a service manager, and (3) runs in a `.service` unit cgroup
  (slices skipped; a delegated sub-cgroup of the unit counts)
  inside the holding manager's subtree: anywhere for pid 1, under
  `user@UID.service` for a user manager. Task el4 makes the user
  manager's subtree strict: the manager must run in the
  `init.scope` directly below `user@UID.service`, UID its own
  effective uid, or it names no subtree; and (4) a holder below a
  user manager must have that manager's effective uid — a
  user's services run as that user, so a setuid program or
  another user's process in the tree is not one. A login-session
  scope, a shell or another unit's process holding a copy of the
  fd is refused and named once. The parent pid is not checked: a
  wrapper script that forks its server is still the service. For
  pid 1 the uid is not checked (`User=`). The adopted holders
  are then remembered as start holders are (qj4's
  earliest-started executable), so later restarts and
  re-activations follow the ordinary rules above; if no adopted
  holder's owner key can be read, the pin stays and the next
  activation is waited for as at start (task el4). A service
  that activates between the start's resolve and the pin is
  caught by resolving the port once more (task el4). No
  activation within the grace ends the session `targets_exited`,
  stderr saying so. Not covered: `Accept=yes` sockets (each
  instance gets a connection, never the listener) and a socket
  unit restarted while waiting (new sockets, not the pinned
  ones). No unit name is read: that needs systemd's D-Bus,
  which the tracer does not use. A new owner that listens while
  the old one still runs (an overlapping restart) is adopted
  once the old one exits, within the grace.
- **#9 Capture policy for argv and env** — argv is in, but it
  is not harmless: command lines carry passwords and tokens
  (`-Dpassword=…`, `--token …`). Argv is recorded through the
  same redaction policy as HTTP (denylist patterns, values
  masked). Capturing the environment at exec
  (`/proc/<pid>/environ`) is useful for the model but a
  privacy call to make explicitly. *Default: argv redacted,
  env off.* M2 step 8 (task ta4) implements the default:
  `internal/redact` masks option and KEY=VALUE values by a
  denylist, program-scoped flags (mysql's `-p`), URL userinfo
  and query values, denied pairs of `&`/`;` runs (form bodies,
  scheme-less JDBC such as h2 files) and Oracle's thin
  `user/password@` (task zi4), and, in one generic last pass, a
  denied key's value in any "key, `:` or `=`, value" syntax —
  JSON (`--spring.application.json=`, `curl -d '{…}'`), YAML,
  quoted dicts, pairs inside shell strings (task rm4). Task
  qp4 holds all of it to one corpus of real secret carriers
  (`internal/redact/testdata/corpus.tsv`, also read by the Java
  agent's tests for headers and URLs): a case is masked or an
  accepted limit listed in docs/schema/redaction.md, and a test
  fails either way it drifts; still a denylist, the corpus
  adds the SQL password keywords and Go DSNs it found. Every
  exec of a traced process
  becomes an `exec` event carrying its redacted argv, and the
  environment is never read. A process already running when the
  trace attaches (a `-pid` or port target, a pattern's start-scan
  match) has no exec; task nh4 records its argv and executable on
  its `attach` event instead, read from `/proc/<pid>/cmdline` and
  `/proc/<pid>/exe` at attach, checked against the start time
  seeded, and redacted by the same policy before the event
  exists. An unreadable `/proc` (process gone, hidepid, another
  pid namespace) leaves both absent and never fails the attach.
  Every `/proc` command line read for an event (this one, an
  exec's completion) is bounded at 64 KiB, its cut last element
  masked, so no event line nears the readers' 1 MiB cap.


## Open — raised for M3 (built on the default)

- **#29 JDK version support** — the project first targeted JDK
  21, but fleets run 17 and older. Boundary hooks, stacks and
  HTTP events do not need 21 (only the deferred virtual-thread
  support would). S1 (from sources, no 21 runtime then) found
  the hook set's natives identical from 21 to 25 (27-ea changes
  two signatures outside the set), while 17 differs (no
  `UnixFileDispatcherImpl`/`UnixDispatcher`, RAF `readBytes`
  instead of `readBytes0`); the hooked *Java* methods differ
  more, which sc4 settled as three variants (below). M3 (task
  0b4) builds the agent for 17 with shaded ASM and ships
  per-JDK table variants. A real JDK 21
  (task sc4) showed one hooked Java method S1's source reading
  missed: `Net.serverSocket` keeps its `boolean` stream flag
  through 24 and drops it in 25 — so three variants, `jdk17`
  (17–20), `jdk21` (21–24) and `jdk25` (25+). The agent runs and
  its hooks fire on 17, 21 (21.0.6, 21.0.12), 25 and 27-ea; 22
  and 24 were checked by `javap` of their class files only, 23
  and 18–20 are untested.
  *Default: agent runs on 17+; anything
  older is traced without Java attribution (the non-Java
  node shape).*
- **#22 HTTP capture mechanics** — agent-boundary hooks (v1
  set: JDK HttpClient, HttpURLConnection, the JDK's built-in
  HttpServer (the microservices workload's REST server),
  Servlet, Apache HttpClient, OkHttp, Netty?); redaction
  policy — denylist (drop Authorization/Cookie/… by default)
  vs allowlist (record only named headers), sensitive query
  params, and URIs: verbatim, path-only, or templated
  (`/orders/{id}`) — path segments carry ids and tokens too;
  and HTTP/2 request pairing — capture stream ids at the hooks
  vs leave HTTP/2 hops time-ordered in v1. *Default: denylist,
  query values masked, HTTP/1.1 pairing only.* Task 3b4
  implements the default for the JDK's HttpServer, HttpClient
  and HttpURLConnection: headers on internal/redact's denylist
  and every query value are masked in the agent (and again by
  the tracer), paths are verbatim, and `req_seq` is recorded
  over HTTP/1 only. Task de4 adds the third-party libraries in
  #22's order, rewritten in the app's class loaders: Servlet,
  Apache HttpClient 5, OkHttp and Netty's HTTP/1.x codec.
  Task 0j4 widens the header denylist to the argv globs
  (anywhere in a name: X-Forwarded-Authorization, Cookie2,
  X-Amz-Credential) plus named key globs (`*api*key*`,
  `*auth*key*`, `*access*key*`, `*private*key*`,
  `*subscription*key*`, `*functions*key*`, not `*key*`), and
  masks the forward-auth URI headers (X-Original-URI/-URL,
  X-Forwarded-URI, X-Rewrite-URL, X-Envoy-Original-Path) as
  URLs like Location; a test fails when the agent's copy and
  internal/redact's lists differ. Task np4 adds identity
  globs (`*jwt*`, `*assertion*`, `*oidc*`, `*userinfo*`,
  `*principal*`, `*csrf*`, `*xsrf*`, `*otp`, `*auth`,
  `authentication*`) and masks a value by its shape under any
  name — a JWT anywhere in it, or a Bearer/Basic/Digest/
  Negotiate scheme with a credential — since a vendor header
  name says nothing reliable while these shapes do; still a
  denylist, the shapes are only more deny rules. Task qp4's
  corpus (see #9) adds `*authentication*` anywhere, `*hmac*`,
  `*websocketprotocol*` and any white space after the scheme.
  *Decided (task xh4): templated at the controller's
  aggregations, raw in the events.* The trace keeps the path
  verbatim (no schema change, no data lost, `iormeshctl query`
  and report.json show what was requested); the build folds
  id-like segments where it counts per path — the service
  database's request keys, hence the services view's top
  paths. Built-in folding is conservative: a segment of only
  digits, a canonical UUID, or 16+ hex digits with a digit
  becomes `{id}`; words and versions (`v2`, `cafe`,
  `report-2024`) never fold. The operator's `path_templates`
  in routes.yaml (`/shops/{shop}/orders/{order}`: a `{name}`
  segment matches any one segment) win over it, first match,
  a literal template pinning a path verbatim. A source
  imported again replaces its rows on a connection, so a
  changed rule or the upgrade never counts requests twice;
  rows stored raw before it (marked by the database's schema
  version 3) fold by the built-in rules when read, stored
  templates never (internal/pathtmpl).
- **#8 Hostname & TLS visibility** — DNS hostname↔IP via Java
  agent hooks (InetAddress) is the clean path; TLS peer
  identity (trust-manager/SSLSocket hooks) is extra value at
  extra surface. Non-Java nodes get no hostnames this way.
  *Default: DNS hooks in v1, TLS identity later.* Task wd4
  implements the DNS half: the agent hooks
  `InetAddress.getAllByName(String)` (stable 17–27) and records
  each new host-to-addresses mapping of a JVM as a `dns` event
  (literals and failed lookups not); TLS identity stays later.
- **#20 OTEL integration mode** — agent-read span context
  (primary, socket-free) vs ingesting the app's OTLP stream (a
  local receiver the app dual-exports to, or the collector's
  file export) vs both; and whether the views render a
  distributed-trace overlay on the mesh. Ingested spans are
  the app's own exported telemetry — deliberately distinct
  from the no-payload-capture rule, worth stating in
  docs/schema.md. *Default: span-context tagging only.*
  Built so (task 4b4): span-context tagging, the `otel`
  envelope object; ingestion stays deferred (below).


## Open — raised for M4 (built on the default)

- **#1 Remote bootstrapping and sudo scope** — transport is
  settled: SSH, key-based, from the controller only. The sudo
  scope is one pinned sudoers rule for exactly
  `/opt/iormesh/bin/iormesh` (ior precedent: scoped sudoers
  rules). Raised: bootstrap and upgrade. The first install needs
  broader privilege once (the operator's own sudo, or
  configuration management laying down the binary and the
  rule), and the binary must be root-owned and not writable by
  the deploying user — otherwise the pinned rule is full sudo
  in disguise. Upgrades therefore go through a tracer
  subcommand that verifies the staged binary's signature
  against a root-owned public key (#28) before replacing
  itself. Built so (task bb4, the upgrade half):
  `iormesh upgrade FILE` runs through the rule and takes
  only the staged file and `FILE.minisig`. It reads both once
  into memory, verifies them against
  `/opt/iormesh/etc/minisign.pub`, requires the signed name to
  be this arch's tracer (`iormesh-linux-<arch>`) and the signed
  version not older than the installed binary's (checked under
  an exclusive lock on `bin/`; through the rule the installed
  binary must still be the running process's own file, and the
  same version with other bytes is refused, so every release
  bumps the version), then installs exactly the verified
  bytes: a new root-owned 0755 file in `bin/`, renamed over
  `/opt/iormesh/bin/iormesh`. It first
  checks the layout — `/opt`, `/opt/iormesh`, `bin/`, `etc/`
  root-owned directories nobody else may write, the installed
  binary and the key regular root-owned files, never a
  symlink — and refuses rather than repairs. A downgrade needs
  `-allow-downgrade`, which root may pass but the rule may not:
  an older signed build may carry a fixed hole.
  Built so (task qh4, the bootstrap half; owner-approved
  2026-11-22): the first install — binary, key and rule — is
  the operator's own sudo, driven by `iormeshctl bootstrap
  HOST` (or configuration management doing the same). Without
  `-apply` it contacts nothing and prints the exact steps,
  with the hashes of the build it verified, for the arch and
  SSH user `-arch` and `-user` name (default amd64 and the
  registry host's `user@` part, else the controller's own user
  name, ssh's default login — a `User` in `~/.ssh/config` is
  not seen); the rule's user must be one user, never `ALL` or
  an all-caps alias name (sudoers would widen it); with
  `-apply` it reads both from the host (`uname -m`, `id -un`;
  a flag that disagrees is refused) and runs the same steps
  over the Transport. Interactive sudo over ssh needs
  a TTY and the Transport has none (`BatchMode`, decision #12),
  so `-apply` runs every privileged step as `sudo -n`: it needs
  the SSH user's own sudo to work without a password
  (NOPASSWD, as on cloud images), and a host whose sudo asks for
  one is refused with a pointer to the printed steps, which the
  operator runs by hand on a terminal. Only registered hosts,
  and only hosts without the tracer (deploy upgrades the
  rest); an existing `/etc/sudoers.d/iormesh` is refused, never
  overwritten. The steps: the controller verifies
  `dist/iormesh-linux-<arch>` against its pinned key exactly as
  deploy does and stages those verified bytes, its signature,
  the pinned key and the rule in `~/.iormesh/staging`; root
  creates `/opt/iormesh`, `bin/` and `etc/` (root:root 0755),
  installs each file under a temporary name next to its place
  (sudo ignores a `.`-name in `sudoers.d`), checks the root-owned
  copy's SHA-256 against the controller's (the rule also with
  `visudo -cf`) and only then renames it into place — the
  binary never runs before that check; then, still under the
  operator's sudo and before the rule goes live, the installed
  tracer's own `upgrade` of the staged build re-verifies it
  against the root-owned key and checks the whole layout
  (same bytes: a no-op); last the rule is installed and a
  `sudo -n iormesh status` proves it. The rule is
  `USER ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh` for the
  SSH user, under a marker comment line, generated from
  `internal/hostlayout` — the one source the tracer's gate,
  deploy and the docs' tests use. `iormesh uninstall` (rm's
  last step) removes `/etc/sudoers.d/iormesh` when its first
  line is that marker, and leaves any other rule alone.
- **#28 Signature scheme** — which tool signs the tracer build
  (minisign, cosign, `ssh-keygen -Y`), where the public key is
  pinned (controller config; a root-owned copy on servers for
  upgrades), and who holds the private key (the release
  pipeline). *Default: minisign, key pinned in the controller
  config.* *Amended by #32 (owner, 2026-11-26): release archives
  ship unsigned and each operator signs with their own key, so
  "the release pipeline" below is the operator.* Built with the
  default (task bb4; the owner has not
  decided otherwise): minisign signatures made and checked with
  `aead.dev/minisign` (pinned in go.mod/go.sum; the files are
  plain minisign, so the `minisign` tool checks them too). The
  signed trusted comment carries `file:` and `version:`, so a
  renamed or older build cannot pass as the one wanted.
  `mage sign` signs `dist/` with the release pipeline's
  encrypted secret key from `$IORMESH_MINISIGN_KEY`, which must
  lie outside the repository; no private key is ever committed,
  tests generate throwaway keys. The controller pins the public
  key at `~/.config/iormesh/minisign.pub` (`iormeshctl verify`,
  which deploy runs too); servers hold the root-owned copy at
  `/opt/iormesh/etc/minisign.pub`. Key rotation is replacing
  both files — the server's with the operator's own sudo, as
  in bootstrap.
  *Provisional (2026-11-21, owner-delegated default, task kf4):
  the Java agent jar is a release artifact signed and checked
  exactly like the tracer* — no decision had yet said how the
  jar reaches a server, and M4's TLS label needs it there. `mage
  agentBuild` copies it to `dist/iormesh-agent.jar`, `mage
  sign` signs it at the release version (`file:
  iormesh-agent.jar`) — and refuses to, as it refuses a tracer
  or controller, when the artifact's own stamp (the jar's
  manifest `Implementation-Version`, a binary's embedded
  version) differs, so a stale build is never signed as the
  release (task uf4; the stamp proves the version, not the
  commit — task 8g4 left a vcs revision out, as the container
  builds see no `.git`, and docs/build.md has dist/ rebuilt
  from the release commit before signing) — `iormeshctl
  deploy` verifies it with the
  pinned key and ships it only to hosts with a project using
  `-spool` or `-agent`, and `iormesh upgrade` — no new
  subcommand, no wider sudoers rule — installs a file whose
  signed name is the jar's as `/opt/iormesh/lib/iormesh-agent.jar`
  (root's, 0644; `lib/` created root's 0755 by the first install,
  so the bootstrap is unchanged). The jar's signed version must
  equal the installed tracer's (one ring format, and an older
  signed jar may carry a fixed hole); through the rule the same
  version with other bytes is refused, as for the tracer. A
  confined session's `-agent` may name only that file, so root
  never loads a jar the release key did not sign: the tracer's
  "never run unverified code" rule covers the jar; `start`
  forwards the checked, resolved jar path to its unit, whose run
  re-checks it. The installed jar has no version of its own, so
  a tracer upgrade to another version removes it under the
  upgrade lock and the next deploy installs the matching one
  (until then `-agent` fails, closed). JVMs matched at exec get
  the same file by `run -agent`'s late attach (M3, task lh4),
  or through `-javaagent` pointing at it, which misses nothing
  before the attach (the M4 runner sets `JAVA_TOOL_OPTIONS`;
  the product sets nothing in a JVM's environment). Overturned if the owner
  wants the jar inside the tracer binary (one artifact, extracted
  at start) or a separate key for it.
- **#24 Service-interaction database** — controller-side:
  sqlite via a pure-Go driver (keeps iormeshctl cgo-free;
  additive merges and queries come natural) vs a plain YAML
  file (trivially diffable and inspectable, but incremental
  updates and bigger meshes get awkward); one database per
  project vs one with project scoping; glob vs regex for the
  naming rules. Lives under `~/.local/share/iormesh/`, next to
  the collected traces. *Default: sqlite, one database, glob
  rules.* Task hb4 built the default (`modernc.org/sqlite`,
  `~/.local/share/iormesh/services.db`, every row carrying its
  projects, `-project` filtering the listings, rules global);
  the owner has not decided otherwise.
- **#21 Project semantics** — for now a project is a manual
  label (`-project`, kept with the registry entry); later
  candidates: defaulting to the k8s namespace, or
  auto-derivation from the traced app. Cross-project
  collection: explicit project list vs merge-everything-found.
  *Default: manual label, explicit list.* The registry (task
  cb4) implements the default: each server entry lists its
  projects by label, each with its own target and flags. In k8s
  mode the project defaults to the namespace (M7): task vb4
  built the tracer side — `iormesh run` in a pod (the
  downward-API `POD_NAME`, `POD_NAMESPACE` and `POD_UID` all
  set) without `-project` takes the namespace; an explicit
  label still wins.
- **#17 Disk-watchdog semantics** — percent of partition vs an
  absolute free-bytes floor (or both); check interval; and
  what the controller does when a tracer exits on the limit
  (surface it, redeploy with a lower duration, or nothing).
  *Default: both limits, surface only.* The tracer side took the
  default (task 8b4): 90% and a 256 MiB floor, checked every
  5 s and before every rotation. The controller half surfaces
  it (task lf4): `build`'s summary and `show health` name a
  session that ended on `disk_limit`; nothing is redeployed.
