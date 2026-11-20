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
  shell on the server.
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
  changed server key fails the run (PLAN.md #12). Its control
  sockets — live, authenticated sessions — sit in a private
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
    run.
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
    reported, not bootstrapped (PLAN.md #1).
  - **What the rule then grants**: installing a build the
    release key signed, for this arch, not older — nothing
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
  root), and `run` under the same gate (a build run with the
  operator's own full sudo, `sudo ./iormesh run -out DIR`, and
  the unit's own run are not). Confined:
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
  - `-agent` needs a jar that root owns: the jar and each
    directory above it must belong to root or the caller (the
    euid, root under sudo) and not be group- or other-writable,
    sticky directories excepted — so a sudoer cannot have root
    load a jar they could rewrite.

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
- **Kubernetes**: eBPF privileges are node-kernel-wide — a
  sidecar with CAP_BPF can in principle observe any process on
  the node. iormesh scopes itself by config to the pod it runs
  in, is added only by the deployment owner's explicit patch,
  and its container image is signed like the tracer binary.
- **Rehearsed first.** Expansion rules — allowlist, hop limit,
  cycle termination — are proven against the test workload in a
  sandbox before they are ever used on real hosts.
