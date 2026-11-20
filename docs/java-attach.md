# Attaching the Java agent

The Java agent (`iormesh-agent.jar`, PLAN.md "M3 — Java agent")
names the Java method, stack and thread behind each syscall of a
JVM. It gets into a JVM in one of two ways (decision #2):

- **`-javaagent` at launch — the primary path.** The JVM starts
  with `-javaagent:/opt/iormesh/iormesh-agent.jar=spool=DIR` and
  `iormesh run -spool DIR` joins its I/O from the first syscall
  on. Silent, nothing missed.
- **Live attach to a running JVM** — for "trace this PID now"
  without a restart, at the cost of the caveats below.

## Live attach

```
# load the agent into the JVMs the session starts with
iormesh run -pid 4242 -project shop -out /var/tmp/trace \
    -spool /var/lib/iormesh/spool -agent /opt/iormesh/iormesh-agent.jar

# or load it later, into a JVM a `run -spool` is already tracing
iormesh attach -pid 4242 -agent /opt/iormesh/iormesh-agent.jar \
    -spool /var/lib/iormesh/spool [-timeout 10s]
```

`run -agent` attaches to the processes the session starts with —
the `-pid` list, the `-listen-port` owners, the JVMs a `-pattern`
finds running at start — before its `-ready` file appears; a
process that is no JVM is noted and traced without Java
attribution. Children forked later and pattern matches exec'd
later are not attached: their JVM is still starting (see
"Safety"); give those `-javaagent`, e.g. through
`JAVA_TOOL_OPTIONS`. `iormesh attach` returns once the JVM
confirmed the agent started; the running tracer then prints
`java agent of pid N joined, K natives probed` at its next spool
scan (within ≈200 ms) — from there on the JVM's I/O is
attributed.

The tracer needs no JDK: it speaks HotSpot's attach protocol
itself (`internal/javaattach`, the same handshake `jcmd` and
`VirtualMachine.attach` use). It creates the trigger file
`.attach_pid<pid>` in the JVM's working directory (or its
`/tmp`), sends one SIGQUIT, waits for the JVM's socket
`/tmp/.java_pid<pid>` (reached through `/proc/<pid>/root`) and
sends the load request over it. That is a client connection to
the JVM's own socket: the tracer still listens on nothing.

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
  WARNING: A Java agent has been loaded dynamically (/opt/iormesh/iormesh-agent.jar)
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
  its `Attach Listener` thread, attribution none.
- **Users.** The JVM accepts an attacher whose effective uid (and
  gid) are its own, or root. `iormesh` runs as root, so it reaches
  a JVM of any user — but the agent then runs as that user: the
  jar must be readable by it (`/opt/iormesh`, not a mode-0700
  home directory) and, since root loads it into every user's JVM,
  changeable by nobody but root (see "Safety"); the spool (mode
  1733) is writable by all.
- **Containers.** Paths are resolved inside the JVM's mount
  namespace: the jar and the spool must exist there under the
  same names. Attaching into containers is M7's (decision #14).

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
  stream). Giving up never touches the JVM again, but a load the
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
