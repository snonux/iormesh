# M7 steps 1–5: the tracer in a pod

Part of [M7 — Kubernetes](../m7.md), in [Implementation
guide](../../implementation.md).

1. **PID-namespace-aware targeting** (#13) — built by task 4f4.
   Before it `target_map` was keyed by the kernel's tgid and
   `/proc` read as the host's, so a tracer inside a pod's PID
   namespace targeted nothing. Now (`internal/c/pidns.c`,
   `internal/bpf/pidns.go`):
   - The tracer stats `/proc/self/ns/pid`. The initial namespace
     has a fixed inode (`PROC_PID_INIT_INO`, `0xEFFFFFFC`): there
     rodata `IORMESH_PIDNS_INUM` stays 0 and every helper is the
     host read it replaced, pruned by the verifier at load — host
     sessions emit the same records as before. Anywhere else the
     inode goes to rodata, and `/proc` must be that namespace's
     (`/proc/self` must name the tracer's own pid), or `Open`
     refuses: a host procfs would make every `/proc/<pid>` another
     process.
   - The BPF side numbers every task by its pid *at the tracer's
     level*: `pids[PIDTYPE_TGID]->numbers[level]` (and
     `thread_pid` for the tid), as `ior_current_namespace_tgid`
     (internal/c/filter.c) walks levels, accepted only if that
     level's namespace inode is the tracer's. A task above or
     beside the namespace gets 0 — filter()'s "no" — and one in
     a namespace nested below it keeps its number at the
     tracer's level. `bpf_get_ns_current_pid_tgid` would have
     matched only a task's innermost namespace.
   - The level is not knowable from userspace (`NS_GET_PARENT`
     stops at the caller's own namespace), so the kernel learns
     it into a global: the sys_enter dispatcher searches each
     task's levels until one matches (the tracer's own first
     syscall at the latest), and any hook meeting a task whose
     innermost namespace is the tracer's takes its level, for
     sessions without syscall probes.
   - Everything that keys per-thread state or fills a record
     uses those numbers: `filter()`, the signal, restart, Java
     mark and exit hooks. Two need more: the classic
     task_newtask tracepoint names the child only by its host
     tid, so a namespace tracer attaches the raw tracepoint
     instead (`handle_task_newtask_ns`), whose arguments hand over
     the child's task_struct — the classic hook stays for host
     mode, which no per-program guard skips (dispatch.c), and a
     run the raw hook's guard skips is counted as skipped runs;
     and a non-leader exec's old tid — a host number in
     `sched_process_exec` — comes from `exec_tid_map`, written at
     the exec's sys_enter by the dispatcher (not for a 32-bit
     task's execve, whose syscalls are not traced, so it has no
     per-tid state for the old tid to find).
   - For the current task, `bpf_get_ns_current_pid_tgid` (with
     the namespace file's device and inode) answers first: it
     fails only for tasks not exactly in the tracer's namespace,
     and only those pay the walk. `filter()` — every traced
     kind's enter and exit of every task on the host — goes
     through a one-slot per-CPU cache keyed by the task pointer
     and its start time (`ns_ids_cache`; the start time also
     changes when a non-leader exec renumbers the task), so a
     task's back-to-back calls pay two helper calls and a
     compare. Measured on a `close(-1)` loop, kernel
     instructions per untraced call beside the tracer (S3
     follow-up 4f4): host mode +1.0 k as before; in the namespace
     +1.4 k with the walk and helper alone, +1.1 k with the
     cache; a process outside the namespace +1.6 k without the
     cache, +0.9 k with it. That left decision #25's bystander
     item open in namespace mode (1.25× / 1.30×); task 9f4 closed
     it: a target-hint bitmap rules most pids out before the
     `target_map` lookup (`targethint.c`), and the dispatcher
     answers the scope test before the tail call, behind a mirror
     of its program array so untraced kinds skip the test. +0.83 k
     per call in host mode, +0.91 k in the namespace; 1.23× /
     1.23× (s3-overhead.md, "Follow-up (9f4)").
   - Both run in global BPF functions (`iormesh_ns_ids`,
     `iormesh_ns_current_ids`): the verifier checks each once
     per program and the callers see an unknown scalar. Inlined,
     their early returns split the verification of each handler
     and the load took about six times host mode's kernel CPU.
     Each opens with the host-mode guard, so in host mode the
     verifier prunes their bodies too.
   - Records carry the namespace's pids, not host tgids beside
     them: an in-pod tracer could not resolve a host pid in its
     `/proc`, the cross-host join keys on ISN pairs (S2), not
     pids, and the pod comes from the cgroup or the downward API
     (step 6). A signal sender outside the namespace reads as a
     kernel-raised signal (no `sender_pid`). `AttachJavaMarks`
     takes the namespace pid: uprobe_multi looks it up in the
     caller's namespace, and the agent names its ring and
     threads by the same numbers — for a JVM in the tracer's own
     namespace; one nested deeper is not joined.
   Tests: `TestPidNSTracerConfined` (internal/pidns_root_test.go,
   in `mage bpfRootTest`) re-runs the test binary as a tracer in
   `unshare --pid --fork --mount-proc`, traces a forking bash
   there and one in a nested namespace, and is told to target a
   host process's host pid and a sibling namespace's process
   by its number there, neither of which it may trace (the
   sibling case fails when the namespace-inode check is
   removed). The
   loader, follow, exec-watch, Java-mark and stream tests also
   pass when the whole root test binary runs in such a namespace
   ([testing.md](../../testing/k8s-tracer.md#pid-namespaces-task-4f4)).
2. **Memlock without `CAP_SYS_RESOURCE`** (#13). libbpfgo
   v0.9.2's `NewModuleFromBuffer` sets RLIMIT_MEMLOCK to 512 MiB
   before every load, which fails without the capability. Load
   through `NewModuleFromFileArgs{SkipMemlockBump: true}` from a
   memfd of the embedded object (libbpf ≥ 1.0 bumps only on
   kernels without memcg accounting), or patch libbpfgo; then
   drop `SYS_RESOURCE` from the set, decisions.md #13 and the docs.
3. **Java staging inside the JVM's root** (#14). The jar and
   spool paths are resolved in the JVM's mount namespace, and an
   ephemeral container cannot give the app container a volume.
   Built by task cf4 (`internal/javaattach/stage.go`,
   `internal/cli/stage.go`). For a JVM in another mount
   namespace (`sameMountNS` false), `run -agent` stages before
   the load (`javaattach.Attacher.Stage`):
   - `/proc/<pid>` is held as an fd and its `maps`, `status`,
     `ns/pid` and `root` opened relative to it, so a reused pid
     fails (`ESRCH`) instead of being read as the JVM;
   - a process that maps no `libjvm.so` gets `ErrNotJVM` and
     nothing is created;
   - a JVM in another PID namespace than the tracer's gets
     `ErrOtherPidNS`: its agent names the ring by its own pid,
     which the tracer would resolve as another process (a host
     tracer reading a container JVM's `1.ring` as host pid 1);
   - the JVM's root is opened through `/proc/<pid>/root`, its
     `/tmp` with `openat2`'s `RESOLVE_IN_ROOT` and no symlinks
     (the fd discipline of the handshake, `openTmp`);
   - the jar's trust rule is applied inside that root: `/` and
     `/tmp` must be root's or the JVM user's and not writable
     by group or others unless sticky, or another user could
     rename the stage away and put their own jar at the path
     the JVM loads (`ErrUntrustedTmp`). An `emptyDir` mounted at
     `/tmp` (0777, not sticky) is refused for that reason; a
     non-root JVM in a root-owned 1777 `/tmp` is staged for like
     any other (its ring is its user's); in a user-namespaced pod
     (`hostUsers: false`, which #13 excludes) the container's root
     is a high host uid, so its `/tmp` reads as a third user's
     and is refused;
   - `mkdirat` of `iormesh-<16 hex>` (crypto/rand), 0700 first;
     a name already taken (the JVM's user may have planted it)
     is never adopted — the next fresh name is tried, eight in
     all; then the directory is opened `O_NOFOLLOW`, checked to
     be the tracer's, and `fchmod`ed to 0755;
   - the jar (the tracer's trusted copy) is copied in with
     `O_CREAT|O_EXCL|O_NOFOLLOW`, then 0644;
   - `agentspool.MakeSpoolAt` makes `spool` beside it, again
     exclusively, with MakeSpool's owner check and an exact
     1733 by `fchmod`, and returns it open.
   The load gets `/tmp/<name>/iormesh-agent.jar` and
   `spool=/tmp/<name>/spool`; the Java join scans the spool as
   `/proc/self/fd/<n>` beside `-spool` (an `agentspool.Dir` per
   stage, with Dir's ring checks unchanged), so nothing in the
   JVM's tree is resolved by name again. Every check runs for
   every JVM; a JVM shares an earlier JVM's stage only with the
   same euid and the same `/tmp` directory object (device and
   inode as opened in its root), not merely the same mount
   namespace, whose processes may run as other users or chroot
   apart (review of cf4, B1). The session removes every
   stage when it ends, through the held fds — rings, stray
   entries (unlinked, never followed), spool, jar, directory; a
   JVM still running keeps writing into its unlinked ring. A JVM
   whose `/tmp` is missing, read-only or untrusted gets no Java
   attribution and a note naming `-javaagent`. `iormesh attach`
   stages nothing: its spool belongs to a separate `run`, which
   would never read a stage it made, so a JVM in another mount
   namespace must see attach's paths as they are. Tests:
   `internal/javaattach/stage_test.go` (a hostile fake root:
   planted names as a directory, a symlink to the host's tree and
   a file; `/tmp` a symlink, missing, read-only, not sticky,
   group-writable or a third user's; `/` world-writable; a
   planted jar file or symlink; removal past a planted symlink;
   another PID namespace; a pid gone; stage reuse by the same
   user and `/tmp` only, a chrooted JVM getting its own and
   another user in the first user's `/tmp` refused; a stage
   directory swapped for another user's), `agentspool`
   `TestMakeSpoolAt`, `internal/cli/stage_test.go` (who stages,
   sharing as the stager decides, notes, removal at close, the
   ring check), and `TestJavaAttachMountNS` in `mage
   integrationTest` (each http-plain JVM in its own mount
   namespace with a private tmpfs `/tmp`, the server as nobody,
   the client as root).
4. **Java mark without `CAP_CHECKPOINT_RESTORE`** (optional,
   #13). `openJavaLibs` opens mapped libraries through
   `/proc/<pid>/map_files`, which needs the capability. Open
   them through `/proc/<pid>/root/<path>` with
   `RESOLVE_IN_ROOT` instead, as it already does for unmapped
   ones, and keep the file only if its device and inode equal
   the mapping's in `/proc/<pid>/maps` — the same file, proved
   another way; then drop the capability.
5. **Prepared pods** (#13, #18). An ephemeral container can
   mount only volumes the pod already has, and the tracer needs
   two: tracefs (hostPath `/sys/kernel/tracing`, read-only —
   mounting it needs `SYS_ADMIN`, and without it no classic
   tracepoint attaches: `errNothingAttached`) and a size-bounded
   `emptyDir` for traces. Raw or BTF tracepoints would need no
   tracefs, but task 8e4 measured them skipping runs
   (internal/c/dispatch.c), so they are not the way out.
   `deploy/prepare-patch.yaml` adds just the two volumes (one
   rollout, no container); iormeshctl checks a pod's spec for
   them before `kubectl debug` and refuses an unprepared pod,
   naming the patch. Generated (task vb4): `mage k8sManifests`
   writes `deploy/` from `internal/k8s/manifest.go`, whose
   golden test fails on any drift; the patch also sets
   `hostUsers: true`, which is pod-level; its volumes are the
   ones `Kubectl.Check` (step 9) looks for, by the same
   constants.
