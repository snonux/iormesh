# Security & scope

Companion to [PLAN.md](../PLAN.md) — the plan is the source of
truth.

- **Read-only tracing.** Privileged (root) for eBPF, but
  observation only — no tampering with what the target does or
  the data it handles. Observation still costs something: the
  Java agent rewrites bytecode at I/O boundaries, and probes —
  kernel tracepoints and uprobes in the JDK's libraries — take
  time, so overhead is budgeted, measured, and capped by the
  tracer's cgroup limits.
- **The LLM eval sends trace data only where the operator
  says.** `mage eval` (PLAN.md M5) puts the rollup and query
  slices of a trace into a model's prompt. It has no default
  model: without an explicit `IORMESH_EVAL_MODEL_CMD` it refuses
  to run, and `mage test` never runs it. It is meant for the
  workload's traces, with a local model unless sending them out
  is fine; the model's replies can run only slice flags of
  `iormeshctl query` on the eval's own store and read only the
  workload's source.
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
  a dot file. Each host gets its own directory
  (`~/.local/share/iormesh/traces/<project>/<host>/`), so one
  server cannot overwrite another's segments by naming files
  after it; project and host come from the registry, never from
  the server, and the directories above are refused when
  foreign-owned or writable by others. Only the store and what
  is below it are checked: a `-store` under a directory others
  can write without the sticky bit is unsafe whatever its own
  mode. Collect needs no sudo: `ready/` belongs to the
  deploying user.
- **The local site treats trace text as hostile.** Every string
  a build shows came from a server, and the site
  (`iormeshctl open`, PLAN.md M5) is opened in the operator's
  browser. Its page loads nothing and runs only its own code: a
  `default-src 'none'` CSP allowing just the inline script and
  stylesheet by hash; the model embedded as an inert JSON block
  whose `<`, `>`, `&` and U+2028/9 are escaped, so no string can
  close it; strings put on the page only as text nodes and
  attributes the script picks (never `innerHTML`); control and
  bidi characters shown escaped; the offered `query -ref`
  command shell-quoted. Site folders are 0700, files 0600 — they
  name production hosts, processes and files — and `latest` is
  followed only to a folder directly beside it
  ([visualization.md](visualization.md) "The local site").
