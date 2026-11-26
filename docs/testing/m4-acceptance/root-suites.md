# Root suites on Rocky 9.9 (task nf4, 2026-11-21)

Part of [M4 acceptance on two real hosts (task lb4)](../m4-acceptance.md),
in [Testing strategy](../../testing.md).

`mage bpfRootTest`'s test binary and the integration test binary
ran as root on `iom1` (the host above). Both were built on the
7.2 laptop with `mage`'s cgo flags plus `-tags netgo,osusergo
-ldflags '-w -extldflags "-static"'` (as `ReleaseTracer` links
the tracer: Fedora's glibc is newer than el9's 2.34), and the
integration inputs (static tracer, `iormeshctl`, the four jars,
`workload/{scenarios,src,libs,otel}`, `edgepeer`, `echopeer`)
were staged under `/opt/nf4`, run serially, and removed again.

**bpfRootTest: 27 of 27 pass** (first run: 17 of 24;
`TestProcessExitGroupDead` is new, the two `sockreuse` tests came
with if4). They cover the paths no earlier el9 run had taken:

- **Sock tests**: all seven `TestSock*` failed to attach
  — `failed to create BPF link for perf_event: -13 (Permission
  denied)` on `syscalls:sys_enter_connect` and friends. The tests
  attached the generated handlers to their own per-syscall
  tracepoints, but since hf4 the handlers read the raw_syscalls
  record, which on el9 is longer (`args` at 24, while
  `sys_enter_connect` ends at 40), and the kernel refuses a
  program reading past its tracepoint's record. Only the tests
  did that; the loader tail-calls the handlers from the
  `raw_syscalls` dispatchers. The tests now fill the dispatch
  slots and attach the dispatchers as the loader does (the same
  fix landed with if4 while this ran).
- **Java mark (`TestJavaMarkNamesTheNative`)**: passes. It now
  also requires a mark from a thread whose tid is not the pid (a
  JVM's `main` never runs on the launcher thread): el9's
  uprobe_multi filters by process, so commit 46ba0e49 is
  backported.
- **PID namespace (`TestPidNSTracerConfined`)**: passes
  (`handle_task_newtask_ns` and the namespace dispatch).
- **Follow and fork, process exit**: `TestFollowForkExec` and the
  exec-watch tests pass. el9's `sched_process_exit` has no
  `group_dead` (comm at 12, pid at 28, prio at 32), so
  `ior_exit_group_dead` takes the `signal->live` fallback. The
  new `TestProcessExitGroupDead` runs it: a Go child ends one
  locked thread (record `group_dead` 0, the process still in
  `target_map`), then exits (a `group_dead` 1 record, the entry
  gone). In ten runs every exit had exactly one `group_dead` 1
  record, on the last thread. On 7.2 the same test runs the
  field path.
- **Target hint**: the harness assumes `pid_max` ≤
  `PID_MAX_LIMIT` (el9: 4194304, the limit), BPF_XADD (present)
  and that the fallback may report a dead group twice, which
  `iormesh_forget_target`'s single successful delete absorbs; no
  duplicate was seen on el9.

**Syscall formats**: el9's `events/syscalls/*/format` against
the 7.2 formats `mage generate` reads. The handlers read every
argument by index from the raw_syscalls record (CO-RE, hf4), so
per-syscall offsets do not matter; the argument order does, and
it matches for every syscall both kernels have. Differences: 7.2
appends `__data_loc` copies of string arguments (never read);
`bpf` gained a fourth argument `size_common` (the handler reads
only `cmd`, args[0]); `getcpu`, `name_to_handle_at` and `semop`
differ in a pointer or integer type name only. 16 syscalls (32
tracepoints) are generated but absent on el9 (`file_getattr`,
`file_setattr`, `getxattrat`, `setxattrat`, `listxattrat`,
`removexattrat`, `listmount`, `statmount`, `listns`, the three
`lsm_*`, `mseal`, `open_tree_attr`, `uprobe`, `uretprobe`); the
loader skips their slots (`tracepointExists`) and reports "0
tracepoints missing" for the default families.

**Integration suite: 51 of 51 passed**, 44 in the first full run and
the other seven after the staging and golden fixes below. That
serial run (about 10 minutes; 2 GiB was enough) failed seven:

- `TestHTTPServlet`, `TestHTTPApache`, `TestHTTPOkHttp`,
  `TestHTTPNetty`, `TestOtelHTTP`: staging — the third-party jars
  under `workload/libs` and `workload/otel` were not copied.
- `TestSystemdUnit`: staging — under SELinux (enforcing on el9)
  systemd may not exec a binary in `/root` (`admin_home_t`,
  status 203/EXEC), and `iormesh start` said only "exit status
  1". Passes from `/opt`. The bare error is task rf4.

Task rf4 made a failed start name its cause. systemd-run
(`--quiet --service-type=exec`) exits 1 with nothing on stderr,
and `--collect` has already dropped the unit, so start now
appends the unit's journal since the launch (`journalctl
--since=@<launch> --unit=…`, falling back to `systemctl status
--lines=0`), at most 20 lines of 300 bytes and 4 KiB, oldest
lines dropped first, each line through `termsafe`; systemd-run's
own stderr is bounded the same way. Task 8g4 tightened it: the
300 bytes are counted after the quoting (a line of control bytes
quotes to four bytes each) and the cut keeps whole runes; each
diagnostic command has a 5 s timeout (`TestUnitLogTimeouts`);
and the status fallback shows the unit's state without journal
lines, which for a unit that survived `--collect` are most
likely a racing start's (the fake systemctl adds such a line
unless given `--lines=0`). Mutation-checked: `--lines=20`, no
timeout, cutting before quoting and cutting inside a rune each
fail a test. When SELinux enforces and the binary's
type is not `bin_t` or `usr_t`, or the log shows a permission or
AVC denial, a hint names the label and points to `/usr/local/bin`
or `/opt`. The hint does not wait for an AVC record: el9 does not
audit this denial (`ausearch -m avc` shows none). The unit tests
(`internal/cli/startdiag_test.go`) put shell stand-ins for
systemd-run, systemctl and journalctl first on `PATH`, so the
real exec path (argv, stderr capture) runs: the el9 journal and
label, the status fallback, the hint's cases, and a 200-line log
with escape sequences in it and in systemd-run's stderr. On iom1
(enforcing), a start from `/root/rf4` now reports:

```
iormesh: systemd-run --unit=iormesh-rf4probe.service … -- /root/rf4/iormesh run …: exit status 1: (no stderr)
iormesh-rf4probe.service logged:
  … iormesh-rf4probe.service: Failed to locate executable /root/rf4/iormesh: Permission denied
  … iormesh-rf4probe.service: Main process exited, code=exited, status=203/EXEC
  …
SELinux is enforcing and /root/rf4/iormesh is labelled admin_home_t: systemd may not exec a binary outside a bin_t or usr_t path (e.g. under /root, admin_home_t); install it under /usr/local/bin or /opt (restorecon relabels it)
```
- `TestPipeIPC` (fixed): el9's glibc 2.34 implements `pipe()` as
  `pipe(2)` on x86_64, Fedora's 2.43 as `pipe2(fds, 0)`; the
  tracer recorded `pipe` with both ends, the golden declared
  `pipe2`. The test now declares the kind the trace's libc issued
  (`pipeKindOfThisLibc`); both ends and every edge are still
  checked.

A second full run, on the tree rebased onto if4, kf4 and jf4,
passed 50 of 51: `TestJavaLiveAttach` failed its golden, a call
after the live attach left unattributed (`ambiguous 14`). Alone
it failed 1 of 8 serial runs on the idle host. 7.2 showed the
same symptom once under load; task tf4. The root suite on that
tree: 27 of 27.

Task tf4 found the cause in the tracer, not the test. The test
already waited for the tracer's `java agent of pid N joined`
line, but that line could lie. The joiner probed a JVM's natives
at the first drain that saw any HOOK record. The agent writes
its hook table into the ring one row at a time, and a drain that
landed mid-table probed only the first rows' natives. Every
failing run printed `joined, 8|15|16|21 natives probed`, every
passing run 37, and the later hooks' I/O (`unlink0`, `open0`)
stayed `none`. The joiner now probes only after a record that
follows the table (the agent's warm-up THREAD and WATERMARK)
shows the table read whole
(`TestJavaJoinWaitsForWholeTable`). `TestJavaLiveAttach`
serially, before → after:

| Host | Before | After |
|---|---|---|
| 7.2 laptop, load ≈ 12 (other workers' suites) | 3 of 110 failed | 0 of 100 failed |
| Rocky 9.9 `iom1`, idle | 1 of 40 failed | 0 of 40 failed |

Outside the suites: a hand-run pattern session over pipe-ipc
(`-pattern '*workload.jar*pipe-ipc*' -trace-families ipc`) died
with "fd_gen 1 without an fd". That is task mf4 again, this time
without `su`; the `-pid` run of the same scenario passes.

**mf4, fixed**: the rejected event was the spawned child's
`newfstat` on fd -1. el9's NSS lookups `fstat(-1)` (EBADF) in the
JVM and again in the child it spawns through jspawnhelper (su and
bash do NSS lookups too). The correlator registered fd -1 as an
fd instance. The exec closed it but kept its generation, so the
child's call got gen 1. A negative fd now names no instance: the
event keeps fd -1 and gen 0. The tracer is wrong on any kernel
for this sequence. Only el9's libc makes it, so 7.2 never
showed it. `TestNegativeFdAcrossExec` (root) replays it with a Go
child on 7.2 and fails without the fix; correlate's
`TestNegativeFdNamesNoInstance` holds the record shape. On iom1
the pipe-ipc pattern session (three runs) and lb4's
`su paul -c java … tcp-loopback` session (`-out` and
`-trace-dir`) now end `stop`. The old binary still dies on the
su run. A writer's error now names the event it rejects.
