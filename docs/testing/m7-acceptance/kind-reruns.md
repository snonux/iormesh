# kind reruns

Part of [M7 acceptance on kind (task yb4)](../m7-acceptance.md), in
[Testing strategy](../../testing.md).

## Debug tracer replacement on kind (task ah4, 2026-11-21)

`mage k8sDebugRestart` runs `TestKindDebugRestart`
(`integrationtests/kind/restart_test.go`) on the laptop's kind
cluster. The setup, images and teardown are `mage
k8sAcceptance`'s, and the VMs were not used. The test uses the
tcp scenario's prepared `server` Deployment, whose app container
runs `sleep infinity`, in namespace `iormesh-ah4`. Its registry
entry is a debug tracer with the Java agent (`-spool`, `-agent`).
The steps:

1. A gated `file-basic` JVM is started, then `iormeshctl deploy`.
   The agent joins the JVM, which is released and ends; the test
   then waits 75 s so the session publishes a segment.
2. The app container's init is SIGKILLed from the kind node, its
   pid taken from `crictl inspect` (on an existing cluster, since
   task si4, by `crictl stop --timeout 0` through
   `IORMESH_K8S_CRICTL`).
3. The ephemeral `iormesh` must end. A collect must fail with
   "session ended" and point to `iormeshctl deploy`.
4. Another gated JVM is started, then `deploy` again. While that
   deploy waits for the attach and `iormesh-2` already runs, a
   second `deploy` reruns (N1).
5. The agent joins the new JVM; after the run and another 75 s,
   `collect` runs.

Result: passed (181 s).

- `iormesh` ended with `Error, exit code 137` as soon as the app
  container's init died. The restart count read at that moment
  was still 0; the test then waited for the pod to be `Ready`
  again with the restarted container.
- The collect at that point failed with the deploy advice.
- The replacing deploy took 10.9 s and printed "earlier tracer
  ended (its unsealed segment is lost, its published ones stay
  for collect); started tracer iormesh-2, … attached, session
  11870b83". The agent joined the JVM running then.
- The N1 rerun started 1.7 s in, once `iormesh-2` ran and 9.2 s
  before the replacing deploy returned. So its attach probes
  read the killed tracer's `dead` `state.json`. It left
  `iormesh-2` alone and reported "tracer iormesh-2 already
  running the pinned image, attached, session 11870b83", the
  same session. Before the N1 fix it would have failed at once
  with "died without its end".
- The final collect, which execs into `iormesh-2`, brought 4
  segment files of the killed session `869f05bb` and 4 of
  `11870b83` into the one `pod-<uid>` directory.

Teardown: the cluster, the registry and its volume, the run's two
images and the two base images it pulled (`kindest/node`,
`registry`) were removed. The disk guard passed with 23 GiB free
(minimum 20). Docker was started for the run and stopped after.
`docker images -a`, `docker volume ls` and `docker ps -a` matched
the state before. The `kind` network predates the run (created
11:37 that day) and was left alone.


## Rerun with pod fields (task 6k4, 2026-11-26)

zg4's procedure at 26da303, the first existing-cluster run since
the pod fields (task sh4) and with `TestKindMicroservices`. iom1
(2 vCPU, raised to 4 GB for the run and set back to 2 GB after,
the config diffed identical): Rocky 9.9, el9 5.14.0-741.22.1,
SELinux enforcing, firewalld active; `el9-k3s.sh up` installed
k3s v1.35.10+k3s1 (containerd 2.2.10-k3s1, systemd cgroup
driver, cgroup v2) with container-selinux 2.250 and k3s-selinux
1.6; Traefik v3.7.19 from the harness. The tracer as shipped
(`spc_t` at `s0`), no node-local policy (`semodule
--list-modules=full`: only the packaged `container` and `k3s`
at priority 200). The laptop built and pushed the images. `mage
k8sAcceptance` with `IORMESH_K8S_KUBECONFIG` and
`IORMESH_K8S_CRICTL='ssh root@iom1 k3s crictl'`, once per
kube-proxy mode (`el9-k3s.sh proxy nftables` between). **All
green:**