- **The controller's rsync must be 3.4.0 or newer.** The
  receiving rsync parses what the server sends, and older ones
  let a malicious sender write outside the destination or read
  the receiver's memory (CVE-2024-12087, CVE-2024-12088,
  CVE-2022-29154). macOS ships rsync 2.6.9 (or openrsync):
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
  changed server key fails the run (PLAN.md #12). Agent and
  port forwarding are switched off on every call
  (`ForwardAgent=no`, `ClearAllForwardings=yes`), whatever
  `~/.ssh/config` says: expansion points the operator's key at
  hosts found from traffic, which get a login and nothing more.
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
- **Signed tracer binary, one sudoers rule.** The controller
  verifies the build's signature before pushing it. On the
  server every privileged action is a subcommand of the one
  binary, so a single pinned sudoers rule covers the lifecycle.
  That rule is only as strong as the binary's ownership: it must
  be root-owned and not writable by the deploying user, and
  upgrades verify the signature against a root-owned key.
  Bootstrap is still open (PLAN.md #1); signing and upgrade
  are built with #28's default (task bb4):
  - **Who signs**: the release pipeline, with `mage sign` over
    `dist/` (minisign via `aead.dev/minisign`). The secret key
    is minisign's scrypt-encrypted file at
    `$IORMESH_MINISIGN_KEY`, refused inside the repository;
    no private key is ever committed — tests generate
    throwaway keys per run.
  - **What is signed**: each binary, with a trusted comment
    `file:<name> version:<x.y.z>` that the signature covers. A
    file renamed to another artifact's name, the arm64 tracer
    staged on amd64, iormeshctl staged as the tracer, or an
    older build fail on that comment, not just on the bytes.
  - **Where the key is pinned**: the controller's
    `~/.config/iormesh/minisign.pub` (the operator's own file,
    writable by nobody else; `iormeshctl verify`), and on each
    server the root-owned copy `/opt/iormesh/etc/minisign.pub`,
    laid down with the binary at bootstrap. Neither path is a
    flag of the tracer.
  - **`iormesh upgrade FILE`**, through the rule: the staged
    file and `FILE.minisig` are first opened `O_PATH|O_NOFOLLOW`
    and checked by `fstat` — regular files, not symlinks, owned
    by the sudo caller (not by root or anyone else, so the rule
    never has root read others' files; root directly may stage
    its own) — and only then reopened for reading through
    `/proc/self/fd`, so root never opens a device or FIFO a
    caller names. They are at most 256 MiB and 4 KiB; both are
    read once into memory, and exactly the verified bytes are
    installed — changing the staged file after the check
    changes nothing. Before any read, the layout is checked:
    `/opt` (followed), `/opt/iormesh`, `bin/` and `etc/` are
    root-owned directories nobody else may write, the
    installed binary and the key root-owned regular files —
    a symlinked install is refused, not replaced. The new
    binary is a fresh file in `bin/`, chowned root:root on its
    descriptor before it is made 0755, fsynced and renamed
    over the old one (then `bin/` fsynced), so a crash leaves
    one or the other. A signed version older than the
    installed binary's is refused, checked under an exclusive
    lock on `bin/` that is held through the rename: through the
    rule the installed binary must still be the very file
    (device and inode) the running process was started from,
    so an upgrade stopped with ^Z, or racing another, cannot
    put an older build over one installed meanwhile — it is
    told to rerun. `-allow-downgrade` is root's alone and
    refused through the rule, since an older signed build may
    carry a fixed hole. The same version with the same bytes
    is a no-op (deploy re-pushes); through the rule the same
    version with other bytes is refused, so every release
    bumps the version. Running
    sessions keep the old binary; the next `start` runs the
    new one.
  - **The Java agent jar takes the same path** (task kf4,
    PLAN.md #28): `mage agentBuild` copies it to
    `dist/iormesh-agent.jar`, `mage sign` signs it at the
    release version (only if its manifest's
    `Implementation-Version` is that version, task uf4;
    [build.md](build.md) "Signing"), and `iormesh upgrade FILE` installs a
    file whose *signed* name is `iormesh-agent.jar` as
    `/opt/iormesh/lib/iormesh-agent.jar` — the same staged-file,
    pinned-key and read-once checks as for the tracer. Its
    signed version must equal the installed tracer's (the two
    speak one ring format, and an older signed jar may carry a
    fixed hole); through the rule the same version with other
    bytes is refused, as for the tracer, and `-allow-downgrade`
    does not apply. `lib/` is created by the first install,
    root:root 0755 (set explicitly, whatever root's umask), so
    a bootstrap made before the jar shipped needs nothing new;
    an existing `lib/` or jar that is a symlink, not root's, or
    group- or other-writable is refused, never repaired. The
    jar is a fresh root:root 0644 file renamed into place:
    readable by every JVM user it is loaded into, changeable by
    root alone. Root loads it into every user's JVM (and the
    JVM user loads it with `-javaagent`), so it gets the
    tracer's rule: never run unverified code.
    The installed jar carries no version of its own, so its
    match with the tracer is kept by the upgrades: an upgrade of
    the tracer to another version removes the jar under the
    same lock (it was checked against the old tracer), and the
    next deploy with an agent project installs the matching one.
    Until then a session's `-agent` and a JVM's `-javaagent`
    fail — closed, never an old jar under a new tracer (task
    kf4's review). An installed jar must be exactly 0644 and
    root's; anything else is refused, not repaired.
  - **`iormeshctl deploy`** (task db4) verifies the build and
    its signature on the controller before each host's push,
    stages both in the deploying user's `~/.iormesh/staging`
    (not `/tmp`, where another user could pre-create the name;
    never `/opt`), and runs under `sudo -n` only the installed
    binary's `upgrade`, `start`, `stop` and `status` — never
    `-allow-downgrade`. The controller's check is not the one
    that counts: the server's `upgrade` verifies the pushed
    bytes again against its own root-owned key, so a file
    swapped between the two checks is refused there. The staged
    path is passed absolute (the home from `pwd`), so it does
    not rely on sudo keeping the working directory (a
    `Defaults runcwd` would change it). Registry values reach
    the server only as single-quoted argv elements, and an
    entry carrying a flag the confined start refuses fails the
    deploy before any host is contacted. A new build restarts
    the live sessions, so a fixed hole does not keep running in
    the old binary. A host without the installed tracer is
    reported, not bootstrapped (PLAN.md #1). A host with a
    project that uses the Java agent (`-spool` or `-agent`)
    also gets `dist/iormesh-agent.jar`, verified on the
    controller (signed by the pinned key, as itself, at the
    tracer's version) before anything is pushed, staged with
    the tracer and installed by a second `upgrade` after the
    tracer's; any refusal starts nothing (task kf4).
  - **What the rule then grants**: installing a build the
    release key signed, for this arch, not older, and the
    agent jar signed at that build's version — nothing
    else. Whoever holds the release key holds every server
    that pins it.
- **Through the sudoers rule, the tracer writes only to its own
  paths.** The rule names the installed binary and nothing else:

  ```
  deploy ALL=(root) NOPASSWD: /opt/iormesh/bin/iormesh
  ```

  (`deploy` being the deploying user). It is only as strong as
  the install: `/opt/iormesh`, `/opt/iormesh/bin` and
  `/opt/iormesh/etc` must be root-owned and not writable by
  group or others, and `/opt/iormesh/bin/iormesh` and
  `/opt/iormesh/etc/minisign.pub` root-owned regular files, not
  symlinks (`upgrade` checks exactly this). Under the rule
  every argument is the caller's and runs as root, so the
  binary gates itself
  (`internal/cli/confine.go`, task 9b4): when `SUDO_UID` is set
  and its resolved path lies under `/opt/iormesh` — or
  `/opt/iormesh/bin/iormesh` is a symlink, or its own path is
  unknown (fail closed) — only `run`, `start`, `stop`, `status`,
  `upgrade` and `uninstall` run. Every other subcommand
  (`normalize`, `attach`, `version`, any added later) is
  refused: "not allowed through the sudoers rule; run it
  without sudo or as root directly".
  Sessions are confined: `start` always (its unit runs as
  root), `run` under the same gate (a build run with the
  operator's own full sudo, `sudo ./iormesh run -out DIR`, is
  not), and the unit's own run again: `start` launches it with
  `IORMESH_CONFINED_UNIT=1` (`systemd-run --setenv`), which only
  ever adds confinement, so the unit re-checks what `start`
  checked. `start` forwards to the unit the values it checked,
  not the caller's spelling of them: `-agent` as the jar's
  resolved real path, whose every directory is root's, `-spool`
  and `-trace-dir` cleaned and absolute — a symlink the caller
  re-points between the check and the unit's start changes
  nothing (task kf4's review). Confined:
  - `-out`, `-cpuprofile` and `-ready` are refused — each
    creates, truncates or removes whatever file it names;
  - `-trace-dir` must be `/opt/iormesh/traces` (after cleaning;
    nothing else, not even a subdirectory), the default;
  - `-spool` must be `/run/iormesh/spool/<project>`, under a
    root-created 0755 `/run/iormesh/spool`, since the tracer
    makes its spool world-writable (1733);
  - `-ready-owner` may only name the sudo caller
    (`SUDO_UID:SUDO_GID`), which `start` passes by default;
  - `-duration` is at most 7 days (everywhere), so the unit's
    `RuntimeMaxSec` stays bounded;
  - `-agent` must be `/opt/iormesh/lib/iormesh-agent.jar` (its
    path, or its real path where `/opt` is a symlink): the jar
    `upgrade` verified and installed (task kf4). Everywhere,
    `-agent` needs a jar that root owns: the jar and each
    directory above it must belong to root or the caller (the
    euid, root under sudo) and not be group- or other-writable,
    sticky directories excepted — so a sudoer cannot have root
    load a jar they could rewrite, nor one the release key
    never signed. Since `start` is always confined, root's own
    `start` too takes only the installed jar; a jar of root's
    choice goes through `run` directly.
  - the test-only clock skew `IORMESH_TEST_CLOCK_SKEW`
    ([testing.md](testing.md)) is refused, by `start` always —
    so a sudoer cannot have a root session record falsified
    timestamps (sudo's `env_reset` normally drops it anyway).

  Every directory the tracer creates is set 0755; one that
  already exists is never chmod'ed. `start`, `stop`, `status`
  and `uninstall` call `systemd-run` and `systemctl` with an
  argv, never a shell string; the unit name is built only from
  a project label `internal/project` accepts; start's cgroup
  caps are checked against their documented forms; every
  forwarded run flag is one `-name=value` element, and a value
  with `$` is refused (systemd 254+ expands `$VAR` in a unit's
  command line; `--expand-environment=no` is unknown to older
  systemd). `uninstall` removes exactly `/opt/iormesh` — no path
  flag, which would delete any tree as root — and only after
  every `iormesh-<project>` unit stopped. `state.json` is
  written by the run in its checked, locked project directory
  (atomic rename of a file opened without following symlinks);
  `status` and `stop` read it only as a regular file, not
  through a symlink, at most 64 KiB. By design, `-pid`,
  `-pattern`, `-listen-port` and `-agent` reach other users'
  processes — that is what a tracer is for — so the sudoer gets
  their I/O metadata in segments they own; grant the rule only
  to whoever may see that.
- **The agent spool is untrusted input.** `iormesh run -spool`
  creates the directory mode 1733 (anyone may create, nobody may
  list or delete another's file) after checking, on a descriptor
  opened without following symlinks, that it is a directory the
  tracer's uid owns. The tracer, root, opens a ring without
  following symlinks and maps it only if the descriptor shows a
  regular file with one link owned by the uid of the process its
  name names — a planted file is reported and never mapped —
  and the mapped header must name that pid. A ring its owner
  truncates under the mapping faults on access, which the reader
  turns into a corrupt ring, not a crash (docs/schema.md, "Java
  attribution").
- **Live attach touches the target only as `jcmd` would.**
  `run -agent` and `iormesh attach` create a trigger file in the
  JVM's working directory (or `/tmp`), send it one SIGQUIT — only
  if it maps `libjvm.so` and handles the signal, through a pidfd
  — and connect to its own attach socket; the request goes out
  only if the connected peer is the target pid with its euid
  (`SO_PEERCRED`), so a socket path the target pointed at another
  process — a root JVM — is refused. The target's directories are
  held as fds (`/tmp` via `openat2` `RESOLVE_IN_ROOT`, no
  symlinks), so its planted symlinks never lead root into the
  host's tree. As root the tracer reaches every user's JVM, whose
  agent then runs as that user — and root's own JVMs too, so the
  agent jar must be changeable by nobody but root or the caller:
  the jar and each directory above it owned by them and not
  group- or other-writable (sticky directories excepted).
  Details in [java-attach.md](java-attach.md).
- **Bounded by design.** Sessions end after 4h by default
  (enforced twice — by the tracer and by its systemd unit) and
  stop before the trace partition fills.
- **Metadata-only tracing.** No file or socket payload contents
  are recorded — paths, sizes, offsets, endpoints, stacks. The
  one deliberate exception: HTTP(S) request metadata (method,
  URI, headers), captured at the Java API boundary — never
  sniffed off the wire. Host names a JVM resolved are recorded
  with their addresses (DNS events, decision #8), from the
  JDK's own lookup API, not from DNS traffic. The class-load map
  records where each class on a recorded stack came from — JAR
  paths, like the file events' paths, verbatim; a remote code
  source URL loses its userinfo, query and fragment in the agent
  and again in the tracer.
- **Metadata is not harmless — redaction covers it.** Command
  lines carry passwords and tokens, URIs carry ids and secrets
  in path segments and query strings. One redaction policy
  applies to HTTP headers, query params and argv alike (path
  segments are recorded verbatim in v1, decision #22's
  default); environment capture is off by default. HTTP
  metadata is redacted twice: by the Java agent before it
  reaches the spool, and again by the tracer before it is
  written. The workload plants secrets in four headers, a
  query parameter and argv, and the tests assert none of them
  ever reaches a trace.
- **The trace directory is root's.** The tracer writes and
  renames segments as root, so the trace dir and the project
  directory must be real directories owned by its euid and not
  group- or other-writable — anyone who could write them could
  swap `ready/` for a symlink after the check and have root
  publish wherever it points. A planted or loosened directory
  stops the start; it is never repaired. `ready/` must be a real
  directory too, and only it is handed to the deploying user
  (`-ready-owner`). One run per project directory: an exclusive
  lock on `<project>/.lock` refuses a second (task 8b4).
- **Traces are sensitive data.** Collected segments stay on the
  controller until the operator deletes them — production
  metadata on a laptop deserves the care that implies.
  The service database (`~/.local/share/iormesh/services.db`,
  task hb4) distills them into hosts, addresses, hostnames and
  redacted HTTP paths: it is created 0600 in a 0700 directory
  and refused when it or its directory is foreign-owned or open
  to others, like the registry. It holds no secrets beyond what
  the traces already redacted (decision #9), and `iormeshctl
  services` quotes control characters from the traces before
  they reach the terminal.
- **Kubernetes** (M7; decisions #13, #14, #18 are provisional —
  PLAN.md "Provisional — M7"): eBPF privileges are
  node-kernel-wide — a container with `CAP_BPF` and
  `CAP_PERFMON` can in principle read any process's memory and
  the kernel's. What the minimal profile buys over `privileged`
  is no devices, no mounts, no namespace escapes and no
  `CAP_SYS_ADMIN`; it does not make BPF pod-local. The tracer
  container:
  - runs as uid 0, `allowPrivilegeEscalation: false`, in a pod
    with `hostUsers: true`, drops ALL and adds `BPF`, `PERFMON`,
    `SYS_RESOURCE` (libbpfgo's memlock bump, not the kernel),
    plus `SYS_PTRACE`, `DAC_OVERRIDE`, `KILL` and
    `CHECKPOINT_RESTORE` when it attributes Java — each traced
    to the code that needs it in PLAN.md #13 and checked by
    leaving it out
    ([testing.md](testing.md#k8s-capability-set-task-ub4));
  - runs as SELinux type `spc_t` at level `s0` — the tracer
    container alone, never the pod, so the app containers keep
    `container_t` and the pod's MCS pair. The `s0` level keeps
    the pod's `/etc/hosts`, `resolv.conf` and trace `emptyDir`,
    which the runtime relabels to the tracer's level, open to the
    app; they no longer carry the pod's own MCS pair. Every
    `container_t` process on the node dominates `s0`, so MCS no
    longer separates these files from other pods — only mount
    namespaces do, and a hostPath over `/var/lib/kubelet/pods`
    or an escape reaches them; the label stays after a debug
    tracer exits. On
    an SELinux-enforcing node (el9) `container_t` is denied
    `bpf { prog_load }` outright (task zg4); `spc_t` is
    unconfined by SELinux, so on such a node the capability set,
    `RuntimeDefault` seccomp and no privilege escalation are what
    bound the tracer. `iormeshctl deploy -selinux-type` (or
    `selinux_type` in the registry) names another type, empty
    omits it; a dedicated `iormesh_container_t` policy module
    granting only the tracer's `bpf`, `perf_event` and tracefs
    permissions is planned (PLAN.md #13, task 7h4);
  - mounts the host's tracefs read-only and nothing else of the
    host; traces live in a size-bounded `emptyDir` of the pod.
    The pod must carry both volumes before the tracer comes
    (an ephemeral container cannot add one), so it is prepared
    once by the deployment owner's patch;
  - scopes itself to the pod by its PID namespace: targeting
    keys by the pid in the tracer's own namespace (task 4f4), so
    a process outside the pod cannot be named as a target (the
    kernel side gives it no number at all), and `SYS_PTRACE` and
    `KILL` reach only what that namespace's `/proc` shows — the
    pod. `DAC_OVERRIDE` is not
    so bounded: it overrides file permissions on everything the
    container's mount namespace holds — its own image and
    volumes, and through `/proc/<pid>/root` the pod's other
    containers, whose files a pod's root could not otherwise
    open. `hostPID` is not used: it would give the app
    containers the node's process table too;
  - stages the agent jar and spool inside the JVM's root for a
    live attach (PLAN.md #14; built by task cf4): a root-owned
    0755 directory under the JVM's `/tmp`, created exclusively
    (`mkdirat` on the `RESOLVE_IN_ROOT` fd) under a random name
    — a name that already exists is never adopted, the JVM's
    user could have planted it; the next fresh name is tried —
    and checked to be root's on its fd; the jar copied in with
    `O_EXCL|O_NOFOLLOW`; the spool beside it 1733, read through
    its held fd, its rings checked as on a host. Refused unless
    the JVM's `/` and `/tmp` are root's or the JVM user's and
    not writable by group or others without the sticky bit:
    otherwise another user could rename the directory away and
    have the JVM load their own jar (an `emptyDir` at `/tmp`,
    0777 without the sticky bit, is refused for that). The
    checks run for every JVM, and a JVM shares another's stage
    only with the same euid and the same `/tmp` directory object.
    A JVM in another PID namespace than the tracer's is not
    staged for: its ring's name would be read as another
    process's pid. It costs the app
    container's ephemeral storage, or its memory where `/tmp` is
    a tmpfs (the jar and one fixed-size ring per JVM). The
    tracer removes the directory through its fds when its
    session ends; a session killed before that leaves it to the
    container's lifetime — a later session does not remove it,
    since it cannot tell a dead session's stage from a live
    one's;
  - is reached only through the operator's kubectl, with the
    cluster named by the registry (`--context` always explicit,
    never the kubeconfig's current context) — the registry stays
    the deploy boundary. The operator's RBAC for this —
    `patch` on `pods/ephemeralcontainers` and `create` on
    `pods/exec` in a namespace at the `privileged` level — is
    itself node-wide read access: whoever holds it can start a
    BPF-capable container there, so grant it like root on the
    nodes;
  - comes as an image pinned by digest and signed like the
    tracer binary — the static tracer and the agent jar on
    `scratch`, no shell or package manager (task vb4; the
    manifests refuse a tag); a sidecar is added only by the
    deployment owner's explicit patch, a debug container only
    by the operator's `iormeshctl`. The patches and the debug
    profile are generated into `deploy/` from internal/k8s and
    test-checked field by field
    ([testing.md](testing.md#k8s-manifests-and-pod-identity-task-vb4)).
    `iormeshctl deploy` checks a tracer container already in the
    pod — the sidecar, or an earlier debug container — against
    its status `imageID`, the digest the kubelet pulled, and
    refuses another digest or an `imageID` naming none (a
    locally loaded image), so a sidecar patch that names a tag
    still runs only the pinned bytes (task af4);
  - is collected into a store directory named by the pod's UID
    (`pod-<uid>`), pinned to the host identity the API server's
    answer implies before any segment arrives — never by what
    the pod's own segments claim (task af4);
  `privileged: true` is the fallback where the minimal profile
  cannot run (old kernels, an SELinux policy without `spc_t`,
  runtimes without the newer capability names; it cannot be combined
  with `allowPrivilegeEscalation: false`); it widens what a
  compromised tracer could do to the whole node, so use it only
  where the minimal set was tried. On an SELinux-enforcing node
  a `container_t` tracer is refused `bpf { prog_load }`; the
  tracer's load error then names SELinux and its context instead
  of libbpf's memlock hint (task 6h4; the denial shows in the
  raw audit.log, not always in `ausearch -m avc`), and the el9
  choices are PLAN.md #13's (task 5h4). Pod Security `baseline` and
  `restricted` refuse both profiles: the tracer needs a
  namespace at the `privileged` level or an exemption — the
  owner's call per namespace.
- **Rehearsed first.** Expansion rules — allowlist, hop limit,
  cycle termination — are proven against the test workload in a
  sandbox before they are ever used on real hosts.
