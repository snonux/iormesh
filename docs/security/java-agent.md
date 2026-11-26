# The Java agent

Part of [Security & scope](../security.md).

- **The agent spool is untrusted input.** `iormesh run -spool`
  creates the directory mode 1733 (anyone may create, nobody may
  list or delete another's file) after checking, on a descriptor
  opened without following symlinks, that it is a directory the
  tracer's uid owns. The tracer, root, opens a ring without
  following symlinks and maps it only if the descriptor shows a
  regular file with one link owned by the uid of the process its
  name names — a planted file is reported and never mapped —
  and the mapped header must name that pid. The name is
  `<pid>.ring`, or `<pid>.<hex>.ring` (task 9j4): in the sticky
  spool the agent cannot delete another user's file, so a
  `<pid>.ring` planted for a JVM's pid (known in advance for a
  live attach) would otherwise keep that JVM's agent from
  installing; the agent leaves such a file alone and takes a name
  with a random 64-bit tag nobody can plant ahead of it. The
  suffixed name carries the pid too and passes the same owner and
  header checks; the planted file itself, owned by a uid other
  than the process's, is reported and never mapped. A ring its owner
  truncates under the mapping faults on access, which the reader
  turns into a corrupt ring, not a crash (docs/schema.md, "Java
  attribution"). The owner check keeps a user from speaking for
  another user's process, not for an untraced process of their
  own, so a ring is read only once its process is traced: an
  event of that pid and incarnation (its attach or fork event at
  the latest) has passed the Java join. Until then the ring is
  held unread — not drained, no http or dns event, no table row,
  no uprobe attached — so a ring mapped before the session adopts
  its JVM (`JAVA_TOOL_OPTIONS` at exec) is still read, while a
  forged ring, or one of a JVM outside the target that inherited
  the agent, never is; nor is one whose process's start time
  cannot be read when it is found (gone, or `/proc` unreadable),
  since only the ring's own, writer-chosen stamp would be left to
  match. Only the newest waiting ring per pid stays mapped. A
  header naming a capacity over 64 MiB is refused, and a record
  over 64 KiB (four times the largest the agent writes) is
  skipped as malformed before anything of it is decoded, so one
  ring puts at most about its capacity on the tracer's heap.
  Rings of dead JVMs are unlinked (task 2j4) — by the agent at
  exit, by the join after the JVM's exit, by the scan when the
  process is gone — through the spool directory's descriptor,
  without following a symlink, and only while the name still
  names the regular single-link inode meant, so root never
  removes a planted symlink or a reused pid's new ring.
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
  Details in [java-attach.md](../java-attach.md).
- **Late attach adds JVMs, not trust** (task lh4; behaviour and
  bounds in milestones.md M3 "Attach"). With `-agent`, a `run` also
  loads the agent into JVMs that appear during the session, and
  only into processes the session traces already: a traced
  process (a child included) that execs (`session.handle`), a
  pattern match the session adopts at its exec
  (`session.judgeExec`, after the tracer's own exec is left
  out), and a restarted port owner it follows
  (`session.adoptOwner`, under the owner rule above). A child
  forked without an exec gets nothing. Each attempt is the
  start-time attach unchanged (`lateAttacher.attempt`, like
  `loadOne`): the same loader and the same `agentStages.target`,
  so the same jar — the path `trustedAgent` resolved and checked
  at start, which a confined session pins to
  `/opt/iormesh/lib/iormesh-agent.jar` — and, for a JVM in
  another mount namespace, a stage made by `javaattach.Stage`'s
  checks (a trusted `/` and `/tmp` of the JVM's root, its PID
  namespace the tracer's, shared only with a JVM of the same
  euid and `/tmp`), removed with the session (`lateAttacher.stop`
  hands every stage to the Java join). `javaattach.Attacher.Load`
  then runs its checks as for any attach: `libjvm.so` mapped, the
  attach mechanism not disabled (`attachDisabled`), the trigger
  file created `O_EXCL|O_NOFOLLOW` or an existing one accepted
  only as root's or the JVM's euid's (`createTrigger`,
  `checkTrigger`), the SIGQUIT sent through a pidfd after
  re-checking the JVM and its handler (`sigquit`), and the
  socket's peer held to the target's pid and euid
  (`checkPeer`). A JVM that refuses — `-XX:+DisableAttachMechanism`,
  a failed stage or handshake — is named once and never tried
  again (`lateAttacher.try`). Retries happen only for the two
  errors raised before any signal is sent, no `libjvm.so` yet
  (`ErrNotJVM`, for 5 s from the exec) and no SIGQUIT handler yet
  (`ErrNoQuitHandler`, for 30 s): one attempt sends at most one
  SIGQUIT, and a job that sent one ends with that attempt,
  whatever its outcome. The start-time attach holds each JVM it
  loads until that JVM's own load ends (`lateAttacher.hold`,
  `loaded`, `release`, tasks 7l4, 9l4): an exec of one
  meanwhile is not tried beside it, and handed over afterwards
  only if its start-time load failed before any signal, so each
  JVM gets at most one SIGQUIT per image from the session — also
  when an exec's record arrives after the release: one made
  before it is dropped (`execBoot`, task dl4). When
  the session gives the start-time loads up, the claims left
  hand nothing over (`releaseAll`). `attachDisabled` sees the flag only on
  the command line or in `JAVA_TOOL_OPTIONS`/`JDK_JAVA_OPTIONS`;
  a JVM that sets it elsewhere (an options file) still never
  loads the agent, but answers the one SIGQUIT with a thread
  dump on its stdout. The attaches run beside the trace loop, at
  most 2 handshakes and 64 waiting processes at a time
  (`lateAttachParallel`, `lateAttachPending`); an exec beyond
  that gets no agent and is named, so an exec storm in the
  target costs the tracer bounded work.
