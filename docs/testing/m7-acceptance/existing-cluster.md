# M7 on an existing cluster

Part of [M7 acceptance on kind (task yb4)](../m7-acceptance.md), in
[Testing strategy](../../testing.md).

## M7 on an existing cluster (task zg4)

kind's node runs in a privileged container, so a kind run says
nothing about a real node's SELinux. For a node of the main target
platform — el9, SELinux enforcing — `mage k8sAcceptance` also
targets an existing cluster: set `IORMESH_K8S_KUBECONFIG` to its
kubeconfig (and `IORMESH_K8S_CONTEXT`, else the kubeconfig's
current context). The run then needs no kind, creates no cluster,
leaves kube-proxy's mode alone (k3s embeds kube-proxy; there is no
daemonset to switch) and runs `TestKindAcceptance` and
`TestKindClusterIP` in the cluster's own mode. It still starts the
laptop's registry on `localhost:5001`, builds and pushes both
images, and installs Traefik with helm. The cluster is the
operator's, and a release `traefik` in namespace `traefik` is the
chart's documented install of a cluster's own ingress controller
(task li4). So before touching the cluster the run claims its
namespaces (`kindrun.ClaimExisting`): it refuses when `traefik`,
`iormesh-m7`, `iormesh-m7-clusterip` or `iormesh-m7-micro` is
already there and not recorded as its own, or when it cannot list
them, and otherwise
records the kubeconfig, the context, the Traefik release and the
namespaces in `.k8s-acceptance/existing.state.json` at the
repository root, beside the base-image state. The teardown
removes only what that record names, in the recorded kubeconfig
and context (not the kubeconfig's current one, which may point
elsewhere by then):
it uninstalls the release and deletes the namespaces (Traefik's
CRDs, which helm never removes, stay). Without a record — a run
refused or failed before its claim, a bare `mage k8sTeardown` —
it leaves the cluster alone; when the namespace deletion fails,
the state file stays for the next `mage k8sTeardown`. It removes
the registry and images as the kind path does and keeps the
operator's kubeconfig. A record of another context makes the next
run refuse until that one is torn down; the refused run's own
teardown leaves that record and its objects alone for an explicit
`mage k8sTeardown`. A record naming no kubeconfig or context (a
hand-edited state file) is not acted on in the cluster: the
teardown says so, keeps the state file and fails. The SIGTERM
check reads the tracer's exit through `IORMESH_K8S_CRICTL`, the
argv prefix that runs crictl on the node (`ssh root@<node> k3s
crictl`), and `mage k8sDebugRestart` kills the app container's
init through it (`crictl stop --timeout 0`, a SIGKILL without
grace: the prefix runs nothing but crictl). Without it both
checks skip, and a skip fails the target (task si4), so a run on
an existing cluster needs the prefix. Without
`IORMESH_K8S_KUBECONFIG` nothing changes.

The cluster must pull `localhost:5001/…` under that name: the
images are pinned by the registry's digest, and that reference is
what the pods run. `scripts/k8s/el9-k3s.sh` sets up such a node:
run on it as root, `up` installs a single-node k3s (pinned, see
below) with containerd's SELinux support on, `k3s-selinux` from
the k3s installer, no bundled Traefik, and a containerd mirror of
`localhost:5001` to `http://localhost:5001`, and adds to
firewalld's trusted zone the pod and Service CIDRs it lacks;
`proxy nftables` (or `iptables`) restarts k3s in that kube-proxy
mode for the second pass; `down` uninstalls k3s with its images
and data and removes the `k3s-selinux` package and repository
(and `container-selinux` and the firewalld sources only if `up`
added them); `status` shows SELinux, k3s and the disk. `down`
refuses to run without the state file `up` writes
(`/root/.iormesh-k3s-state`), so it cannot wipe a k3s node it did
not set up (task ni4).

The script pins k3s v1.35.10+k3s1 and the sha256 of that tag's
`install.sh` (`8598e002…6d91`) instead of piping get.k3s.io into a
root shell; the installer checks the k3s binary against the
release's `sha256sum` file. To bump, set `K3S_VERSION` in the
script to the new tag and `K3S_INSTALLER_SHA256` to the output of
`curl -sfL https://raw.githubusercontent.com/k3s-io/k3s/<tag>/install.sh
| sha256sum`, the tag's `+` written as `%2B`. From the laptop, one
tunnel carries both directions:

```
scp root@node:/etc/rancher/k3s/k3s.yaml "$scratch/k3s.yaml"
ssh -N -L 6443:127.0.0.1:6443 -R 5001:127.0.0.1:5001 root@node &
IORMESH_K8S_KUBECONFIG="$scratch/k3s.yaml" \
  IORMESH_K8S_CRICTL='ssh root@node k3s crictl' mage k8sAcceptance
```

k3s's kubeconfig names `https://127.0.0.1:6443`, which the `-L`
forward serves; the `-R` forward gives the node the laptop's
registry. Trace data stays on the laptop and the node. Delete the
copied kubeconfig after the run.

