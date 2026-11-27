# Trace data

Part of [Security & scope](../security.md).

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
  The msghdr data calls (task oh4; why, in milestones.md M4 "Clock
  reconciliation from the traffic itself") read the caller's
  memory only for lengths. `sendmsg` and `recvmsg` take their
  bytes from the return value; a `recvmsg` with `MSG_TRUNC`
  also reads the leading fields of its `struct msghdr` and the
  `iov_len` of at most 8 iovec entries, to bound what was copied
  (`internal/c/recv.c` `ior_recvmsg_capacity`, emitted by
  `internal/generate` `writeRecvmsgCapacityCapture`).
  `sendmmsg` and `recvmmsg` park the address of the caller's
  `mmsghdr` array at the enter, and the exit reads only the
  4-byte `msg_len` of each of the first `ret` entries — at most
  1024, one `bpf_probe_read_user` each — and sends their sum
  (`internal/c/mmsg.c` `iormesh_mmsg_on_enter`,
  `iormesh_mmsg_sum`, `iormesh_mmsg_on_exit`). No buffer the
  iovecs point to, nothing `msg_name` or `msg_control` points to
  is read; the enter records the fd, as for every other data
  call (the generated handlers in
  `internal/c/generated_tracepoints.c`). A read that faults, or an exit
  without its park, makes the sum a marked lower bound
  (`MMSG_BYTES_PARTIAL`), never a guess.
- **Metadata is not harmless — redaction covers it.** Command
  lines carry passwords and tokens, URIs carry ids and secrets
  in path segments and query strings. One redaction policy
  applies to HTTP headers, query params and argv alike (path
  segments are recorded verbatim in v1, decision #22's
  default, and only the controller's per-path counts fold
  ids into templates); environment capture is off by default. HTTP
  metadata is redacted twice: by the Java agent before it
  reaches the spool, and again by the tracer before it is
  written; a test keeps the agent's header lists equal to the
  tracer's. Header names use the argv globs anywhere in the
  name, so prefixed credential headers
  (`X-Forwarded-Authorization`, `X-Amz-Credential`,
  `X-Auth-Key`) are masked too, as are the identity headers
  auth proxies and clouds forward (`X-JWT-Assertion`,
  `Cf-Access-Jwt-Assertion`, `X-Amzn-Oidc-Data`, `X-Auth`) and,
  under any name, a value holding a JWT or a `Bearer`/`Basic`
  credential; forward-auth URI headers
  (`X-Original-URI`, `X-Forwarded-URI`) have their query
  values masked like a `Location`; the full list is in
  [schema.md](../schema/redaction.md#redaction). The workload plants
  secrets in four headers, a query parameter and argv, and the
  tests assert none of them ever reaches a trace.
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
  redacted HTTP path templates. Ids are folded (task xh4): a
  segment of only digits, a canonical UUID or 16+ hex digits
  with a digit becomes `{id}`, and the operator's
  `path_templates` in routes.yaml win over that, so the
  database stores one row per route, not one per id, and not
  the ids it recognizes. An id the rules miss stays, as do
  rows a build stored before task xh4 (they are folded when
  read); the traces keep the raw paths. The database is
  created 0600 in a 0700 directory
  and refused when it or its directory is foreign-owned or open
  to others, like the registry. It holds no secrets beyond what
  the traces already redacted (decision #9), and `iormeshctl
  services` quotes control characters and invalid UTF-8 from the
  traces before they reach the terminal. Every other iormeshctl
  print of server-, cluster- or file-supplied text does the same;
  a `mage test` gate (`internal/buildgate/ctlprint_test.go`, task
  oj4) fails on an fmt print in `internal/ctl` whose text value
  skips that escape.
- **Prune deletes only the store's own old segments, and only
  when told** (tasks yh4, gk4; the rule is milestones.md M4 "Collect"
  and "Build window and prune"). Nothing cleans the store on its
  own: `iormeshctl prune -older-than DUR` runs only as the
  operator's command, as the operator (no sudo, no server
  contacted), and without `-apply` it only lists
  (`pruneCommand`, `listPrune` in `internal/ctl/prune.go`). The
  store must pass collect's rule — a directory the operator owns
  and others cannot write (`pruneStore`, `checkStoreDir`). A
  file is a candidate only if it is a regular, non-dot file
  directly in `<store>/<project>/<host>/`, under a project and
  host directory that are real directories (not symlinks) with
  valid names, and its name parses as a segment whose time range
  ended before now−DUR; a table file goes only once no events
  segment of its session stays (`planPrune`, `realSubdirs`,
  `oldInHost`). Other names, dot files (rsync's temporaries, the
  fetch lock), symlinks, subdirectories and the host pins are
  never listed. `-apply` first has the service database forget
  the listed segments in one transaction
  (`servicedb.DB.Forget`): their request rows, and every
  connection no remaining segment names; naming rules are never
  touched, and a missing database is not created
  (`forgetPruned`, `internal/servicedb/forget.go`). If that
  fails, nothing is deleted. Then each listed path is removed
  through an `os.Root` opened on the store (`applyPrune`), so a
  directory swapped for a symlink between the listing and the
  delete cannot lead outside the store. The listing's checks are
  not repeated at the delete: a name swapped meanwhile for a
  symlink is removed as the link, never its target.