| Test, subtest | iptables | nftables |
|---|---|---|
| `TestKindAcceptance` | PASS (178 s) | PASS (138 s) |
| `clusterip`, `hairpin`, `ingress`, `rbac` | PASS | PASS |
| `identity` | PASS: 5 pods, 6 process ids, one `boot_id` | PASS, the same |
| `sigterm` | PASS: exit 0, 754 ms | PASS: exit 0, 848 ms |
| `TestKindMicroservices` | PASS (123 s) | PASS (124 s) |
| `mesh` | PASS: 4 hosts, 4 sessions, `tcp 4 fused, 0 unresolved` | PASS, the same |
| `pod_fields` | PASS | PASS |
| `identity` | PASS: 4 pods, one `boot_id` | PASS |
| `TestKindClusterIP` | PASS (94 s) | PASS (95 s) |

The raw `/var/log/audit/audit.log` and its four rotations, read
after each pass (they rotate every few minutes under the BPF
audit records, so each read covered its whole pass), held no
`AVC`, `USER_AVC` or `SELINUX_ERR`.

**Real cgroup files.** During both passes a sampler on the node
read, every 3 s, `/proc/<pid>/cgroup` of each process under
`kubepods` as the node sees it and, for each `iormesh` tracer,
of every process of its pod through `nsenter -C -t <tracer>`
(the tracer's private cgroup namespace), mapping ids to
containers and sandboxes with `crictl ps -a` and `crictl pods`.
They replace task sh4's constructed k3s shapes in
`internal/k8s/container_test.go` (`TestContainerOfShapes`,
`TestParseProcCgroupK3sCaptures`):

| Pod (namespace `iormesh-m7`) | Captured |
|---|---|
| `inventory`, burstable, sidecar tracer (`shareProcessNamespace`) | node view of the tracer, the app (its `sleep` and the JVM) and the sandbox's `pause`; the tracer's view of the app and of `pause` (`0::/../cri-containerd-<id>.scope`) and of itself (`0::/`) |
| `gateway`, best-effort, debug tracer (`kubectl debug --target app`) | node view of the tracer, the app and `pause`; the tracer's view of the app |
| `inventory` again (nftables pass) | the app's view of itself (`0::/`) and of the tracer (`0::/../cri-containerd-<tracer>.scope`) |

Every shape is the systemd driver's
`/kubepods.slice/kubepods-<qos>.slice/kubepods-<qos>-pod<uid_>.slice/cri-containerd-<id>.scope`,
the one kind showed without kind's `kubelet` root: the parser
read each one as the constructed fixtures expected, and needed
no change. Two things the constructed set had wrong. No process
sat in the pod slice itself (cgroup v2 keeps processes out of
an inner cgroup): the sandbox's `pause` has a scope of its own,
so `ContainerOf` names the sandbox id for it, an id no container
status lists — a sidecar tracer sees `pause` as pid 1, but
`pause` does no traced I/O. And `pause` shares the node's
cgroup namespace, so from inside it the full path shows. k3s
on el9 runs the systemd driver; the cgroupfs cases stay as
paths built from the formats.

After the run `el9-k3s.sh down` (state file present) removed
k3s, the two SELinux packages it added, the repository and the
two trusted firewalld sources: `rpm -qa` matched the 372
packages before, `/opt/iormesh` matched (paths, sizes, mtimes,
modes), the sampler and its files were removed. The laptop's
`docker images -a`, volumes, containers and networks matched
the state before; Docker was stopped, the tunnel closed, the
kubeconfig deleted. iom1 was stopped and its config restored;
`doas vm list` on f3 afterwards:

```
iom1       default    uefi       2    2G      -               No       Stopped
iom2       default    uefi       2    2G      -               No       Stopped
iom3       default    uefi       2    2G      -               No       Stopped
```
