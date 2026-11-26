# SELinux reruns

Part of [M7 acceptance on kind (task yb4)](../m7-acceptance.md), in
[Testing strategy](../../testing.md).

## Re-run with `spc_t` (task 5h4, 2026-11-21)

The same VM and versions (4 GB for the run), the tracer container
as shipped by task 5h4 — #13's seven capabilities,
`RuntimeDefault`, `allowPrivilegeEscalation: false` and
`seLinuxOptions: {type: spc_t, level: s0}` — and **no node-local
policy**: `semodule --list-modules=full` listed only the packaged
`container` and `k3s` modules (priority 200), nothing at the
local priority 400. **M7 passes on el9** (milestones.md M7):

| Criterion | iptables | nftables |
|---|---|---|
| tracer spec as stored (`spc_t`/`s0` on the tracer only, pod `securityContext` empty, apps without `seLinuxOptions`) | PASS | PASS |
| BPF load under SELinux, tracefs hostPath, BTF | PASS, no AVC | PASS, no AVC |
| `clusterip` (fused, goldens), both tests | PASS | PASS |
| `hairpin` | PASS (server saw 10.42.0.1) | PASS (server saw 10.42.0.1) |
| `identity` (5 pods, one `boot_id`) | PASS | PASS |
| debug container with `-agent` (Java live-attach, container_t JVM to spc_t tracer through the emptyDir spool, ring and attach socket) | PASS | PASS |
| `ingress` routed edge to the inventory pod (the sidecar tracer) | PASS | PASS |
| `rbac` (list-only discovery) | PASS | PASS |
| `sigterm` (crictl over ssh) | PASS: exit 0, 810 ms | PASS: exit 0, 715 ms |

`kubectl debug --custom` (kubectl 1.35) took the field: every
debug container was stored with it. On the node (`ps -eZ`) the
tracers ran as `system_u:system_r:spc_t:s0`, the app containers
as `container_t` with their pods' own MCS pairs. The raw
`/var/log/audit/audit.log` and its rotations, read by
timestamp (they cover the whole session), held no AVC during
either pass — `ausearch` is not trusted here (task zg4).

The level is part of the fix. The first pass ran with
`{type: spc_t}` alone: `TestKindClusterIP` passed, but
`TestKindAcceptance` failed in the ingress subtest, the gateway's
JVM getting `java.net.ConnectException`; the audit log held
`avc: denied { read }` for `java` (`container_t:s0:c19,c249`)
on `resolv.conf` and `etc-hosts`, labelled
`container_file_t:s0:c292,c345` — the debug tracer's own pair.
Given a type without a level, containerd gives the container a
fresh MCS pair and relabels the pod's shared files (and the trace
`emptyDir`) to it, locking the app out. With `level: s0` those
files (and the `emptyDir`) were `container_file_t:s0`, readable
and writable from every pair, and both passes went green.
After the run `el9-k3s.sh down` left the VM with the same 372
packages (`rpm -qa` identical), the same `/opt/iormesh` (paths,
sizes, mtimes, modes) and no trusted firewalld source; the
laptop's `docker images -a` and volumes matched the state before.
The kind path (`mage k8sAcceptance` without
`IORMESH_K8S_KUBECONFIG`) passed with the field present too:
`TestKindAcceptance` all six subtests (119 s, SIGTERM exit 0 in
702 ms) and `TestKindClusterIP` (104 s, nftables); the kind node
has no active LSM, so there the field is inert. Docker was
started for the runs and stopped after; images and volumes
matched the state before.


## App container restart (task 8h4, 2026-11-21)

Does an app container restarting mid-session, which relabels the
pod's shared files back to the pod's MCS pair, break the tracer or
the spool? The same VM and versions (4 GB), the tracer as shipped
(`spc_t` at `s0`), no node-local policy, and a minimal
reproduction instead of the suite: the tcp scenario's `server`
Deployment (app container `sleep infinity`, prepare patch) with a
debug tracer carrying the Java agent (the acceptance test's
`-spool`/`-agent` flags), and the ingress scenario's `inventory`
shape with the sidecar patch (`run -pattern '*workload.jar*'`).
In each pod a gated `file-basic` JVM ran and then held; the app
container's init (`sleep`) was then killed with SIGKILL from the
node, and the kubelet restarted the container (restart count 1).

