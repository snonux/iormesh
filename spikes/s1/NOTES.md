# S1 spike notes — raw observations (task wa4)

Throwaway prototype for PLAN.md "Spikes" S1. These are raw notes; the
write-up (`docs/spikes/s1-java-join.md`), clocks, transport (#27),
attach (#2) and cost belong to the follow-up task xa4.

## Setup

- Host: Fedora, kernel 7.2.15, x86_64, bpftrace v0.24.2, root via
  `sudo -n`.
- JDKs run: **25.0.4.1** (Red Hat build, `/usr/lib/jvm/java-25-openjdk`)
  for everything; **27-ea** for the uprobe join only. No JDK 21
  runtime is installed: JDK 21 was checked against the openjdk/jdk21u
  sources (`natives-jdk21-src.txt`); 17 (GraalVM CE) only via `javap`.
- Workload: `workload.jar` (M0, `--release 21`), scenarios file-basic
  and tcp-loopback, platform threads only.

## Files

| File | What |
|------|------|
| `join.bt.in` | bpftrace template: uprobe/uretprobe per-TID mark, syscall log |
| `hooks-all.txt` | discovery hook set: every `Java_*` of libjava/libnio/libnet |
| `hooks-v1.txt` | curated leaf I/O hook set (the S1 candidate) |
| `run.sh` | gated run of one scenario under join.bt (+ optional agent) |
| `analyze.py` | cut log to the marker bracket, list syscalls with marks |
| `agent/` | minimal Java agent (body rewrites) + `build.sh` |
| `agentjoin.py` | join uprobe-marked syscalls to agent records (TID + time) |
| `javap-natives.sh`, `natives-*.txt` | native-method inventory per JDK |

Reproduce:

```
HOOKS=hooks-all.txt ./run.sh /usr/lib/jvm/java-25-openjdk file-basic OUT
./run.sh /usr/lib/jvm/java-25-openjdk tcp-loopback OUT   # hooks-v1
python3 analyze.py OUT/server.log --all --pairs
agent/build.sh /usr/lib/jvm/java-25-openjdk A
JAVA_OPTS="-javaagent:A/agent.jar=A/bridge.jar,{out}" \
    ./run.sh /usr/lib/jvm/java-25-openjdk tcp-loopback OUT
python3 agentjoin.py OUT client
```

## 1. Uprobe TID-mark join

Mechanism: uprobe on entry of a JNI function pushes its name on a
per-TID depth stack, uretprobe pops; `raw_syscalls:sys_enter` of the
target PID logs the innermost mark. Uprobes are attached by absolute
library path (inode), not `-p PID`, so libraries the JVM maps after
attach (libnet, libextnet are loaded lazily) are covered too.

Result with `hooks-v1.txt` (584 probes = 292 functions × 2), 3 runs
each on JDK 25 and 27-ea, identical every run: **every scenario I/O
syscall on the scenario thread carries the right native's mark.**

| Syscall (scenario) | Native mark (JDK 25 and 27-ea) |
|--------------------|-------------------------------|
| openat (+ fstat) FileOutputStream | `Java_java_io_FileOutputStream_open0` |
| write file / stderr | `Java_java_io_FileOutputStream_writeBytes` |
| openat (+ fstat) FileInputStream | `Java_java_io_FileInputStream_open0` |
| read file | `Java_java_io_FileInputStream_readBytes` |
| close file | `Java_java_io_FileDescriptor_close0` |
| unlink (`Files.deleteIfExists`) | `Java_sun_nio_fs_UnixNativeDispatcher_unlink0` |
| statx before unlink | `..._UnixNativeDispatcher_lstat0` / `stat0` |
| socket, setsockopt | `Java_sun_nio_ch_Net_socket0` |
| bind / listen | `Net_bind0` / `Net_listen` |
| getsockname | `Net_localInetAddress`, `Net_localPort` |
| accept (not accept4) | `Java_sun_nio_ch_Net_accept` |
| fcntl (accepted fd) | `Java_sun_nio_ch_IOUtil_configureBlocking` |
| connect | `Java_sun_nio_ch_Net_connect0` |
| socket read / write | `Java_sun_nio_ch_SocketDispatcher_read0` / `write0` |
| getsockopt, shutdown | `Net_getIntOption0`, `Net_shutdown` |
| close socket | `Java_sun_nio_ch_UnixDispatcher_close0` |
| socketpair + close (libnio init) | `Java_sun_nio_ch_UnixDispatcher_init` |
| class loading from the app jar | `Java_java_io_RandomAccessFile_readBytes0`, `seek0` |

Counts of "declared-kind" syscalls — every openat/read/write/close/
connect/accept/unlink on the scenario thread inside the bracket, i.e.
expect.json's events *plus* same-kind JDK work (app-jar class loading,
the marker line on stderr, net.properties, libextnet): file-basic
14 marked / 0 unmarked; tcp-loopback server 27 / 7, client 29 / 7.
The 7 unmarked per tcp process are all the JVM's own `dlopen` of
`libextnet.so` (see §3), none is scenario I/O.

Raw findings:

- **x86_64 syscall 335 is the uretprobe trampoline** (`uretprobe(2)`,
  Linux ≥ 6.11): every uretprobe return enters it on the *target*
  thread and never exits. With this hook set it is the most frequent
  "syscall" of the target. The production tracer must drop nr 335
  (or the arch's equivalent) — it is self-noise.
- **Hooking every `Java_*` is wrong** (`hooks-all.txt` run):
  non-I/O natives that run Java code underneath (`Class.forName0`,
  `ClassLoader.defineClass1`, `findBootstrapClass`) become outer
  brackets, and I/O by unhooked code below them inherits their mark —
  e.g. libextnet's socket()/getsockopt()/close() probes marked
  `Class_forName0`, jimage `pread`s marked `findBootstrapClass`.
  Only leaf I/O natives belong in the set.
- **Leaf brackets still leak JVM work**: the begin marker's failed
  open throws `FileNotFoundException` from inside
  `FileInputStream_open0`; loading that class does `pread` on the
  jimage (fd 3) and `futex` under the open0 mark.
  `Inet6Address_init` marked two `mprotect`s. Fix: a per-hook
  allowlist of syscall kinds (open0 → openat/fstat; writeBytes →
  write; Net_accept → accept/accept4; ...), anything else under the
  mark is "JVM work inside a bracket", not attributed.
- **Late-mapped libraries**: libextnet natives fired (the mark is set)
  but bpftrace printed `func` as a raw address, because it symbolizes
  against the maps it saw at attach. Production should key a mark on a
  per-attach-point id (BPF cookie), never on runtime symbolization.
- **Sticky marks (production concern)**: join.bt's per-TID depth
  stack is never reset. A missed uretprobe (return-probe instance
  limit, a native that leaves by longjmp, probes detached mid-call,
  a TID reused by a new thread) would leave a mark that tags every
  later syscall of that TID. The production tracer needs a reset path:
  clear on thread exit (`sched_process_exit`), cap the depth, and
  sanity-check marks against the per-hook syscall allowlist.
- Other threads inside the bracket: only C1/C2 compiler threads and
  "Monitor Deflation" doing futex/mprotect/mmap — no file or socket
  I/O by JVM threads during these scenarios.

## 2. Java-level hook set (body rewrites only)

Natives have no body, and retransformation cannot add methods (so no
`setNativeMethodPrefix` wrapper). The agent therefore rewrites the
**Java method that calls the native**. Verified against JDK 21 sources
and JDK 25 bytecode; `agent/core/.../HookTable.java`:

| Java method (body rewritten) | calls native |
|------------------------------|--------------|
| `FileOutputStream.open` | `open0` |
| `FileOutputStream.write` (all overloads) | `write`, `writeBytes` |
| `FileInputStream.open` | `open0` |
| `FileInputStream.read` (all overloads) | `read0`, `readBytes` |
| `FileDescriptor.close` | `close0` |
| `RandomAccessFile.readBytes`, `.seek` (+ `writeBytes`, `open`) | `readBytes0`, `seek0`, ... |
| `UnixNativeDispatcher.unlink` (and `open`, `rename`, ...) | `unlink0`, ... |
| `Net.socket`, `Net.serverSocket` | `socket0` |
| `Net.bind`, `Net.connect` | `bind0`, `connect0` |
| `NioSocketImpl.listen` | `Net.listen` — **itself native** |
| `NioSocketImpl.accept` | `Net.accept` — **itself native** |
| `SocketDispatcher.read`, `.write` (+ `readv`, `writev`) | `read0`, `write0`, ... |
| `UnixDispatcher.close` | `close0` |

Corrections to the docs/implementation.md S1 table (for xa4):

- `FileDispatcherImpl` `read/write/pread/pwrite`: since JDK 21 these
  live in `sun.nio.ch.UnixFileDispatcherImpl` (natives `read0`,
  `pread0`, `readv0`, `write0`, `pwrite0`, `writev0`, `seek0`, ...);
  `FileDispatcherImpl` (Linux) keeps only `transferTo0/From0`.
- `Net.accept`, `Net.listen`, `Net.shutdown`, `Net.poll` are native
  methods: they cannot be body-rewritten. Hook their callers
  (`NioSocketImpl.accept/listen`, `ServerSocketChannelImpl.implAccept`
  for NIO channels) instead.
- `FileInputStream.readBytes` / `FileOutputStream.writeBytes` /
  `RandomAccessFile.readBytes0` are natives; hook `read`/`write` and
  RAF's private Java `readBytes`/`writeBytes`.
- Socket close goes `UnixDispatcher.close0` (JDK 21+), not a
  `SocketDispatcher` method.

Release drift (decision #29), from `natives-*.txt`:

- **21 → 25**: no change in any native the hook set uses.
- **17 → 21**: `UnixFileDispatcherImpl` and `UnixDispatcher` do not
  exist in 17 (everything is in `FileDispatcherImpl`); RAF natives
  `readBytes`/`writeBytes` became `readBytes0`/`writeBytes0`;
  `SocketDispatcher` had no `write0`/`writev0` natives in 17.
- **25 → 27-ea**: `IOUtil.configureBlocking(FileDescriptor, boolean)`
  became `(int, boolean)`; `UnixNativeDispatcher.fstatat0` changed
  return type. Hook-set natives used here unchanged.

Agent prototype (`agent/`, JDK 25 only — it uses `java.lang.classfile`,
final in 24; M3 needs shaded ASM for 21):

- `appendToBootstrapClassLoaderSearch(bridge.jar)` alone is not
  enough: java.base does not read the bootstrap loader's unnamed
  module, so rewritten JDK methods fail to link `Hooks`. Fix:
  `Instrumentation.redefineModule(java.base, extraReads={Hooks'
  module}, ...)`.
- Rewrite = `Hooks.enter(id)` at entry, `Hooks.exit(id)` before every
  return and in a catch-all handler that rethrows. Retransformation of
  the already-loaded java.base classes succeeded; `--self-test`
  passes under the agent (all scenarios ok).
- Side effect: appending to the bootstrap search path prints
  `OpenJDK 64-Bit Server VM warning: Sharing is only supported for boot
  loader classes because bootstrap classpath has been appended` on
  **stderr** (CDS for app classes is off). stdout's PID-first contract
  holds.
- Records kept in memory and written in a shutdown hook, so the agent
  adds no I/O inside the bracket. Kernel TID: `readlink
  /proc/thread-self` once per thread (option (b)); this readlink is
  the agent's only per-thread syscall.

## 3. Agent ↔ uprobe join (TID + time)

`agentjoin.py`: a marked syscall belongs to the innermost agent record
on the same kernel TID whose [enter, exit] contains it. It checks each
join mechanically against a hook → native map (`HOOK_NATIVES`): 8
runs (5 + 3 after review), JDK 25: **every expect.json event joined
to its declared method, 0 hook/mark mismatches**.

| Process | expect.json events attributed | Extra scenario ops, also attributed | Declared methods seen |
|---------|-------------------------------|-------------------------------------|-----------------------|
| file-basic | 6/6 (openat, write, close, openat, read, close) | unlink | `FileWork.write:60/61/62`, `FileWork.read:69/70/74`, `FileWork.run:44` (unlink) |
| tcp server | 11/11 (file 6 + accept, read, write, read EOF, close) | unlink, listener close | `TcpLoopback.serve:128/131/132/135`, `expect:148`, `server:103` (listener) |
| tcp client | 10/10 (file 6 + connect, write, read, close) | unlink | `TcpLoopback.client:140/141/143`, `expect:148` |

Non-scenario I/O inside the bracket also joined, to JDK frames: app
jar class loading (`RandomAccessFile` under `URLClassPath$JarLoader`),
the marker line on stderr (`PrintStream.writeln`), and the client's
read of `conf/net.properties` (`ProxySelector.<clinit>`).

Clocks (raw, for xa4): bpftrace `nsecs` is **CLOCK_BOOTTIME**
(`nsecs == nsecs(boot)`), `System.nanoTime()` is CLOCK_MONOTONIC. On
this laptop boot − mono = 307 592.39 s (~85 h suspended). join.bt
samples (boot, mono) in one probe at BEGIN and END; the offset jitters
between samples: BEGIN→END |drift| 1–84 ns within a run, and the
BEGIN offset spread across runs up to ~130 ns here (179 ns in the
review's runs). The real safety argument is the margin, not the
jitter: the smallest distance between a joined syscall and the edge
of its agent window (`agentjoin.py`, "smallest hook-window-to-syscall
gap") was 1.6–3.7 µs in the post-review runs (≥ 2.3 µs in the
review's), i.e. ≥ 10× the offset error. Note the uprobe mark already
gives an exact, clock-free native-level join; a clock-free *agent*
join is possible too (k-th agent bracket of hook H on TID t ↔ k-th
uprobe bracket of its native on t) — untested.

## 4. What stays unbracketed

Observed in these runs:

- **JVM class loading from the runtime image**: `pread`/`mmap` of
  `lib/modules` (fd 3) by libjvm — no JNI frame, or under an
  unrelated native (see the leak above).
- **JVM native-library loading**: loading `libextnet.so` (the JVM's
  ELF check, then the loader's `dlopen`): openat, fstat, read, lseek,
  read, lseek, close, openat, read, fstat, close (+ mmap/mprotect) —
  libjvm and ld.so, not `Java_*`. Same for any `System.loadLibrary`.
- **libextnet feature probes**: right after loading, 5× socket +
  getsockopt + close under libextnet's own natives
  (`LinuxSocketOptions.*Supported0`, marked at native level, printed
  as raw addresses) — JDK-internal, no agent hook.
- **JIT/GC/runtime threads**: futex, mprotect, mmap on compiler and
  "Monitor Deflation" threads.
- **libnio init**: `socketpair` + `close` (`UnixDispatcher.init`) is
  marked at native level but has no agent hook (JDK-internal).

Known by construction (not exercised here):

- **Third-party JNI** (Netty epoll/io_uring transports, RocksDB,
  sqlite-jdbc, ...): their `Java_*` live in other libraries; would
  need their own hook entries per library.
- **FFM downcalls** (`java.lang.foreign`, final in 22): no `Java_*`
  frame at all — unbracketed by both the uprobe and the agent.
- **JVM-internal I/O**: `-Xlog`/warnings (libjvm `write`), hsperfdata
  (mmap), CDS archive mapping, JFR recordings, the attach listener's
  unix socket (`/tmp/.java_pid<pid>`), `jcmd`/JMX.
- **Memory-mapped file I/O** (`MappedByteBuffer`): page faults, no
  syscalls.
- Unhooked JDK libraries: libprefs (`FileSystemPreferences` locks),
  libmanagement, libattach (in the attaching JVM), libsctp,
  libj2pkcs11 — add globs if they matter.
- Virtual threads: deferred (PLAN.md "Deferred — not in v1").

## Not done here (xa4)

Uprobe cost per I/O op, agent cost and stack-walk share (S3's
syscall-burst), transport (#27), attach mode (#2: only `-javaagent`
was used), JDK builds with stripped or static libraries, and running
the agent on a real JDK 21.
