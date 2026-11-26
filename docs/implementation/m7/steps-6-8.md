# M7 steps 6–8: cgroups, pod names, the image

Part of [M7 — Kubernetes](../m7.md), in [Implementation
guide](../../implementation.md).

6. `internal/k8s/cgroup.go`: parse `/proc/<pid>/cgroup` → pod
   UID + container id (cgroup v1 and v2 path forms; unit-test
   against captured strings from kind). Under a private cgroup
   namespace (cgroup v2's default) an in-pod tracer sees `0::/`;
   it then takes its pod from the downward API (`POD_NAME`,
   `POD_NAMESPACE`, `POD_UID` in its env) and the controller
   maps processes to containers — settle how on kind. The same
   step makes process ids pod-unique: since step 1 pids are the
   pod's, so `proc:<host_id>/<pid>@<inc>` with the node's
   `host_id` could collide across two pods of one node.
   Built (task vb4), wired into records by task sh4 (below):
   `k8s.ParseProcCgroup` and `ParseCgroupPath` return pod UID,
   container id, runtime and QoS class for the systemd and
   cgroupfs drivers (containerd, CRI-O, cri-dockerd; kind's
   `/kubelet` cgroup root; static pods' hash UIDs), `ErrNotInPod`
   for `0::/`, non-pod cgroups and anything under `user.slice`
   or a `user@<uid>.service` (a user may name cgroups at will
   there), and an error for ids no kubelet writes or a pod slice
   not under its systemd parent chain. In the tracer's own
   private cgroup namespace a sibling container reads
   `/../<container>`: the container id comes out, the pod is the
   tracer's (`k8s.PodFromEnv`, from the downward API both
   generated container specs set). A static pod's UID is not its
   mirror pod's `metadata.uid`; since task 7f4 no tracer runs in
   a static pod (its UID is refused at start, and iormeshctl
   refuses the pod up front), so the join through the mirror
   pod's `kubernetes.io/config.mirror` annotation is needed only
   if a node-level tracer seeing other pods ever comes. The
   test strings follow the formats; kind's real files (task yb4:
   kind 0.31, containerd 2.2.0, systemd driver, cgroup v2) are in
   `TestParseProcCgroupKindCaptures` — from the host, from the
   node, the tracer's own, and the tracer seen from the app's
   private namespace (`0::/../cri-containerd-<id>.scope`).
   **Pod fields on events** (task sh4; milestones.md M7): the
   correlator stamps each event's `pod` object at enqueue —
   namespace and name from `Config.Pod`, the container id from
   the event's process, read once per process through
   `Config.ContainerOf` (the run's reader of
   `/proc/<pid>/cgroup`, `k8s.ContainerOf`: a container of
   another pod, the pod cgroup and the tracer's own `0::/` give
   none) and inherited by a forked child. The Java join copies a
   JVM's `pod` from its joined events onto its http and dns
   events. `TestContainerOfShapes` holds the reader to kind's
   and k3s on el9's captures (task 6k4,
   `TestParseProcCgroupK3sCaptures`: sidecar and debug pods, the
   tracer, the app and the sandbox's pause) and to cgroupfs
   paths.
   `iormeshctl build -namespace NS` keeps the sessions whose
   pinned pod FQDN names NS (`federate.Segments.InNamespace`),
   narrows the service database's views to those pods' host
   ids (`servicedb.Filter.Hosts`), and writes to
   `<projects>@<NS>/`; `show`, `open` and `tui -namespace NS`
   read it (task hk4).
   **The pod is the host** (task 7f4, an advisor's provisional
   choice; review B2): in a pod `iormesh run` takes its host id
   from the downward API, never from `/etc/machine-id` — the
   `scratch` image has none, a baked one would be the same in
   every pod, and the node's would need a third `hostPath`.
   `k8s.PodFromEnv` reads the three variables; `PodRef.HostID`
   is `pod-<uid>`, `PodRef.FQDN` `<name>.<namespace>.<uid[:8]>`
   (no resolver lookup) — through `k8s.PodHostID` and `PodFQDN`,
   the helpers the controller pins each pod's store directory
   with (step 10), and checked by the same `k8sname` rules, so
   tracer and controller cannot disagree on case or suffix —
   and `cli.describeHost` uses them instead
   of the machine-id and hostname, even when the image carries a
   machine-id. Because one tracer runs per pod (milestones.md M7;
   `Kubectl.Check` refuses a debug session in a pod with a
   debug container or a sidecar already), the pod is one PID
   namespace, so `proc:pod-<uid>/<pid>@<inc>` is unique without
   a PID-namespace key. Pods are grouped by node through the
   session's `boot_id`. None of the three set: a host run,
   machine-id as before. Some set, or one malformed (UID not
   the API server's form — a static pod's hash UID too, since
   the controller would pin its mirror pod's UID instead; name
   not an RFC 1123 subdomain, namespace not a label, FQDN over
   `k8s.MaxFQDN` = 190 bytes, so every segment name stays within
   the 249 bytes `segmentName` takes and its unpack temporary
   `.<name>.part` within 255): `ErrPodEnv` (naming the variables
   to unset), and `run` refuses at its flags — a silent
   fallback would stamp an identity that is not the pod's. The
   controller refuses such a pod up front (`checkPod`: `Check`
   and `Deploy` validate the pod's API name, namespace and UID
   the same way, `ErrName`), and `Deploy` in debug mode refuses
   a pod with an `iormesh` sidecar as `Check` does. Tests:
   `internal/k8s/podidentity_test.go` (validation, and two pods
   with the same pid and start tick written as segments, checked
   against the controller's segment-name pattern, loaded under
   the controller's pins and merged into two process nodes) and
   `internal/cli/hostinfo_test.go`. On kind (task yb4,
   `integrationtests/kind`): six pods on one node, each its own
   `pod-<uid>` with its FQDN, one `boot_id`.
   The project (decision #21): `iormesh run` without `-project`
   in a pod (all three downward-API variables set) takes the
   namespace (`k8s.ProjectForNamespace`); `start`, a systemd
   unit, does not. The registry's `pods:` entries are to default
   the same way, through it (task wb4).
7. Pod names: resolved on the controller through the API server
   (`kubectl get pods -o json`), joined by pod UID — the tracer
   never talks to the API.
8. The image and its security context (#13): the tracer image
   (static binary and agent jar, no shell needed), pinned by
   digest. The container spec, shared by the sidecar patch and
   the debug container's `--custom` file:

   ```yaml
   # pod: hostUsers: true (user-namespaced caps do not reach BPF)
   securityContext:
     runAsUser: 0
     allowPrivilegeEscalation: false     # dropped for privileged
     seccompProfile: {type: RuntimeDefault}  # Localhost, then
                                             # Unconfined: fallbacks
     capabilities:
       drop: [ALL]
       add: [BPF, PERFMON, SYS_RESOURCE,          # kernel tracing
             SYS_PTRACE, DAC_OVERRIDE, KILL,      # Java attribution
             CHECKPOINT_RESTORE]
   volumeMounts:
     - {name: tracefs, mountPath: /sys/kernel/tracing, readOnly: true}
     - {name: iormesh-traces, mountPath: /opt/iormesh/data}  # traces/ in it
   # pod volumes (step 5's prepare patch): tracefs = hostPath
   # /sys/kernel/tracing (Directory), iormesh-traces = emptyDir
   # with a sizeLimit
   ```

   `deploy/sidecar-patch.yaml`: the prepare patch plus that
   container and `shareProcessNamespace: true`. The debug path,
   on a prepared pod: `kubectl debug --target=<container>
   --image=<digest> --custom=<that file>`; the `privileged`
   profile swaps the file for `--profile=sysadmin` (and drops
   `allowPrivilegeEscalation: false`, which the API refuses
   beside `privileged: true`). Check on kind that `--custom`
   carries the capabilities and the volume mounts, and pin the
   oldest containerd and CRI-O whose `RuntimeDefault` lets
   `bpf` and `perf_event_open` through on `CAP_BPF` and
   `CAP_PERFMON` (believed containerd 1.6, CRI-O 1.23).
   Generated (task vb4), run by M7's kind and el9 acceptance
   (tasks yb4, 5h4): one container spec,
   `customSpec` (internal/k8s/debug.go), is what `Kubectl.Debug`
   hands `--custom`, what `deploy/debug-profile.json` holds for a
   hand-run `kubectl debug` (JSON, which every kubectl with
   `--custom` reads), and the sidecar's container: the security
   context, the mounts and the downward-API env — name, image and
   command stay kubectl's flags. Since task 5h4 the minimal
   profile's security context also sets `seLinuxOptions: {type:
   spc_t, level: s0}` on this container (never the pod; `iormeshctl deploy
   -selinux-type` or the registry's `selinux_type` picks another,
   `-selinux-level` or `selinux_level` the level (task 8h4),
   empty omits it; decisions.md #13; the opt-in `iormesh_container_t`
   type ships as a policy RPM, `packaging/selinux/`, task 7h4). The
   sidecar adds resource limits
   of one CPU and 1 GiB (start's unit defaults: the container is
   the unit) beside small requests (50m, 128 MiB), and runs
   `iormesh run` as its process, with example args
   (`k8s.DefaultSidecarArgs`) the owner replaces by the app's
   target; either target races the app (`-listen-port` resolves
   at start and only then follows the port, `-pattern` sees only
   later execs).
   - **Trace dir** (review B1): the kubelet makes an emptyDir's
     root 0777 (setgid too under `fsGroup`), which the segment
     writer's trust check refuses, and must. So the volume mounts
     at `k8s.TracesMount` (`/opt/iormesh/data`) and the tracer
     writes to `k8s.TraceRoot` (`/opt/iormesh/data/traces`), a
     directory it creates root-owned 0755; `iormesh run` in a pod
     defaults `-trace-dir` to it, `iormesh ready` and `Fetch`
     read it. The unchecked 0777 root is only the tracer
     container's: no other container mounts the volume. On kind
     under `fsGroup: 2000` (`fsGroupChangePolicy: Always`, task
     yb4) the volume root was `drwxrwsrwx root:2000` and
     `traces/` stayed `drwxr-xr-x` after 20 minutes: the
     ownership pass runs at mount, before the tracer makes it.
   - **Byte cap**: statfs on a disk-backed emptyDir sees the
     node's partition, not the `sizeLimit` past which the kubelet
     evicts the whole pod. `-disk-max-bytes` caps the trace dir
     itself (`segment.Watchdog.MaxDirBytes`). On kind (task yb4)
     the percent limit (`-disk-limit 90`) stopped every debug
     tracer at once on a node whose partition was 96 % used, for
     the node's reasons, not the trace's. So an in-pod tracer
     runs with `k8s.PodDiskArgs` (task df4): `-disk-limit 0
     -disk-max-bytes` `k8s.TraceCap` of the emptyDir's
     `sizeLimit`: the smaller of ¾ and the limit less 96 MiB
     (1536 MiB for 2 GiB, 104 MiB for 200 MiB), in whole MiB.
     The 96 MiB (task 5g4) is the segment being built when the
     cap trips (≤ 64 MB plus a tick's events: the cap is checked
     every few seconds and at rotation) and the end files, which
     a quarter of a small volume could not hold; a `sizeLimit`
     under 128 MiB leaves less than a 32 MiB cap and is refused.
     `TraceCap` reads any byte-size quantity
     (`k8s.parseQuantity`): binary or decimal SI (`2Gi`, `2G`,
     `2000M`), a byte count, a decimal exponent (`2e9`), a
     fraction (`1.5Gi`); a sign, a milli suffix or an exponent
     over two digits is no size. The sidecar patch appends them
     to its args (last, so they win); a debug deploy appends them
     to the tracer argv from the pod's own `sizeLimit`, read with
     the pod (`Kubectl.debug`). In debug mode `checkPrepared`
     refuses a `sizeLimit` `TraceCap` cannot use, so `Check` and
     `Deploy` both say so before any `kubectl debug`, rather than
     start a tracer without a cap. `-disk-min-free` keeps its
     default, so a tracer still never takes a node's last
     256 MiB; the node's fullness otherwise is the kubelet's
     eviction thresholds' business. The trade-off of no percent
     limit (task 5g4): on a node short of disk the tracer keeps
     writing until its byte cap, and the kubelet's node-pressure
     eviction (by default at `nodefs.available` < 10 %) ranks
     pods by ephemeral storage over their request, the trace
     emptyDir included — so a busy trace makes the traced pod an
     earlier eviction candidate on a full node. A 98 % limit
     would not help: the kubelet acts at 90 % used by default,
     before 98 % is reached, and the byte cap already bounds the
     trace's share at ¾ of the volume. An owner who wants the
     trace never to add to node pressure sets a smaller
     `sizeLimit` (or an ephemeral-storage request that covers
     it). A pod entry
     setting `-disk-limit` or `-disk-max-bytes` is refused before
     any pod is contacted (`checkPodFlags`): the pod's would win
     silently.
   - **At the cap** (task ki4): a sidecar's run carries no
     `-linger`, so a session ended by the cap used to exit 0,
     the kubelet restarted the container, and the new run's
     preflight (`checkTraceDisk`) refused the still-full dir —
     a crash loop. The restarting container took the pod
     NotReady (step 8, Readiness), and collect, finding the
     container stopped, reported it stranded without an exec,
     so nothing could drain the dir; at the 18 MB/min reference
     rate a 2 GiB volume reaches its 1536 MiB cap in about 85
     minutes. Now `runSession` calls `awaitDrain`
     (`internal/cli/drain.go`) after the trace: in a pod
     (`k8s.PodFromEnv`), with the trace dir over the
     watchdog's limits — after a `disk_limit` end or a refused
     preflight alike — the run, its tracer already closed,
     polls the watchdog every 2 s and returns only once collect
     (`ready -remove`) has brought the dir back under them, or
     on SIGTERM. It keeps the session's result, so a refused
     preflight still exits non-zero once drained; the restart
     then starts a fresh session. Outside a pod nothing
     restarts the run or collects through an exec, so it still
     fails at once.
   - **Native sidecar or regular container** (for task yb4): the
     patch adds a regular container. A native sidecar (an
     `initContainers` entry with `restartPolicy: Always`, beta
     and on by default from 1.29) starts before the app
     containers, so `-pattern` would see the app's exec, and it
     is stopped after them; but it only waits for the tracer's
     start (or a startup probe, and the image has no shell for
     an exec probe — a tracer subcommand would be needed), and it
     needs 1.29+ nodes. Readiness: a pod is Ready only when every
     container is, so a crash-looping regular tracer takes the
     pod out of its Services' endpoints. Settled on kind (task
     yb4, Kubernetes 1.35): a tracer failing at once (a bad
     flag), as a regular container and as a native sidecar
     without probes, made the pod NotReady and its endpoint
     `ready: false` either way — the native sidecar let the app
     start, but neither shields the app's endpoints. A healthy
     sidecar kept the pod Ready and its endpoint ready. The
     default stays a regular container: the native one buys only
     the start order, at the price of 1.29+ nodes.

   The image:
   `scripts/container/tracer-image.Containerfile` (docs/build.md,
   "Container builds"), with the tracer on its `PATH` for
   `kubectl exec … -- iormesh`. While `k8s.DefaultImage` is empty
   `deploy/` names an unpullable digest-pinned placeholder on
   `registry.invalid`. Task yb4 built the image and ran it on
   kind from a local registry (`mage k8sAcceptance`); none is
   published, so the constant stays empty and `deploy/`
   unchanged until one is — then set it and rerun `mage
   k8sManifests`.