The harness option and the script are unit-tested
(`TestTeardownExisting`, `TestExistingConfig`, `TestCrictlPrefix`,
the claim and record in `TestClaimExistingRefusesTaken`,
`TestClaimExistingUnreachable`, `TestClaimExistingRecords`,
`TestTeardownExistingWithoutRecord`,
`TestTeardownExistingKeepsRecord`, `TestRefusedRunLeavesForeignRecord`
and `TestTeardownRecordWithoutContext`; the script with shellcheck,
and its `down` against recording fakes in `TestDownRefusesWithoutState`
and `TestDownFirewalld`).


### Run on Rocky 9.9 (task zg4, 2026-11-21)

One VM (2 vCPU, 4 GB for the run, 20 GB disk): Rocky 9.9, el9
5.14.0-741.22.1, SELinux enforcing, k3s v1.35.10+k3s1 with
containerd 2.2.10, container-selinux 2.250 and k3s-selinux 1.6,
firewalld active; the laptop built and pushed the images.
**M7 does not pass as shipped** (milestones.md M7, reopened):

| Criterion | #13 spec as shipped | With the diagnostic module |
|---|---|---|
| tracer spec (#13's capabilities, `RuntimeDefault`, mounts, image digest) | spec stored as asked, tracer dies on load | PASS |
| BPF load under SELinux | FAIL: `avc: denied { prog_load }` `container_t` `tclass=bpf` | PASS, no AVC |
| tracefs hostPath | not reached (denied in permissive: `tracefs_t` `file { open read }`) | PASS |
| BTF from the container's sysfs | not reached | PASS, nothing needed |
| `clusterip` (fused, goldens), iptables | FAIL (no trace) | PASS |
| `clusterip`, nftables (`el9-k3s.sh proxy nftables`) | not run | PASS |
| `hairpin` | FAIL (no trace) | PASS (server saw 10.42.0.1) |
| `identity` (5 pods, one `boot_id`) | FAIL | PASS |
| debug container with `-agent` (Java live-attach) | FAIL | PASS |
| `ingress` routed edge to the inventory pod | FAIL (sidecar never Ready) | PASS |
| `rbac` (list-only discovery) | not reached | PASS |
| `sigterm` (crictl over ssh) | not reached | PASS: exit 0, 624 ms |

The first column is the run as shipped: the sidecar's rollout timed
out (the tracer crash-looped on `load BPF object: permission
denied`), the debug tracers ended `session failed` and lingered,
and libbpf's message blames `RLIMIT_MEMLOCK` (task 6h4). `ausearch
-m avc` printed no match; the raw `/var/log/audit/audit.log` held
the denial. Six probe pods, one tracer each with `-duration 15s`,
isolated the cause:

| Probe pod | Result |
|---|---|
| #13 as shipped | EPERM |
| `seccompProfile: Unconfined` | EPERM |
| `+SYS_ADMIN` | EPERM |
| `seLinuxOptions.type: spc_t` (#13's capabilities, `RuntimeDefault`) | attached |
| `spc_t` and Unconfined | attached |
| `privileged: true` | attached |

`container_t` permissive (a one-line CIL module, removed again)
logged the rest of what the tracer does: `perf_event { open cpu
kernel read write }` and `tracefs_t` `file { open read }`. The
diagnostic module that made the second column pass, loaded with
`semodule -i` and removed after:

```
(allow container_t self (bpf (map_create map_read map_write prog_load prog_run)))
(allow container_t self (perf_event (open cpu kernel read write tracepoint)))
(allow container_t tracefs_t (dir (search read open getattr)))
(allow container_t tracefs_t (file (open read getattr)))
```

It grants every container on the node BPF and is no deployment
answer; decision #13 records the choices (task 5h4). Two node
findings, fixed in the script: el9's firewalld rejected
forwarded pod traffic (`reject with icmpx admin-prohibited`, so
Traefik's pod got "Host is unreachable" to the backend and the
gateway a 502), so `up` trusts the pod and Service CIDRs at
runtime; k3s's metrics-server never became Ready and its stale
API stalled every namespace deletion, so `up` disables it. The
ingress pass came after both fixes; ClusterIP passed before them.
After the run the cluster, the module, the packages, the
registry and the images were removed: the laptop's `docker
images -a` and volumes matched the state before, and the VM had
the 371 packages and the `/opt/iormesh` it started with.


### Load refusal hint (task 6h4)

Since that run a BPF load refused with EPERM or EACCES no longer
leaves libbpf's `RLIMIT_MEMLOCK` line as the only clue: the
tracer's `load BPF object` error names the likelier cause
(`internal/bpf/lsmhint.go`). SELinux enforcing with the process
in a confined domain (not `spc_t`, `unconfined_t`, `kernel_t`)
names the context from `/proc/self/attr/current`, decision #13's
el9 profile and the raw audit.log; otherwise an active AppArmor,
BPF LSM or lockdown (`/sys/kernel/security/lsm`); the memlock
hint stays only for a low limit on a kernel before 5.11, else
the message says libbpf's hint does not apply. The error, and
whether the tracer fails, is otherwise unchanged. Unit-tested
with injected file reads and limit (`TestLoadHint`: enforcing
`container_t`, enforcing `spc_t`, permissive, no LSM, low and
high memlock on old and new kernels; `TestLoadHintContextIsSafe`,
`TestWithLoadHint`), each branch mutation-checked.
