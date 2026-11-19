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
  — and connect to its own attach socket, refused unless the
  socket belongs to the JVM's uid or root. As root the tracer
  reaches every user's JVM, whose agent then runs as that user.
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
- **Traces are sensitive data.** Collected segments stay on the
  controller until the operator deletes them — production
  metadata on a laptop deserves the care that implies.
- **Kubernetes**: eBPF privileges are node-kernel-wide — a
  sidecar with CAP_BPF can in principle observe any process on
  the node. iormesh scopes itself by config to the pod it runs
  in, is added only by the deployment owner's explicit patch,
  and its container image is signed like the tracer binary.
- **Rehearsed first.** Expansion rules — allowlist, hop limit,
  cycle termination — are proven against the test workload in a
  sandbox before they are ever used on real hosts.
