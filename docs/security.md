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
  directory is forced to 0700.
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
  Bootstrap and the signature scheme are open decisions in
  PLAN.md (#1, #28).
- **The lifecycle subcommands take no free-form input to
  systemd or the filesystem's root.** `start`, `stop`, `status`
  and `uninstall` (task 9b4) call `systemd-run` and `systemctl`
  with an argv, never a shell string; the unit name is built
  only from a project label `internal/project` accepts; start's
  cgroup caps are checked against their documented forms, and
  every forwarded run flag is one `-name=value` element.
  `uninstall` removes exactly `/opt/iormesh` — no path flag,
  since under the sudoers rule one would delete any tree as
  root — and only after every `iormesh-<project>` unit stopped.
  `state.json` is written by the run in its checked, locked
  project directory, replaced by an atomic rename of a file
  opened without following symlinks.
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
