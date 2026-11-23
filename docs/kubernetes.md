# Tracing pods on Kubernetes

This is the operator's runbook for tracing pods with iormesh:
what a cluster and a pod need first, which mode to pick, how to
deploy, collect and clean up, the limits you will run into, and
what the tools' error messages mean. The decisions behind it are
in [PLAN.md](../PLAN.md): milestone
[M7](../PLAN.md#m7--kubernetes-sidecar--debug-container-deployment)
and the provisional decisions #13 (privilege profile), #14 (Java
attach) and #18 (iormeshctl's config), under
["Provisional — M7"](../PLAN.md#provisional--m7-2026-11-20). The
owner has confirmed them for now. The security reasoning is in
[security.md](security.md) ("Kubernetes"), and the test evidence
is in [testing.md](testing.md#m7-acceptance-on-kind-task-yb4).

iormeshctl reaches a cluster only through your own `kubectl`,
and always with an explicit `--context` taken from the registry.
It never uses the kubeconfig's current context. Every value it
passes to kubectl is checked against Kubernetes' name rules
first, and nothing goes through a shell.

## How it fits together

- **The tracer runs in the pod.** It runs as a container in the
  target pod, either an ephemeral debug container (`kubectl debug
  --target`) or a sidecar. It shares the pod's PID namespace and
  traces only the pod's processes.
- **The pod is the host.** Each pod gets its own host identity,
  `pod-<uid>`, taken from the downward API, and its own store
  directory on the laptop, `<store>/<project>/pod-<uid>/`. In a
  pod the project defaults to the namespace (decision #21).
- **One tracer per pod.** A pod never runs two tracers at once.
- **Traces stay in the pod until collect fetches them.** The
  tracer writes to an `emptyDir` of the pod (`iormesh-traces`,
  2Gi by default). `iormeshctl collect` streams the published
  segments out through `kubectl exec` and then deletes them in
  the pod. Nothing is ever pushed to the pod: the image carries
  the tracer.

## Prerequisites

### Controller (laptop)

- `kubectl` 1.30 or later. The debug container's security
  context goes in through `kubectl debug --custom`. 1.30 needs
  `KUBECTL_DEBUG_CUSTOM_PROFILE=true`, which iormeshctl sets
  itself, and 1.29 has no `--custom` at all
  ([build.md](build.md)).
- A kubeconfig context for the cluster, listed in the registry
  (below).
- `iormeshctl` (cgo-free, `CGO_ENABLED=0`). Unlike a server
  deploy, a pod deploy needs no signed `dist/` and no minisign
  key: the image is the build.

### Tracer image: build and pin your own

No tracer image is published yet. `k8s.DefaultImage`
(internal/k8s/target.go) is empty, so every pod deploy must name
an image pinned by digest, and is refused without one. The
generated manifests in `deploy/` name an unpullable
`registry.invalid/...@sha256:000…` placeholder for the same
reason.

1. Build the release tracer and the agent jar: `mage buildPodman`
   (or `mage buildDocker`) for `dist/iormesh-linux-<arch>`, and
   `mage agentBuild` for `iormesh-agent.jar`.
2. Build the image from the repository root, one per arch:

   ```
   podman build --platform linux/amd64 \
       -f scripts/container/tracer-image.Containerfile \
       -t <registry>/iormesh .
   ```

   The image is `FROM scratch`: the static tracer and the jar,
   with no shell and no package manager ([build.md](build.md),
   "The tracer image").
3. Push it, and use the digest the registry reports:
   `<registry>/iormesh@sha256:<64 hex>`. A tag alone is refused
   everywhere: in the registry file, by `-image` and by the
   manifest generator.

Put the pinned image in the registry's `k8s.image` (or a pod
entry's `image:`), or pass it per run with `deploy -image`. Only
`deploy` needs it; `collect` does not check the image. Nothing
signs the image yet: the digest pins the bytes, but no signature
vouches for them.

When deploy finds a tracer already in a pod (a sidecar, or an
earlier debug container), it checks the container's status
`imageID` (the digest the kubelet actually pulled) against the
pinned one. It refuses a different digest, and also an `imageID`
that names no registry digest at all, such as a locally loaded
image. Push the image to a registry the nodes pull from.

### Nodes

- A kernel the tracer supports. el9's 5.14 is the main target.
- The host's tracefs at `/sys/kernel/tracing`, which the prepare
  patch mounts read-only.
- On an SELinux-enforcing node (el9): see [SELinux](#selinux).

### Namespace: Pod Security level `privileged`

Pod Security `baseline` and `restricted` refuse the tracer's
profile, both the minimal capability set and the `privileged`
fallback. The namespace of the pods you trace must enforce the
`privileged` level, or be exempted. That is the namespace
owner's call. With Kubernetes' standard Pod Security admission,
that is the namespace label
`pod-security.kubernetes.io/enforce=privileged`. All acceptance
runs so far used namespaces at `privileged`; a level below it
has not been tried.

### RBAC for the operator

| Mode | iormeshctl needs, in the pod's namespace |
|---|---|
| debug | `get`, `list` on `pods`; `patch` on `pods/ephemeralcontainers`; `create` on `pods/exec` |
| sidecar | `get`, `list` on `pods`; `create` on `pods/exec` |

`list` is only used when the entry is a selector. Applying the
prepare or sidecar patch is the workload owner's job and needs
`patch` on the workload (a Deployment, say), not anything above.
To read a tracer's logs when troubleshooting, you need `get` on
`pods/log`. `routes discover` is read-only and needs `get`/`list`
on what it reads: Ingresses, Traefik IngressRoutes, Services,
EndpointSlices, pods and ReplicaSets.

Grant the debug-mode verbs like root on the nodes. Whoever can
add an ephemeral container in a `privileged` namespace can start
a BPF-capable container there, and BPF is node-wide
([security.md](security.md)).

### Prepare the pods (both modes)

An ephemeral container cannot add volumes or change pod-level
fields. So every pod to be traced is prepared once, by the
workload owner, with `deploy/prepare-patch.yaml`. The patch sets
`hostUsers: true` (a user-namespaced pod's capabilities do not
reach BPF), adds the read-only `tracefs` hostPath and adds the
`iormesh-traces` emptyDir with `sizeLimit: 2Gi`. It adds no
container. It causes one rollout:

```
kubectl patch deployment <name> --patch-file deploy/prepare-patch.yaml
```

The trace volume's `sizeLimit` sets the tracer's byte cap: ¾ of
it, or the size less 96 MiB if that is smaller, so 1536 MiB for
2Gi. Deploy passes the cap to a debug tracer itself. A
`sizeLimit` under 128Mi is refused in debug mode. The sidecar
patch includes the prepare patch.

### The registry: `k8s:` and `pods:`

Pods are registered by hand in the same registry file as
servers (`~/.config/iormesh/servers.yaml`, 0600). `iormeshctl
add` registers only hosts. The shape is #18's and still
provisional:

```yaml
k8s:
  image: <registry>/iormesh@sha256:<digest>  # no default yet
  mode: debug                  # debug (default) | sidecar
  profile: minimal             # minimal (default) | privileged
  selinux_type: spc_t          # default; "" omits seLinuxOptions
  selinux_level: s0            # default; or the pod's s0:cN,cM
  clusters:
    - name: prod               # what pod entries and refs name
      context: prod-admin      # always passed as --context
      namespace: shop          # default namespace and project
pods:
  - cluster: prod
    selector: app=checkout     # or pod: <pod-name>, not both
    container: app             # the app container (debug --target)
    projects:                  # name: left out, so "shop"
      - target: {pattern: "*checkout.jar*"}
        flags: ["-spool=/opt/iormesh/data/spool",
                "-agent=/opt/iormesh/lib/iormesh-agent.jar"]
  - cluster: prod
    namespace: shop
    pod: inventory-0
    container: app
    mode: sidecar
    projects:
      - target: {listen_port: 8080}
```

- **Reference.** A pod entry is named on the command line as
  `<cluster>/<namespace>/<pod>` or `<cluster>/<namespace>/<selector>`,
  for example `prod/shop/app=checkout`.
- **Precedence.** Image and SELinux type and level: a deploy
  flag (`-image`, `-selinux-type`, `-selinux-level`) beats the
  pod entry, which beats the `k8s:` default. Mode and profile
  have no flag: the pod entry beats the `k8s:` default. A
  cluster sets none of these, only the context and namespace:
  for those, `-context`/`-namespace` beat the pod entry, which
  beats the cluster. `-context` may only restate the entry's
  own cluster's context. A context the registry does not list
  is refused.
- **Projects.** A debug entry carries exactly one project, since
  an ephemeral container runs one session. A project's `name:`
  may be left out; it is then the namespace.
- **Flags iormeshctl sets itself.** The flags pass through to
  `iormesh run`. An entry may not set `-disk-limit` or
  `-disk-max-bytes` (deploy takes them from the pod's
  `sizeLimit`). It may not set a `-trace-dir` other than the
  pod's emptyDir. It may not set the flags the entry owns itself
  (`-project`, `-pattern`, `-listen-port`, `-pid`, `-out`).
- **Java attribution.** In the scratch image the agent jar is at
  `/opt/iormesh/lib/iormesh-agent.jar`, and the spool goes on the
  emptyDir, as in the example. The tracer stages both inside the
  JVM's root for a live attach ([java-attach.md](java-attach.md),
  decision #14).

## Debug or sidecar?

| | debug (ephemeral container) | sidecar |
|---|---|---|
| Who adds the tracer | the operator, with `iormeshctl deploy` | the workload owner, with the sidecar patch |
| Workload change | prepare patch only (one rollout) | sidecar patch (one rollout), plus `shareProcessNamespace: true` |
| JVM restart to attach | no: live attach (#14) | no, but a JVM started before the tracer is missed by `-pattern` |
| App container restarts | **the tracer dies with it** | the tracer keeps running |
| Removable | no: runs until the pod goes | revert the patch (another rollout) |
| Session command line | the registry entry | the patch's `args` |

**Use debug mode** for a look at a pod that stays up: there is
no workload change beyond the prepare patch, and nothing to
remove later except by restarting the pod.

**Use sidecar mode** for pods that restart (crashes, OOM kills,
liveness kills), and for tracing over a long period. In debug
mode the tracer shares the app container's PID namespace, so
when the app container's init dies, the kernel kills the tracer
too (exit 137), and an ephemeral container is never restarted.
In a sidecar pod, the pause container is init of the shared PID
namespace, so the tracer survives. A restarted JVM gets no agent
from `run -agent`; give such JVMs `-javaagent` (PLAN.md M7,
[java-attach.md](java-attach.md)).

## Deploy

### Debug mode

```
iormeshctl deploy prod/shop/app=checkout
iormeshctl deploy -image <registry>/iormesh@sha256:<digest> \
    prod/shop/app=checkout
```

For each running pod the entry names, deploy:

1. reads the pod and checks that it is prepared (`hostUsers`,
   both volumes, a usable `sizeLimit`) and has the app
   container;
2. starts the tracer as ephemeral container `iormesh`, running
   `iormesh run -linger …` with the entry's project and target,
   the pod's disk bounds, and the pinned `--image`;
3. waits until the session reports its probes attached, for at
   most `-attach-timeout` (default 2m), and prints
   `started tracer, project P, image I; attached, session S`.

A workload started after deploy returns is traced from its first
syscall. On a rerun, deploy leaves alone a tracer that is still
running the pinned image and the same project ("already running
the pinned image … left alone"). If the pod's tracer has ended
(see [Limits](#limits-you-will-hit)), deploy starts the next one,
as described there.

`-selinux-type` and `-selinux-level` (or `selinux_type` and
`selinux_level` in the registry) set a debug tracer's SELinux
options; see [SELinux](#selinux).

### Sidecar mode

1. Copy `deploy/sidecar-patch.yaml` and edit the copy:
   - `image:` your pinned image;
   - `args:` the app's target, for example `-pattern
     '*app.jar*'` in place of `-listen-port 8080`. Keep
     `-disk-limit 0` and set `-disk-max-bytes` to match your
     `sizeLimit` (1536MiB for the default 2Gi). `-duration
     168h`, the longest a run takes, keeps sessions long: when
     one ends the container exits and the kubelet restarts it
     into the next session;
   - `level:` under `seLinuxOptions`, if the pod pins its MCS
     level ([SELinux](#selinux)).
2. The workload owner applies it:
   `kubectl patch deployment <name> --patch-file <copy>`.
3. Register the pod entry with `mode: sidecar`. The sidecar's
   args carry no `-project`, so its project is the namespace;
   leave the entry's project `name:` out to match that. If you
   add `-project` to the args, use the same name in the entry.
4. Run `iormeshctl deploy <ref>`. In sidecar mode deploy starts
   nothing: it checks that the sidecar is there with
   `shareProcessNamespace`, and that its pulled image matches
   the pin. It prints `sidecar running the pinned image; its
   sessions are the sidecar patch's command line`.

The patch's `-listen-port` finds the listener's owner at start,
and fails if the app is not listening yet; after that it follows
the port to a restarted app of the same executable and uid for
`-listen-grace` (default 2m). `-pattern`
only matches execs after the tracer attached. In either case the
container restart (restart policy) retries.

## Collect and build

```
iormeshctl collect prod/shop/app=checkout   # these entries
iormeshctl collect                          # every host and pod
iormeshctl refresh                          # collect, then build
iormeshctl build
```

- A selector is resolved again on every run: collect reaches the
  pods that are running now. Terminating pods are skipped.
- Each pod's segments go to `<store>/<project>/pod-<uid>/`.
  Before any segment arrives, the directory is pinned to the
  pod's identity as the API server reports it, never as the
  pod's own segments claim it. The pins live in `hosts.json`
  beside the service database. If you pass `-db` to `build`,
  pass the same `-db` to `collect`; `refresh` shares one.
- In debug mode, collect enters the pod's newest tracer
  container. The emptyDir is the pod's, so it also serves what
  earlier tracers in the pod published.
- A pod without a tracer container prints `no "iormesh" tracer
  container, nothing to collect`.
- `collect` and `refresh` of every target take no `-context` or
  `-namespace`.

Collect often. Segments still in a pod are lost when the pod
goes, and a full trace volume stops tracing until collect drains
it.

Every event of a pod's tracer names its namespace, pod and the
container its process runs in (the `pod` object,
[schema.md](schema.md#pod-fields-on-events-m7)), so in sidecar
mode the app's, a sidecar's and the tracer's own processes are
told apart. To look at one namespace:

```
iormeshctl build -namespace prod   # only pods in prod
iormeshctl show services -namespace prod
iormeshctl open -namespace prod    # its site in the browser
iormeshctl tui -namespace prod     # its model in the terminal
```

The build keeps the sessions of pods in `prod` (hosts outside a
pod are left out) and the services and edges their connections
make, and writes `<projects>@prod/` next to the unfiltered build;
`show`, `open` and `tui` read it with `-namespace prod`, and the
unfiltered one without. With a window too (`build -namespace prod
-last 2h`) it writes `<projects>@prod~window/`, which they read
with `-namespace prod -windowed`
([visualization.md](visualization.md#namespace-and-windowed-builds)).
`refresh` does not take it: its `-namespace` is the kubectl
namespace of the collect.

## Limits you will hit

- **A debug tracer dies with its app container.** A crash, OOM
  kill or liveness kill of the app container kills the tracer
  too. Its unsealed segment is lost, up to one rotation interval
  of events; its published segments stay in the emptyDir. Run
  `iormeshctl deploy <ref>` again: once every earlier tracer in
  the pod has ended, it starts the next one, `iormesh-2`, and so
  on up to `iormesh-8` (ephemeral container names cannot be
  reused or removed). It says `earlier tracer ended (its
  unsealed segment is lost, its published ones stay for
  collect)`. After eight tracers it refuses; restart the pod or
  switch to sidecar mode. A JVM started after the new tracer
  gets no live-attached agent.
- **Uncollected segments die with the pod.** The trace volume is
  an emptyDir. Deleting, evicting or rescheduling the pod, or a
  rollout (including reverting a patch), deletes whatever collect
  has not fetched yet.
- **A sidecar at its cap waits for collect.** When the trace
  volume reaches its byte cap, the session ends (`disk_limit`).
  The sidecar then does not exit: it frees its BPF and waits,
  logging `BPF released, waiting for collect to drain it (or
  SIGTERM)`, until collect has drained the volume back under the
  cap. It then exits, and the kubelet restarts it into a fresh
  session. Nothing is traced in between. At the 18 MB/min
  reference rate, a 2Gi volume fills its 1536 MiB cap in about
  85 minutes, so collect more often than that. A debug tracer at
  its cap idles as after its `-duration` (next item): collect
  still drains it, but a new session needs a pod restart.
- **A debug tracer's session has a duration.** The session ends
  after `-duration` (4h unless the entry's flags set one). The
  tracer then frees its BPF and stays idle (`-linger`), so
  collect can still fetch its last segment. A new session needs
  a pod restart.
- **A selector reaches at most 32 pods.** More than 32 running
  matches is refused as a selector broader than intended, and no
  pod is touched. Narrow the selector or split the entry.
- **`rm` leaves the tracer running.** `iormeshctl rm <ref>`
  collects the entry once more and unregisters it, and then
  prints `the tracer in its pods is not stopped`. A debug tracer
  runs until its `-duration` ends (and then idles) or the pod
  goes; a sidecar runs until its patch is reverted. See
  [Cleanup](#cleanup).
- **Time bounds.** Each kubectl get, debug or short exec gets 2
  minutes, the fetch stream 2 minutes of silence, and each pod
  entry an hour per command, after which iormeshctl moves on to
  the next. Ctrl-C ends the running kubectl calls, removes
  `kubectl debug`'s temporary profile and reports "interrupted".
- **Tested on single-node clusters only.** M7 was accepted on
  one single-node kind cluster (Fedora host, 7.2 kernel, no LSM,
  containerd 2.2.0) and one single-node k3s cluster on Rocky 9.9
  (SELinux enforcing, containerd 2.2.10, kubectl 1.35). There
  has been no multi-node cluster, no CRI-O, no OpenShift and no
  Pod Security level below `privileged`
  ([PLAN.md M7](../PLAN.md#m7--kubernetes-sidecar--debug-container-deployment)).

## SELinux

On an SELinux-enforcing node (el9), `container_t` is denied
`bpf { prog_load }`. By default the tracer container, and never
the rest of the pod, therefore runs as **`spc_t` at level `s0`**.
`spc_t` is unconfined by SELinux. What bounds the tracer there
is the capability set, `RuntimeDefault` seccomp and no privilege
escalation. The `s0` level keeps the pod's shared files
(`/etc/hosts`, `resolv.conf`, the trace emptyDir), which the
runtime relabels to the tracer's level, readable by the app
containers, at the cost of their own MCS pair. The label stays
after a debug tracer exits.

- **The pod pins its MCS level** (`securityContext.seLinuxOptions.level`,
  e.g. `s0:c123,c456`): give the tracer the same pair. For a
  debug tracer use `-selinux-level s0:c123,c456` or
  `selinux_level`; for a sidecar, the patch's `level:` line. The
  shared files then keep the pod's pair.
- **Opt-in confinement: `iormesh_container_t`.** Where nodes
  accept the RPM, the `iormesh-selinux` policy module confines
  the tracer to its own MCS-constrained type, with only the
  tracer's seven capabilities, BPF, perf events, read-only
  tracefs and the Java attach. Build it with `mage selinuxRPM
  podman` (or `docker`) into `dist/selinux/` (sources in
  `packaging/selinux/`) and install it on each node (it requires
  `container-selinux`). Then deploy at the pod's own MCS pair:

  ```
  iormeshctl deploy -selinux-type iormesh_container_t \
      -selinux-level s0:c123,c456 prod/shop/app=checkout
  ```

  At plain `s0` the Java attach is denied under this type. Pods
  that share one pinned pair (replicas, a namespace) are not
  separated by MCS. Stop the tracers before removing the RPM, or
  they become `unlabeled_t`.
- `-selinux-type ""` omits `seLinuxOptions` and leaves the
  runtime's default, which fails on enforcing el9.
- `profile: privileged` is the fallback where the minimal set
  cannot run. It widens what a compromised tracer could do to
  the whole node, so use it only after the minimal set was tried.

The details and the el9 runs are in [security.md](security.md)
("Kubernetes") and
[testing.md](testing.md#dedicated-type-iormesh_container_t-task-7h4-2026-11-21).

## Cleanup

1. **Collect first.** Every step below that deletes a pod also
   deletes its uncollected segments.
2. **Unregister**: `iormeshctl rm <ref>` (or `rm -project NAME
   <ref>` for one project). If the last collect fails, rm leaves
   the registry unchanged; `rm -force` drops the entry anyway and
   loses what was not collected. rm takes no `-context` or
   `-namespace`.
3. **Stop the tracer.**
   - Debug: an ephemeral container cannot be stopped or removed.
     Restart the pod (a rollout restart, or delete the pod) when
     you want it gone. Until then it idles after its
     `-duration`, holding no BPF.
   - Sidecar: the workload owner reverts the sidecar patch,
     which causes a rollout.
4. **Unprepare**: the workload owner reverts the prepare patch
   when no more tracing is planned, which causes another rollout.
5. **Locally**: the traces stay in `<store>/<project>/pod-<uid>/`
   until you delete them. Treat them as production metadata.
6. **SELinux module**: stop every `iormesh_container_t` tracer
   before removing the RPM from a node.

## Troubleshooting

Errors name the pod entry or the pod they are about (deploy
prints `iormeshctl: <ref> pod <name>: <error>`). Look for the
fragments below. The tracer's own side is in its log:
`kubectl logs <pod> --container=iormesh` (or `iormesh-N` for a
replaced debug tracer; the newest is the live one).

| Message contains | Meaning and fix |
|---|---|
| `k8s: image refused: no tracer image configured (set k8s.image or -image, pinned by digest)` | No image: [build and pin your own](#tracer-image-build-and-pin-your-own). |
| `is not pinned by digest (<repo>@sha256:<64 hex>)` | A tag was given; use the pushed digest. |
| `runs sha256:…, not the pinned …` | The pod's tracer (a sidecar, or an earlier debug container) runs other bytes than the pin. Fix the sidecar's image or the pin. |
| `runs imageID "…", which names no registry digest to check against` | A locally loaded image; push it to a registry. |
| `has spec image "…", not the pinned …; restart the pod for a new session` | A running debug tracer was created with another image. |
| `k8s: context refused: "…" is not a registered context` | Add the cluster to `k8s.clusters`, or fix `-context`. |
| `-context and -namespace are not taken by …; name the pod entry instead` | `rm`, or a collect or refresh of every target, given `-context` or `-namespace`. |
| `registry: pod entry not registered` | No `pods:` entry has this reference; check the namespace part, which defaults to the cluster's. |
| `k8s: pod not prepared: …; apply deploy/prepare-patch.yaml to its workload first` | Followed by `hostUsers is false`, `no volume "tracefs"` or `no volume "iormesh-traces"`: apply the prepare patch. |
| `volume "iormesh-traces" has sizeLimit "…"; want a size of at least 128Mi` | Raise the emptyDir's `sizeLimit` (debug mode). |
| `no "iormesh" sidecar with shareProcessNamespace; apply the sidecar patch` | A sidecar-mode entry for a pod without the patch. |
| `already has a "iormesh" sidecar; trace it in sidecar mode` | Set `mode: sidecar` on the entry. |
| `k8s: no running pod: selector "…" matches N pods in NS, none running` | Wrong selector or namespace, or the pods are not up. |
| `k8s: selector matches too many pods: … at most 32` | Narrow the selector. |
| `k8s: tracer not attached: … not attached after 2m0s (last: …); see kubectl logs <pod> --container=<tracer>` | Read the tracer's log. A slow image pull may need a longer `-attach-timeout`. |
| `BPF load refused; SELinux is enforcing and this process runs as …` (in the tracer's log) | A `container_t` tracer on el9: keep the default `spc_t`, or use the module. The denial shows in the raw `audit.log`. |
| `the session already ended (…); restart the pod for a new one` or `the tracer died without its end` | The debug session is over; restart the pod for a new one. |
| `traces project "…", not "…"` | A running debug tracer traces another project; restart the pod. |
| `debug tracer "…" ended and the pod has had 8, the most deploy starts in one pod` | Restart the pod, or use sidecar mode. |
| `session ended: tracer container "…" …; kubectl exec cannot enter a stopped container: iormeshctl deploy … starts a fresh tracer` | Debug tracer killed with its app: run deploy, then collect again. |
| `session ended: … stranded until the pod goes` | Nothing can enter the pod's emptyDir anymore. Its segments are lost when the pod goes. |
| `k8s: another fetch into this directory is running` | A second collect into the same `pod-<uid>`; wait for the first. |
| `k8s: kubectl stalled: … no answer within 2m0s` or `no data for 2m0s on the fetch stream` | The API server, a credential plugin or the exec stream hung. Retry; segments that were not fetched stay in the pod. |
| `(gave up on the host after 1h0m0s)` | The per-entry hour ran out; the next entry was still tried. |
| `k8s: pod not reached: …` followed by kubectl's own text | kubectl itself failed: `Forbidden` (RBAC, or Pod Security refusing the debug container), pod not found, unknown context. |
| `flag -disk-max-bytes…: deploy sets it from the pod's iormesh-traces sizeLimit` | Remove disk flags from the entry. |
| `debug mode runs one session per pod, not N projects` | Split the entry, or use sidecar mode. |
