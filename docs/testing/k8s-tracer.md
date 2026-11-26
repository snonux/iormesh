# Kubernetes: capabilities, PID namespaces, transport

Part of [Testing strategy](../testing.md).

## K8s capability set (task ub4)

decisions.md #13 (provisional) names the capabilities the
tracer needs without `privileged`. They were checked on the host,
not in a cluster (2026-11-20, Fedora kernel 7.2.15, SELinux off,
JDK 25.0.4, libbpfgo v0.9.2): a container's root is uid 0 with a
reduced capability set, which `setpriv` reproduces by shrinking
the bounding set before exec. The tracer and the agent jar were
installed root-owned in a root-owned directory (the jar trust
rule; a binary under a 0700 home is not even executable without
`DAC_OVERRIDE`), and a fresh JVM running `Files.readAllBytes` in a
loop as an ordinary user was traced with live attach:

```
sudo -n setpriv --bounding-set=-all,+bpf,+perfmon,+sys_resource,\
+sys_ptrace,+dac_override,+kill,+checkpoint_restore --inh-caps=-all -- \
  /run/iormesh-x/iormesh run -pid JVM -project x -out OUT -duration 6s \
  -spool SPOOL -agent /run/iormesh-x/iormesh-agent.jar
```

With that set the run matched an unrestricted root run: the
agent loaded, `java agent of pid N joined, 37 natives probed`, and
≈310 of ≈360 brackets attributed in both. Each capability left
out alone (one fresh JVM per run):