**SELinux: no break.** The raw `/var/log/audit/audit.log`, which
covered the whole session, held no `AVC`, `USER_AVC` or
`SELINUX_ERR`. `ls -Z` on the node:

| File | Before the tracer | Tracer running | After the app restart |
|---|---|---|---|
| `etc-hosts`, sandbox `resolv.conf` and `hostname` | `s0:c557,c864` (the pod's) | `s0` | `s0:c557,c864` |
| trace `emptyDir`, `spool/`, `traces/` | — | `s0` | `s0` |

The restarted container relabels the files it mounts back to the
pod's pair, as expected; the `emptyDir`, which only the tracer
mounts, stays at `s0`. The `spc_t` tracer reads either label.
In the sidecar pod the app container starts after the tracer, so
there those files are at the pod's pair from the start.

**Debug mode: the tracer dies with the app container — not
SELinux.** `kubectl debug --target` joins the app container's PID
namespace; when its init died the kernel killed every process in
it, the tracer included: the ephemeral container ended `exitCode
137` at the same instant, and an ephemeral container is never
restarted. The unsealed segment (`.active-*`, 0 bytes) was lost;
`iormeshctl collect` said "session ended: tracer container
terminated (Error, exit code 137) … stranded until the pod goes",
and `iormeshctl deploy` refused a second tracer ("ephemeral
containers cannot be replaced: restart the pod"). Before the kill
the agent was live-loaded into the JVM (`java agent of pid 41
joined, 37 natives probed`). A JVM started after the tracer, as a
restarted app's always is, gets no agent from `run -agent` — as
[java-attach.md](../../java-attach.md) documents; it needs `-javaagent`.
Task ah4 takes the debug-mode finding: milestones.md M7 now recommends
the sidecar for pods that restart, and `iormeshctl deploy` replaces
an ended debug tracer with `iormesh-2` (up to `iormesh-8`), whose
`iormesh ready` serves the stranded sealed segments. This is checked
against the fake kubectl (the "Debug tracers after an app container
restart" item above) and on kind, in the next section.

**Sidecar mode: no break.** With `shareProcessNamespace` the pause
container is the PID namespace's init, so the tracer kept running
(restart count 0) through the app restart; the pattern re-attached
to the new JVM, and the collected segment held both incarnations
— pid 27 before the restart and pid 64 after, 644 and 647 events,
8 `file-basic` file events each — plus one `drop` record
(`skipped_runs`, 1 lost) at the restart. After the run `el9-k3s.sh
down` left the same 372 packages and the same `/opt/iormesh`; iom1
was stopped and set back to 2 GB; the laptop's images, volumes and
containers matched the state before, Docker was stopped and the
copied kubeconfig deleted.


## Dedicated type `iormesh_container_t` (task 7h4, 2026-11-21)

The opt-in policy module (decisions.md #13, `packaging/selinux/`) on the
same VM and versions (4 GB): Rocky 9.9, container-selinux 2.250,
selinux-policy 38.1.86, k3s v1.35.10+k3s1. The RPM came from `mage
selinuxRPM podman` on the laptop and was installed with `dnf`; its
`%post` loaded the module at priority 400 beside the packaged
`container` and `k3s` ones, and a `dnf remove` (its `%postun`)
unloaded it again. (Since the policy review the spec installs and
removes at priority 200, `semodule -X 200`, leaving 400 to local
overrides; task gh4 installed that RPM, below.) These runs relied
on container-selinux's `virt_sandbox_use_all_caps` boolean, on by
default, for the tracer's capabilities; it was not turned off.
Task gh4's module grants them itself and passed with it off.
Two harness variables run the acceptance with
another type and level: `IORMESH_K8S_SELINUX_TYPE` and
`IORMESH_K8S_SELINUX_LEVEL` (default `spc_t` and `s0`). They go
into the registry's `k8s:` and the sidecar patch, the spec check
expects them, and a level other than `s0` is also pinned on every
workload pod (`securityContext.seLinuxOptions.level`), so the
tracer runs at its pod's pair as #13 requires
(`TestSELinuxOptions`). Every run used `iormesh_container_t` at
`s0:c100,c200`.

| Run | Module | Result |
|---|---|---|
| 1, iptables, enforcing | zg4's four rules only | `TestKindClusterIP` PASS; `TestKindAcceptance` FAIL: the gateway's tracer attached, but the agent never joined, and no AVC was logged |
| 2, iptables, the type permissive, dontaudit off (`semodule -DB`) | the same | PASS; the review below |
| 3, iptables, enforcing | zg4's rules plus the Java attach's five | PASS, no AVC |
| 4, nftables, enforcing | the same | PASS, no AVC |
| 5, nftables, module removed, default `spc_t` at `s0` | none (only the packaged `container` and `k3s` modules) | PASS, no AVC: tracers `spc_t:s0`, apps `container_t` at their pods' random pairs, SIGTERM exit 0 in 587 ms |

Runs 3 and 4 in detail:

| Criterion | iptables | nftables |
|---|---|---|
| tracer spec as stored (`iormesh_container_t` at `s0:c100,c200` on the tracer only, apps without `seLinuxOptions`) | PASS | PASS |
| BPF load, tracefs hostPath, BTF | PASS, no AVC | PASS, no AVC |
| `clusterip` (fused, goldens), both tests | PASS | PASS |
| `hairpin` | PASS | PASS |
| `identity` (5 pods, one `boot_id`) | PASS | PASS |
| debug container with `-agent` (Java live-attach) | PASS | PASS |
| `ingress` routed edge (the sidecar tracer) | PASS | PASS |
| `rbac` (list-only discovery) | PASS | PASS |
| `sigterm` (crictl over ssh) | PASS: exit 0, 719 ms | PASS: exit 0, 616 ms |

On the node (`ps -eZ`, sampled every 10 s) the tracers ran as
`system_u:system_r:iormesh_container_t:s0:c100,c200`, the app
containers as `container_t` at the same pair, and the cluster's own
pods (CoreDNS, Traefik, local-path) at their own pairs. The raw
`/var/log/audit/audit.log` and its rotation, read by timestamp, held
no `AVC`, `USER_AVC` or `SELINUX_ERR` during runs 3 and 4.

Run 1's silence is container-selinux's: it dontaudits a container
domain's `search` of another domain's `/proc/<pid>` directory, so
the tracer's first step towards the JVM failed without a record.
Run 2 made the type permissive with a one-line CIL module
(`(typepermissive iormesh_container_t)`) and rebuilt the policy
without dontaudit rules; the audit2allow review of its AVCs (read
by hand — the VM has no `audit2allow`, and installing it would have
changed its packages) found, for `scontext=iormesh_container_t`,
only targets of type `container_t` at the pod's pair, the JVMs of
the app containers:

| Denied | Object |
|---|---|
| `dir { search getattr }` | `/proc/1`, `/proc/7` |
| `file { open read getattr }` | `/proc/1/cmdline`; `read` also without a path, the ptrace read check `/proc/<pid>/root` takes |
| `lnk_file { read }` | `/proc/<pid>/exe` |
| `process { signal }` | the SIGQUIT to the JVM |
| `unix_stream_socket { connectto }` | `/tmp/.java_pid7`, the attach socket |

Nothing else appeared for the type — no further file, network or
capability need. zg4's diagnostic module granted `container_t`
itself, which owns these objects, so it never met them. The five
rules were added as `allow iormesh_container_t container_t:…` with
exactly these permissions; the permissive module was removed and
the dontaudit rules restored (`semodule -B`) before runs 3 to 5.
Not run: the type at `s0` (MCS refuses the Java attach there,
decisions.md #13), a multi-node cluster, a pod without a pinned level.

**Without the boolean (task gh4, 2026-11-21).** The module now
grants the tracer's seven capabilities itself
(`self:capability { dac_override kill sys_ptrace sys_resource }`,
`self:capability2 { bpf perfmon checkpoint_restore }`). Same VM
(4 GB), same versions; the RPM from `mage selinuxRPM podman`.

- *Install at priority 200:* `dnf install` put the module beside
  the packaged ones, `semodule -lfull`: `200 container`,
  `200 iormesh`, `200 k3s`.
- *MCS:* `seinfo -a mcs_constrained_type -x | grep
  iormesh_container_t` printed `iormesh_container_t` (setools
  installed for this and removed again). `sesearch` showed
  `capability2 { bpf checkpoint_restore perfmon }` for the type
  without a condition, where `container_t` has them only under
  `[ virt_sandbox_use_all_caps ]:True`.
- *The acceptance,* iptables, `iormesh_container_t` at
  `s0:c100,c200` as above:

| Run | Boolean (`setsebool`, not `-P`) | Result |
|---|---|---|
| 1 | off | FAIL, not the tracer: the app containers' JVMs (`container_t`, root) were denied `capability { dac_override }` opening `workload.jar` (2 AVCs), so no scenario started; the tracers attached |
| 2 | off, plus a temporary local CIL module at priority 400, `(allow container_t self (capability (dac_override)))` | PASS: `TestKindAcceptance` (all six subtests, SIGTERM exit 0 in 658 ms) and `TestKindClusterIP`, no AVC |
| 3 | on, the CIL module removed | PASS, both tests (SIGTERM exit 0 in 721 ms), no AVC |

`ps -eZ` showed the tracers as `iormesh_container_t:s0:c100,c200`
and the JVMs as `container_t` at the same pair. The raw
`/var/log/audit/audit.log` and its rotations, read by timestamp,
held no `AVC`, `USER_AVC` or `SELINUX_ERR` during runs 2 and 3.
Run 1 shows that the boolean is still the app containers' matter:
a node owner who turns it off must give their own containers what
they need.

- *Cross-pair containment:* one pod (`shareProcessNamespace`)
  with its level pinned at `s0:c300,c400`: the app container ran a
  gated `file-basic` JVM as `container_t:s0:c300,c400`, the tracer
  container `run -pattern '*workload.jar*' -spool … -agent …` as
  `iormesh_container_t:s0:c100,c200`, dontaudit rules off
  (`semodule -DB`) so the denials are logged. The tracer loaded
  its programs ("attached, tracing pattern"), but the pattern never
  matched the JVM, and `kubectl exec … iormesh attach -pid 8`
  (the JVM's pid in the pod) failed "no such process". The audit
  log:

  ```
  avc:  denied  { search } for  pid=27986 comm="iormesh" name="8"
  dev="proc" ino=154534
  scontext=system_u:system_r:iormesh_container_t:s0:c100,c200
  tcontext=system_u:system_r:container_t:s0:c300,c400 tclass=dir
  permissive=0
  ```

  and the same `search` denial for the pause process (`/proc/1`)
  and for the processes `kubectl exec` started in the pod. MCS
  stops the attach at its first step; the signal and socket are
  never reached. The pod was deleted and `semodule -B` restored
  the dontaudit rules.
- *Removal:* `dnf remove iormesh-selinux` unloaded it at priority
  200 ("Removing last iormesh module"); `semodule -lfull` listed
  no `iormesh` and `seinfo -t iormesh_container_t` no type.

After the run the boolean was on, `el9-k3s.sh down` left the same
372 packages and the same `/opt/iormesh`, iom1 was stopped and set
back to 2 GB, the laptop's Docker images matched the state before,
Docker was stopped, the tunnel closed and the kubeconfig deleted.
