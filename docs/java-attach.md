# Attaching the Java agent

The Java agent (`iormesh-agent.jar`, milestones.md "M3 — Java agent")
names the Java method, stack and thread behind each syscall of a
JVM. It gets into a JVM in one of two ways (decision #2):

- **`-javaagent` at launch — the primary path.** The JVM starts
  with `-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=DIR` and
  `iormesh run -spool DIR` joins its I/O from the first syscall
  on. Silent, nothing missed.
- **Live attach to a running JVM** — for "trace this PID now"
  without a restart, at the cost of the caveats below.

## On hosts iormeshctl deploys to

The jar ships like the tracer (task kf4, decisions.md #28):
`mage agentBuild` copies it to `dist/iormesh-agent.jar`, `mage
sign` signs it at the release version, and `iormeshctl deploy`
pushes it to every host with a project that uses the agent — a
registry entry carrying `-spool` or `-agent` — after checking it
against the pinned key. On the host `sudo iormesh upgrade`
checks it again against the root-owned key, requires the
tracer's version, and installs it as
`/opt/iormesh/lib/iormesh-agent.jar` (root's, 0644). A deployed
session may use only that jar and its project's spool:

```
iormeshctl add -project shop -pattern 'java -jar */shop.jar*' app1 \
    -spool /run/iormesh/spool/shop -agent /opt/iormesh/lib/iormesh-agent.jar
iormeshctl deploy app1
```

`-agent` reaches the JVMs running when the session starts. A
JVM started later, matched at exec, needs the agent at launch —
the same installed jar, e.g. through the environment so the
command line the pattern matches stays as it is:

```
JAVA_TOOL_OPTIONS=-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/shop \
    java -jar shop.jar
```

That line is an example for operators, not something iormesh
sets: the product never touches a JVM's environment. The M4
acceptance runner uses it to start its microservices
([testing.md](testing.md#m4-acceptance-on-two-real-hosts-task-lb4)).
Mind what `JAVA_TOOL_OPTIONS` does: every JVM started with it
in its environment prints `Picked up JAVA_TOOL_OPTIONS: …` on
stderr, and child processes inherit it, so a JVM that spawns
other JVMs loads the agent into all of them (each writing its
ring into the same spool) — prefer `-javaagent` on the command
line where the pattern allows it.

### A late JVM's start-up

A JVM that starts during a session gets the agent by late
attach (below), up to 30 s after its exec. Its start-up I/O —
class loading, config reads, the first connections — happens
before that and stays unattributed; the tracer says so with
`agent loaded into pid N; its I/O before now stays
unattributed`. This is an accepted limit (milestones.md M3, task
mk4): only `-javaagent` at launch covers a JVM's start, and
that is a change to how the application is started, which the
operator makes. iormesh never writes an application's unit,
drop-in, environment or pod spec, and `iormeshctl deploy`
applies nothing to it.

For an application run as a systemd unit, a drop-in does it
without touching the unit file or the command line a `-pattern`
matches (here the unit `shop.service`, project `shop`):

```
sudo systemctl edit shop.service
```

```
[Service]
Environment=JAVA_TOOL_OPTIONS=-javaagent:/opt/iormesh/lib/iormesh-agent.jar=spool=/run/iormesh/spool/shop
```

then `sudo systemctl restart shop.service`. Before that:

- **An existing `JAVA_TOOL_OPTIONS`.** If the unit sets one
  already (`Environment=`, or an `EnvironmentFile=`, which wins
  over `Environment=`), the drop-in replaces it: put the
  `-javaagent:` option into that value instead.
- **A hardened unit.** Under `ProtectSystem=strict` `/run` is
  read-only for the JVM; add
  `ReadWritePaths=-/run/iormesh/spool/shop` to the drop-in so
  the agent can create its ring. A JVM in a confined SELinux
  domain may be denied the jar or the spool; the audit log
  names the denial.
- **No session running.** The spool exists from the project's
  first session after boot (the tracer creates it and leaves
  it). A JVM started before that finds none: its stderr (the
  unit's journal) gets `iormesh-agent: disabled: …` and it runs
  on without the agent. The next session's start-time attach
  (`-agent`) then loads it live, from that point on. A JVM
  started while no session runs but the spool exists keeps its
  ring there (full, it drops and counts), and the next session
  of the project picks the ring up.
- **Every JVM in the unit.** All JVMs the unit starts inherit
  the variable (see the `Picked up` note above), traced or not.

Remove the drop-in (`sudo systemctl revert shop.service`, which
drops every drop-in of the unit, or delete the file `systemctl
edit` created) and restart to undo it.

In a Kubernetes pod ([kubernetes.md](kubernetes.md#debug-or-sidecar))
`-javaagent` needs the jar and a spool shared with the tracer
inside the app container — a workload change for which iormesh
ships and tests no recipe (decision #14 is open with the
owner), so a pod's JVM start-up stays unattributed there.

## Live attach

```
# load the agent into the JVMs the session starts with
iormesh run -pid 4242 -project shop -out /var/tmp/trace \
    -spool /var/lib/iormesh/spool -agent /opt/iormesh/lib/iormesh-agent.jar

# or load it later, into a JVM a `run -spool` is already tracing
iormesh attach -pid 4242 -agent /opt/iormesh/lib/iormesh-agent.jar \
    -spool /var/lib/iormesh/spool [-timeout 10s]
```

`run -agent` attaches to the processes the session starts with —
the `-pid` list, the `-listen-port` owners, the JVMs a `-pattern`
finds running at start — before its `-ready` file appears; a
process that is no JVM is noted and traced without Java
attribution. A JVM that appears later in the session gets the
agent too (late attach, task lh4): a pattern match exec'd later,
a traced process — a child forked later included — that execs a
JVM, a restarted `-listen-port` owner the session adopts. Such a
JVM is still starting at its exec, so the tracer tries it every
250 ms from its exec until the load succeeds, for at most 30 s:
a process that still maps no `libjvm.so` 5 s after its exec is
no JVM and dropped without a word, a JVM without its SIGQUIT
handler yet (see "Safety") is tried again. One that refuses
(`-XX:+DisableAttachMechanism`, dynamic loading disabled, no
stage possible), is still not ready after 30 s or exits first is
named once on stderr, `no Java attribution for pid N: …`, and
never tried again; a load prints the same `agent loaded into
pid N` line as at start. A JVM the session starts with that
execs while its own start-time load still runs is left to that
load, so it never gets two SIGQUITs at once (task 7l4); it is
tried late afterwards, its windows from that exec, only if its
load found no JVM or no SIGQUIT handler yet — one loaded or
refused then gets no agent in the new image. What counts is
when the exec was made, not when its record arrives (task dl4):
a load that times out waiting for the socket after its SIGQUIT
takes 10 s, and an exec record from before its end that arrives
only after it is left to that load as well, so even then the
JVM gets no second SIGQUIT. Its claim ends
with its own load, not the others' (task 9l4): a JVM loaded at
start that execs a new JVM while a later pid's load still runs
gets the late attach there like any other exec. Every check of
the start-time attach applies unchanged — the same handshake
and its checks of the JVM's uid and namespaces, the same trusted (in a confined
session pinned) jar, a stage inside a foreign root removed when
the session ends. The attaches run beside the trace, never in
its loop: at most 2 handshakes at a time and 64 processes
waiting. A process exec'd while 64 wait gets nothing; the first
one of each such overflow is named on stderr (`… pid N gets no
agent, nor does any other process exec'd before one of them is
done`), and once a waiting attach ends, processes exec'd after
are tried again and the next overflow is named anew. The I/O
such a JVM does before its load stays unattributed — its start-up, mostly —
so give a JVM whose start matters `-javaagent`, e.g. through
`JAVA_TOOL_OPTIONS`. A process forked without an exec (a JVM's
fork) is no new JVM and gets nothing; one that turns up in the
start-time scan and execs while it is loaded, its exec record
reaching the session only after that load ended, may be loaded
twice: the agent, already installed, installs nothing twice but
resyncs its ring, as for any second load (above). `iormesh
attach` returns once the JVM
confirmed the agent started; the running tracer then prints
`java agent of pid N joined, K natives probed` at its next spool
scan (within ≈200 ms) — from there on the JVM's I/O is
attributed. What the line guarantees once `iormesh attach` has
returned too: every hook is installed (the load returns after
the agent's retransformation) and every native of the agent's
whole hook table is probed, so a call the JVM makes from then on
is bracketed and marked. The tracer waits for the table to be
complete (a record after its last row) before it probes;
probing a partly written table left later hooks' I/O
unattributed (task tf4).

The tracer needs no JDK: it speaks HotSpot's attach protocol
itself (`internal/javaattach`, the same handshake `jcmd` and
`VirtualMachine.attach` use). It creates the trigger file
`.attach_pid<pid>` in the JVM's working directory (or its
`/tmp`), sends one SIGQUIT, waits for the JVM's socket
`/tmp/.java_pid<pid>` (reached through `/proc/<pid>/root`) and
sends the load request over it. That is a client connection to
the JVM's own socket: the tracer still listens on nothing.

## Second sessions

A JVM outlives tracer sessions: a session ends (after 4 h by
default), the tracer restarts or is redeployed, and the JVM keeps
its agent. The ring is consume-once, and the agent writes its
dictionary records — the hook table (HOOK), each thread
(THREAD), stack (STACK) and class (CLASS) — once each, so a later
session would read brackets it could never attribute. Each reader
therefore adds one to the ring header's reader epoch (offset 40)
when it opens the ring (task 5j4, decisions.md #27). The agent's
watermark thread polls the word every tick (`watermark=`, 10 ms):
the first reader finds everything still in the ring and needs
nothing; from the second reader on, the thread writes the hook
table again, closes it with a WATERMARK, and bumps a generation
that makes each thread, stack and class be written once more at
its next bracket. The tracer probes the JVM's natives once that
WATERMARK has arrived, as it waits for the install's table (task
tf4). While the ring is too full for the whole table — a new
reader still freeing what the last session left — the agent
writes no watermark at all and retries the table every tick, so
the tracer never probes a partly resent table; it holds the JVM's
syscalls meanwhile, up to its join timeout. Brackets read before
the resent table stay unattributed until their thread and stack
show up again.

The resync costs the application what its first brackets cost (a
THREAD per thread, a stack walk's description and a CLASS per
distinct call site), happens at most once a second however often
readers open the ring (one asked for sooner waits), and never
blocks or throws into an application thread. With `watermark=0`
there is no watermark thread and no resync. Loading the agent
into a JVM that has it already (`iormesh attach` again, a second
`-javaagent`) installs nothing twice but asks for the same
resync; the first load's ring and options stay. It finds the
bridge in the bootstrap loader before extracting anything (task
8j4), so it costs the JVM no temporary jar, descriptor, mapping
or bootstrap search-path entry.

A ring therefore lives as long as its JVM (task 2j4, decisions.md #27).
The JVM's agent unlinks its own ring in its shutdown hook, right
after the last watermark: a tracer that reads the ring has had it
mapped since its first scan, and unlinking leaves the mapping
alone. A JVM killed without its shutdown hooks (SIGKILL) leaves
the file to the tracer: the Java join, told of every traced
process's group-dead exit, reads that JVM's ring one last time
once every event the process may still have upstream is decided
(the correlator's fuse window plus the join timeout past the
exit), then closes and unlinks it and forgets the JVM; and each
spool scan unlinks a ring whose process is gone, such as one left
while no session ran. Nothing unlinks a live JVM's ring. Every
unlink looks at the entry through the spool directory's
descriptor without following a symlink and removes it only if it
is still the regular, single-link file meant — the inode the
reader mapped, the scan listed, or the agent created — so a new
JVM that reused the pid keeps its fresh ring. The join also
forgets a virtual thread once its last bracket is Retain (10 s)
behind the watermark; the agent writes a virtual thread's THREAD
record again at its first bracket 5 s after the last one, so the
thread is known as virtual again before its I/O is joined (an
older agent leaves such a thread unnamed and unflagged, like one
whose THREAD record was dropped). A virtual thread idle that long
then gets a second, identical row in the thread table.

Between sessions the agent goes dormant (task ej4, decisions.md #27).
Once the ring's tail has stood still for 10 s while the ring kept
dropping records (it is full; the watermark thread's own
watermarks drop when the application is quiet) and no reader
bumped the epoch, the watermark thread sets a flag that every
hook reads first among its work: a dormant hook walks no stack and
runs no HTTP probe, and counts the record it skipped as dropped
in the ring header, as the full ring would have, so the watermarks
stay honest about the time it slept. A DNS lookup counts nothing
and is recorded at its next resolution. An awake hook pays one
volatile read for this. The flag clears as soon as the tail moves
(a stalled tracer draining again) or the epoch does (a new
reader), or the agent is loaded again, and waking goes through
the same resync as a new reader. Without the watermark thread
(`watermark=0`) the agent never goes dormant.

Mixed versions: the epoch sits in what was zero padding, so the
ring format version did not change. An agent older than 5j4 never
reads the word: under a newer tracer its first session works and
later ones get no hook table (attribution none), as before; a
newer jar loaded into such a JVM says it found an older agent and
changes nothing. A tracer older than 5j4 never bumps it: a newer
agent under it resyncs only when loaded again (an old
`iormesh attach` still triggers that), which the old tracer reads
as duplicate HOOK rows. The first newer session after old ones
still gets a resync: the agent sees the epoch move after the
ring's tail already had, so some earlier reader consumed the
dictionary.

## Caveats

- **Everything before the attach is missed.** The agent brackets
  only what runs after it installed: earlier I/O stays
  `attribution: none`, and the tracer never guesses it. The same
  holds for state built before the attach — a connection's HTTP
  codec created earlier is not tracked (the agent poisons Netty
  codecs it did not see created), and the OTEL span context is
  read only once the app's OTEL storage is armed (a live attach
  arms the ones already initialized).
- **JEP 451's warning.** From JDK 21 on, a JVM that loads an agent
  dynamically prints four lines on its own stderr:

  ```
  WARNING: A Java agent has been loaded dynamically (/opt/iormesh/lib/iormesh-agent.jar)
  WARNING: If a serviceability tool is in use, please run with -XX:+EnableDynamicAgentLoading to hide this warning
  WARNING: If a serviceability tool is not in use, please run with -Djdk.instrument.traceUsage for more information
  WARNING: Dynamic loading of agents will be disallowed by default in a future release
  ```

  Starting the JVM with `-XX:+EnableDynamicAgentLoading` silences
  it. JDK 17 prints nothing.
- **Dynamic loading disabled.** A JVM started with
  `-XX:-EnableDynamicAgentLoading` refuses the load — as every JVM
  will once a later release makes that the default (JEP 451). The
  JVM keeps running unharmed; the attach fails with
  `dynamic agent loading is disabled in the target JVM (start it
  with -XX:+EnableDynamicAgentLoading, or use -javaagent)` (exit
  1 for `iormesh attach`, a note and no Java attribution for that
  JVM under `run -agent`), and the tracer does not retry. Opt in
  with `-XX:+EnableDynamicAgentLoading` at the JVM's next start,
  or switch to `-javaagent`.
- **Loaded classes are retransformed.** The JDK's I/O classes are
  loaded long before the attach, so the agent rewrites them in
  place: S1 measured ≈90–130 ms for the install on a loaded host
  (≈150–175 ms with the handshake), with the JIT dropping the
  compiled code of the rewritten methods.
- **The install's own I/O is in the trace.** Reading the agent
  jar, extracting its bridge jar into the JVM's temporary
  directory and creating the ring show up as the JVM's events on
  its `Attach Listener` thread, attribution none. A repeated load
  reads the agent jar only: the bridge and ring are there.
- **Users.** The JVM accepts an attacher whose effective uid (and
  gid) are its own, or root. `iormesh` runs as root, so it reaches
  a JVM of any user — but the agent then runs as that user: the
  jar must be readable by it (`/opt/iormesh`, not a mode-0700
  home directory) and, since root loads it into every user's JVM,
  changeable by nobody but root (see "Safety"); the spool (mode
  1733) is writable by all, so the tracer reads a ring only once
  its process is traced (an event of that pid and incarnation,
  its attach or fork event at the latest, has passed the Java
  join). A ring found earlier — the agent loaded at exec through
  `JAVA_TOOL_OPTIONS`, before the session adopted the JVM — waits
  unread and is read from then on; one of a JVM the session never
  traces (it inherited `JAVA_TOOL_OPTIONS` but is no target) is
  never read: no events, no table rows, no probes. Nor is a ring
  whose JVM's start time cannot be read when it is found. Another
  user's `<pid>.ring` in the spool, which the agent cannot delete,
  does not stop it: it creates `<pid>.<hex>.ring` instead (task
  9j4), checked the same way. A ring is at
  most 64 MiB (`ringsize=`) and a record at most 64 KiB
  ([security.md](security.md), "The agent spool is untrusted
  input").
- **Containers.** Paths are resolved inside the JVM's mount
  namespace. For a JVM in another one (an app container beside
  an in-pod tracer, decision #14, provisional), `run -agent`
  stages a copy of the jar and a spool inside the JVM's root:
  `/tmp/iormesh-<random>/` (root's, 0755) holding
  `iormesh-agent.jar` and `spool/` (1733), created through
  descriptors opened from `/proc/<pid>/root`, refused when the
  JVM's `/` or `/tmp` could be swapped by another user (not
  root's or the JVM user's, or writable by others without the
  sticky bit), read through its held descriptor and removed
  when the session ends. A JVM without a writable, trusted
  `/tmp` (an `emptyDir` at `/tmp`, 0777 and not sticky, is not
  trusted), or in another PID namespace than the tracer's, gets
  no Java attribution and a note naming `-javaagent`; a non-root
  JVM in a root-owned 1777 `/tmp` is staged for like any other.
  `iormesh attach` stages nothing — its spool belongs to a
  separate `run` — so for it the jar and spool must exist in the
  JVM's namespace under the same names, and for a JVM in another
  mount namespace it says it could not verify the ring. In a pod
  the tracer needs `SYS_PTRACE`, `DAC_OVERRIDE`, `KILL` and
  `CHECKPOINT_RESTORE` ([security.md](security.md)
  "Kubernetes"). When the app container restarts, the JVM that
  comes back starts after the tracer and gets the agent from `run
  -agent` only when the session follows it — a `-pattern` match,
  a restarted `-listen-port` owner — through the late attach
  ("Live attach" above), staged like any other. A debug-mode tracer does not even outlive the restart:
  `kubectl debug --target` puts it in the app container's PID
  namespace, and the kernel kills it along with that namespace's
  init (task ah4). `iormeshctl deploy` then starts a fresh debug
  tracer, which live-attaches to the JVM running at that point. For
  a pod that restarts, use the sidecar and `-javaagent`
  (decisions.md #14).

## Safety

An attach must never take the target down. SIGQUIT's default
action kills a process, and a JVM whose attach listener already
runs answers it with a thread dump on its stdout, so the tracer:

- refuses a process that maps no `libjvm.so`
  (`not a HotSpot JVM`), and a JVM without a SIGQUIT handler yet
  — still starting; try again once it runs
  (`the JVM has no SIGQUIT handler`). A JVM run with `-Xrs`
  starts its listener at startup and needs no signal: it attaches;
- refuses a JVM with `-XX:+DisableAttachMechanism` on its command
  line or in `JAVA_TOOL_OPTIONS`/`JDK_JAVA_OPTIONS`. The flag in
  an `@argfile` is not seen: such a JVM prints a thread dump and
  the attach times out;
- signals only when the socket is missing, once, through a pidfd
  (a recycled pid is never signalled), and removes its trigger
  file afterwards. A socket a `/tmp` cleaner deleted under a
  running listener gets the signal too — a thread dump, as with
  `jcmd`. A trigger file that is there already is used only if it
  is a regular file of root or the JVM's user; anything else (a
  planted symlink, another user's file) refuses the attach before
  any signal;
- sends the load request only when the connected peer is the
  target process itself: the kernel's `SO_PEERCRED` must name the
  target's pid and effective uid. The socket's path is the
  target's to point anywhere — a symlink to a root JVM's socket
  is refused, whatever the path's owner;
- bounds the whole attach by `-timeout` (default 10 s; `run
  -agent` uses 10 s per JVM, while it keeps reading the BPF
  stream — per try for a late attach, whose tries a JVM still
  starting ends quickly). Giving up never touches the JVM again, but a load the
  JVM already started still completes inside it — a running
  `run -spool` then picks up its ring at the next scan.

The tracer runs as root and the target owns every path of the
handshake, so it also never lets the target steer it into the
host's tree: the JVM's working directory and `/tmp` are opened
as directory fds — `/tmp` with `openat2`'s `RESOLVE_IN_ROOT`,
refusing a `/tmp` that is a symlink — and the trigger file is
created (`O_EXCL|O_NOFOLLOW`), checked and removed relative to
those fds, never re-resolved by path. And it loads only an agent
jar nobody else can change: the jar and every directory above it
must belong to root or the caller and not be writable by group or
others (a sticky directory such as `/tmp` excepted); the JVM gets
the jar's resolved real path.

## Checked JDKs

| JDK | live attach | JEP 451 warning | `-XX:-EnableDynamicAgentLoading` |
|---|---|---|---|
| 17.0.19 | works | none | — |
| 21.0.12 | works | yes | refused, JVM runs on |
| 25.0.4 | works | yes | refused, JVM runs on |
| 27-ea+35 | works | yes | refused, JVM runs on |

25 runs in `mage integrationTest` (`TestJavaLiveAttach`,
`TestJavaLiveAttachAtStart`, `TestJavaAttachDynamicLoading`,
[testing.md](testing.md#live-attach-task-ge4)); 21 and 27-ea
through `IORMESH_ATTACH_JDKS`, 17 by hand (the workload needs
21).