| Left out | What broke |
|---|---|
| `BPF` | libbpf's probe load: `Couldn't load trivial BPF program` |
| `PERFMON` | loading the first tracing program (`handle_inet_sock_set_state`): EPERM |
| `SYS_RESOURCE` | `load embedded module: error setting rlimit` — libbpfgo's 512 MiB memlock bump; the same run under `prlimit --memlock=unlimited` traces fine without it |
| `SYS_PTRACE` | kernel tracing works; attach fails: `JVM root: permission denied` (`/proc/<pid>/root`) |
| `DAC_OVERRIDE` | kernel tracing works; attach fails: `connect .java_pid<N>: permission denied` (the JVM's 0600 socket) |
| `KILL` | kernel tracing works; attach fails: `SIGQUIT: operation not permitted` |
| `CHECKPOINT_RESTORE` | the agent loads, but `java mark: none of the 37 natives found` and nothing is attributed (`/proc/<pid>/map_files`) |

Kernel-only tracing (`run -pid` of a shell loop, no `-spool`)
needs `BPF`, `PERFMON` and `SYS_RESOURCE` — or just the first two
with the memlock limit raised. Not covered here, and left for the
kind run: tracefs and BTF inside a container, the runtime's
seccomp and AppArmor defaults, and a non-root tracer uid. PID
namespaces are covered on the host (task 4f4, below).


## PID namespaces (task 4f4)

An in-pod tracer numbers every process as its PID namespace does
(decisions.md #13, docs/implementation.md M7 step 1). Checked on the
host, without a cluster, with `unshare --pid --fork --mount-proc`
(a new PID namespace and a `/proc` mounted for it — what a pod's
container has):

- `TestPidNSTracerConfined` (in `mage bpfRootTest`): the test
  binary re-runs itself as such a tracer. Inside, a forking bash
  is traced by its namespace pid and its forked child follows by
  the namespace pid its `$!` reports; a bash in a namespace nested
  one level deeper (a second `unshare`) is traced by its number in
  the tracer's namespace, its child followed; and the tracer is
  told to target the host pid of a host bash that keeps opening
  the probe file outside — no record of it may appear; nor of a
  bash in a *sibling* namespace (a second `unshare --pid` beside
  the tracer's) that it is told to target by its number there.
  Only the expected children may open the probe file, and no
  record may carry pid 0. With the namespace switch forced off
  (the tracer reading host pids, as before task 4f4) the three
  positive checks fail; with the namespace-inode check at the
  tracer's level removed, the sibling check fails (its loop is
  traced as pid 203).
- The root test binary run whole inside a namespace
  (`sudo -n unshare --pid --fork --mount-proc ./iormesh-internal.test
  -test.run …`): the loader, follow, exec-watch, native-stack,
  signal, Java-mark (uprobe_multi attached by the JVM's namespace
  pid) and stream-sequence tests pass. The `TestSock*` tests do
  not apply: they load the object without the loader, so it stays
  in host mode while their pids are the namespace's.
- On the host, the same tests — and the integration suite — are
  the check that host mode did not change: the walk is pruned at
  load there.

The tracer refuses a namespace whose `/proc` is the host's
(`/proc/self` naming another pid than its own), since every
`/proc/<pid>` read would then name another process.


## K8s transport (task wb4)

The controller's kubectl transport (`internal/k8s`, decisions.md #12,
#18; docs/implementation.md M7 step 9) is tested without a
cluster: `fake_test.go` puts a fake `kubectl` first on `PATH`, a
shell script that logs its argv (NUL-separated, one record per
call) and serves canned output — pod JSON for `get`, a tar for
`exec … iormesh ready`, a copy of the `--custom` file for `debug`.
The real `execRunner` runs it, so the tests see exactly the argv
kubectl would get. They run with the package's unit tests
(`CGO_ENABLED=0 go test ./internal/k8s/`, and in `mage test`):

- status: a failed remote command (stderr ending "command
  terminated with exit code N", N kubectl's status — also behind
  10 KB of stderr) is `*exec.ExitError`; kubectl failing on its
  own, before or after a complete stream, is `ErrUnreachable`
  without one (both fail when `explain` stops telling them
  apart);
- argv: `--context=` and `--namespace=` on every call, `exec POD
  --container=iormesh -- argv…` with shell metacharacters arriving
  as single, verbatim elements; `debug POD --container=iormesh
  --target=APP --image=<digest> --attach=false --profile=general
  --custom=FILE -- argv…` (`sysadmin` for the privileged profile),
  and the `--custom` spec's security context and two mounts;
- refusals before any kubectl runs: an unregistered or malformed
  context, an image not pinned by `@sha256:<64 hex>` (none is
  refused by `Debug` and `Deploy` alone, since task af4 — exec and
  fetch need no image; the built-in default is empty until task
  yb4 publishes one),
  namespace, pod or container names that are not Kubernetes names
  (`$(id)`, `x;touch …`, `--all-namespaces`), a remote dir other
  than `/opt/iormesh/data/traces/<project>/ready`, and `Push`;
- refusals after the `get` alone: a pod without the `tracefs`
  hostPath or a size-bounded `iormesh-traces` emptyDir, with
  `hostUsers: false`, without the app container, or already
  carrying an `iormesh` ephemeral container; a sidecar target
  without its container or `shareProcessNamespace`;
- the fetch stream: `WriteReady` sends only regular `*.parquet`
  files of a `ready/` planted with a symlink, a FIFO, a
  subdirectory, a dot file and a text file; `Unpack` refuses
  traversal, directory parts, absolute and dot names, links,
  devices, directories, oversize and duplicate entries, and leaves
  no partial file from a truncated stream, nor more than the
  per-fetch total; `WriteReady` notes an oversize file on stderr;
  `Fetch` refuses more than 64 KiB after the tar's end and removes
  on the tracer exactly the files that arrived, also when the
  stream is refused part-way; through an injected file interface
  (`ready_sync_test.go`, task ii4), `Unpack` fsyncs each temporary
  before its rename and the directory after the last, and a failed
  sync keeps `Fetch` from removing what is not durable (nothing at
  all for the directory sync); `TestReadyRoundTrip`
  (internal/cli) runs `iormesh ready` and `-remove` around
  `Unpack`.

What only kind can show is checked by task yb4's run
([below](m7-acceptance.md#m7-acceptance-on-kind-task-yb4)).
