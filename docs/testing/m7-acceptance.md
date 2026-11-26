# M7 acceptance on kind (task yb4)

Part of [Testing strategy](../testing.md).

milestones.md M7's acceptance runs on one local kind cluster:
`mage k8sAcceptance` (`magefile_k8sacceptance.go`,
`magefile_k8simages.go`) and the tests in `integrationtests/kind`.
The target says SKIP and succeeds when `kind`, `kubectl`, `helm`
or a usable `sudo -n docker` is missing; starting and stopping
the Docker daemon is the operator's. Every docker and kind call
goes through `sudo -n`. It:

1. builds the controller, the static release tracer (`mage
   releaseTracer`, which ran on the Fedora host directly), the
   agent jar and the workload jar;
2. refuses to go on when Docker's data root (`docker info`'s
   `DockerRootDir`, where the node, images and registry volume
   land) has under 20 GiB free, or on btrfs with metadata within
   0.5 GiB of its allocation and under 2 GiB unallocated — before
   the run and before each image build; it never balances, and
   says so (a NOTICE) when the `sudo -n btrfs` check cannot run;
3. records which base images (`kindest/node`, `registry`,
   `eclipse-temurin`) are present, in a state file beside the
   kubeconfig (the first run's; a rerun over a kept cluster keeps
   it), and, once the setup below is done or has failed, the
   exact image IDs this run's steps used: the registry
   container's if it created that container, the node
   container's if it created the cluster, the Containerfile's
   pinned JRE if its workload build ran — each only when this
   run's own snapshot, taken before its setup, did not hold it
   (`kindrun.Ran`, `UsedImages`, `RecordUsed`);
4. starts a local registry (`registry:2`, pinned) on
   `localhost:5001` with a named volume `iormesh-registry-data`
   (a stopped container of that name is removed first), creates
   the cluster `iormesh-m7` without `--kubeconfig` — root never
   writes a path the operator chose — and writes `kind get
   kubeconfig`'s output as the operator to
   `.k8s-acceptance/kubeconfig` at the repository root (or
   `$IORMESH_KIND_KUBECONFIG`; 0600, ignored by git), then wires
   the node's containerd to the registry; its kind and kube-proxy
   configs are `os.CreateTemp` files;
5. builds and pushes the tracer image
   (`scripts/container/tracer-image.Containerfile`) and a workload
   image (`scripts/container/kind-workload.Containerfile`: a JRE,
   `workload.jar`, and, test-only, the agent jar), both labelled
   `org.iormesh.acceptance=m7`, so each pod's `imageID` carries the
   registry digest — a `kind load`ed image reports a bare config
   digest, which deploy refuses;
6. installs Traefik (chart 42.4.1, Traefik 3.7, a ClusterIP
   Service: kind has no load balancer) from a helm repository
   config and cache in a temporary directory, so the operator's
   helm repositories stay as they were;
7. runs `TestKindAcceptance` and `TestKindMicroservices` (below,
   task uh4) with kube-proxy in iptables mode,
   switches kube-proxy to nftables and runs `TestKindClusterIP`,
   each with `go test -v`, judged as `mage integrationTest` is
   (`gatecmd.KindTestsVerdict`, task si4): the target fails when
   the test does not report `--- PASS`, or when it or any subtest
   reports `--- SKIP` (go test exits 0 for both); `mage
   k8sDebugRestart` judges `TestKindDebugRestart` the same way,
   and `mage k8sMicroservices` `TestKindMicroservices`, each
   alone;
8. tears down exactly what it made (`internal/kindrun`
   `TeardownPlan`): the cluster, the registry container with its
   volumes and the named volume, the run's two tags, any image
   left with the run's label (`docker image prune --all
   --filter=label=org.iormesh.acceptance=m7`), the base images
   the run pulled (by ID: those it recorded as used that no base
   repository held before; with no state none — an
   `eclipse-temurin` or `registry` image the operator pulls for
   other work between a kept run and `mage k8sTeardown` stays,
   task oi4; a state file written before that change carries no
   `used_images`, so its base images are left for the operator to
   prune by hand), the kubeconfig and the state file. Every step
   but helm's uninstall exits 0 when its object is already gone,
   so a failing step is a real failure (sudo or the daemon
   refusing): the rest still run, the teardown fails and the
   state file stays for the next attempt (task qi4).
   `IORMESH_KIND_KEEP=1` keeps everything; `mage k8sTeardown`
   runs the same steps on its own and is safe to repeat. Never
   `docker system prune` or an unfiltered prune: the daemon holds
   the operator's own images, volumes and build cache (the first
   manual run's system prune removed unrelated images). BuildKit's
   build cache cannot be labelled, so the two builds' cache — about
   0.6 GB with the JRE base's layers, measured after a run — stays;
   it is the operator's to prune.

Re-run on 2026-11-21 on cf4's tracer, without the workaround and
with the scoped teardown: `TestKindAcceptance` (all six subtests,
104 s) and `TestKindClusterIP` (83 s, nftables) passed, the
ingress subtest's routed edge `→ shop.iormesh.test →
ingress/iormesh-m7/shop → iormesh-m7/inventory →
iormesh-m7/deployment/inventory (route, 1 connections)` among
them. An interrupted run (SIGINT to mage after the first image
push) printed `interrupt: teardown running as pid …`, and the
detached `k8sTeardown` removed the cluster, the registry and its
volume with the images back to the state before.

Re-run on 2026-11-21 on the df4–ff4 tracer and controller,
without either compensation (no disk flags in the registry, no
wait for the `attached` log line; the sidecar's own start, which
no deploy makes, is still awaited by its log line), on a host disk
97 % full (33 GiB free): `TestKindAcceptance` (all six subtests,
185 s) and `TestKindClusterIP` (103 s, nftables) passed. Deploy
reported every debug tracer `attached, session …` before the test
started a workload, each debug container's command ended with
`-disk-limit 0 -disk-max-bytes 1536MiB`, and the routed edge read
`→ shop.iormesh.test → ingress/iormesh-m7/shop →
iormesh-m7/inventory → iormesh-m7/deployment/inventory →
iormesh-m7/inventory-c78778bcd-wqscn (route, 1 connections)`.
Docker was started for the run and stopped after it; `docker
images -a` and `docker volume ls` were identical before and after,
with no container or kind cluster left.

Checked on 2026-11-21 with a real run over a daemon holding
unrelated images: `docker images -a` was identical before and
after (the three pulled base images removed by ID, the operator's
eight tags untouched), no volume or container left, no kind
cluster, no kubeconfig or state file.

Ctrl-C or SIGTERM during the run starts the teardown as `mage
k8sTeardown` in a session of its own (log in
`.k8s-acceptance/teardown.log`),
which outlives mage's 5-second grace for cancelled targets and the
terminal's process-group SIGINT; if the daemon or the binary is
gone by then, run `mage k8sTeardown` by hand — the recovery step
for any interrupted run. `internal/kindrun`'s tests drive the
plans with a fake runner recording every argv: teardown names only
the run's objects (no `system prune`, no prune without the label
filter), removes only base images the run recorded as used and
that were absent before (never an unrelated ID new in a base
repository, nor one the operator pulled between a kept run and a
rerun or between the state's save and a failed step), keeps the
first run's state across a kept cluster's rerun while used IDs
accumulate, keeps `JREImage` equal to the
workload Containerfile's pin, replaces a
stopped registry, never passes `--kubeconfig` to `kind create`,
writes the kubeconfig without following a planted symlink, and
the disk guard reads the data root and refuses a full disk or
tight btrfs metadata.

The tests read the cluster from `IORMESH_KIND_*` (kubeconfig,
context, the two images, the iormeshctl binary, the node
container for `crictl`) and skip without them. Each makes a
fresh namespace, applies `testdata/workloads.yaml`, and applies
deploy/'s patches as an operator would — rendered from
`k8s.Manifests` for the test's image, the sidecar's target set
to the workload's pattern. Every app container sleeps; the test
runs the scenarios with `kubectl exec` once the tracers are
attached, through iormeshctl's real k8s path: a registry with
`k8s:` and `pods:` entries, `deploy`, `collect`, `routes
discover`, `build -registry`.

| Check | What it asserts |
|---|---|
| tracer spec | each debug container as stored: exactly #13's seven capabilities with `drop: [ALL]` after `--profile=general` and `--custom` merged, nothing privileged, `RuntimeDefault` seccomp, uid 0, the three downward-API env vars, both mounts, `command` starting with `iormesh` (the image's `PATH`) and ending with the disk bounds deploy took from the pod (`-disk-limit 0 -disk-max-bytes 1536MiB`, task df4); the tracer's status `imageID` names the pushed digest (the sidecar's too) |
| `clusterip` | tcp-nat's client dials the server Service's ClusterIP (880, DNAT to the pod's 80): the build prints the routed edge `→ Service → deployment → pod (fused, 1 connections)`; each pod's trace matches `expect-client.json` / `expect-server.json` and the merge `expect.json`, the rig's addresses replaced by the client pod's, the ClusterIP and the server pod's (no SNAT between pods); a golden with the client dialing the pod fails |
| `hairpin` | tcp-nat's two roles in one pod, the client dialing the pod's own Service: one fused connection, the server end seeing the node's masquerade address (10.244.0.1), the Service and pod named |
| `identity` | every pod is its own store directory and host, `pod-<uid>`, with the FQDN `<name>.<ns>.<uid[:8]>`; all share one `boot_id` |
| `ingress` | the gateway (debug container with the agent) asks for `shop.iormesh.test` through Traefik (hostAliases to Traefik's ClusterIP); the backend runs under a sidecar. The build prints one routed edge `→ shop.iormesh.test → ingress/<ns>/shop → <ns>/inventory → <ns>/deployment/inventory → <ns>/<inventory pod> (route, 1 connections)` — the pod named as the Service's sole endpoint, traced by the sidecar (task ff4) — no route flag; `model.json` and `graph.svg` carry it; the discovered route's FQDN, ingress, Service and ingress Service equal the scenario's declared `routes.yaml` (discovery leaves the workload to the Service, so it is checked in the build's line), and the snapshot holds the IngressRoute's host too (`traefik.io`); the merged trace matches `workload/scenarios/ingress/expect.json` (both ends unresolved: Traefik splits the call; the gateway's `dns` and `http` events carry the FQDN) |
| `rbac` | `routes discover` as a ServiceAccount allowed only `list` on services, pods, endpointslices, replicasets, ingresses and ingressroutes (both Traefik groups), not secrets: same two routes |
| `sigterm` | deleting a pod ends its debug tracer on the kubelet's SIGTERM: exit 0, about 0.6 s after the delete, inside the 5 s grace |

`TestKindClusterIP` is the `clusterip` row alone, in a namespace
of its own, under nftables.

The gateway's tracer runs `-agent` with the tracer image's jar
and its spool on the tracer's emptyDir; the app container sees
neither, so the tracer stages both inside the JVM's root (M7
step 3, decision #14, task cf4). Until cf4 the run below used a
test-only workaround — the app container mounted the trace
emptyDir and the workload image carried the jar at the tracer's
paths — which is now gone from `testdata/workloads.yaml` and
`kind-workload.Containerfile`. The re-run without it (below)
passed, the ingress routed edge included: the agent the tracer
staged reported the gateway's FQDN as its HTTP host.
The gateway starts behind the workload's startup gate, so the
debug tracer finds the JVM at start and live-loads the agent
before the bracket.

Run on 2026-11-21 (kind 0.31.0, Kubernetes 1.35.0, containerd
2.2.0, Docker 29.9.2, Fedora kernel 7.2.15, one node):
`mage k8sAcceptance` from scratch passed: `TestKindAcceptance`
(every subtest, 107 s; iptables) and `TestKindClusterIP` (84 s;
nftables), then tore the cluster and registry down. Two pods
happened to share pod-local pid 47 and still gave two process
ids. The first from-scratch run had failed only the ingress
golden, which had pinned the backend's `close` to its main
thread — the JDK HttpServer closes on whichever thread finishes
the exchange, so the golden no longer declares it.

Run on 2026-11-24 at 4ca2f82 (task 5k4; kind 0.31.0, Kubernetes
1.35.0 from `kindest/node:v1.35.0`, its containerd 2.2.0, Docker
29.9.2, Fedora kernel 7.2.15, one node): `mage k8sAcceptance`
from scratch, Docker freshly started, passed in 7 min 40 s wall
time, the first full run since sh4, ki4, ii4, the kindrun
claim/record/teardown changes and `TestKindMicroservices` joined
the suite. `TestKindAcceptance` passed every subtest (133 s;
iptables), `TestKindMicroservices` its `mesh`, `pod_fields` and
`identity` subtests (122 s), and `TestKindClusterIP` passed
(95 s; nftables), none skipped. The scoped teardown left `kind
get clusters` empty and no `iormesh-registry` container; it
removed the registry's volume, the two pushed images and the base
images the run had pulled.

Checked by hand on the same cluster and recorded here, not
automated:

- **kubectl floor**: `kubectl debug --custom` is unknown to
  1.29.15, and to 1.30.14 unless `KUBECTL_DEBUG_CUSTOM_PROFILE=true`
  (the controller now sets it on every kubectl); 1.31.12 and
  1.35.9 take it by default. Every version that took it stored
  exactly the custom capabilities with `--profile=general`.
- **Readiness**: a tracer failing at once, as a regular container
  and as a native sidecar (`initContainers`, `restartPolicy:
  Always`), made the pod NotReady and its endpoint not ready
  either way; a healthy sidecar kept both ready
  (docs/implementation.md M7 step 8).
- **`-linger`**: a session ended by `-duration` left the debug
  container running and collectable (its last segment fetched),
  and the pod's deletion ended it with exit 0.
- **`fsGroup`**: under `fsGroup: 2000` the emptyDir root was
  `drwxrwsrwx root:2000` and `traces/` stayed `drwxr-xr-x` 20
  minutes on.
- **Ingress Service**: discovering Traefik's namespace too
  without `-ingress-service` (kind's ClusterIP Service gives the
  Ingress no status address) named the gateway's edge as the
  Traefik Service with `route_contradicted`; with
  `-ingress-service traefik/traefik` it is the route's edge.
- **RBAC**: a forbidden `ingressroutes` list fails discover whole
  and writes no snapshot.

Found and fixed: `build` had no `-registry`, so the snapshot's
context was checked against the default registry and refused
(`build` takes it now, `refresh` shares its own;
`TestRoutesRegistryFlag`); a selector matched a terminating pod
right after a rollout and deploy failed on it (`Select` skips
`deletionTimestamp`; `TestSelectArgvAndFilter` — so deploy,
collect and rm (its final collect) of a selector entry now skip
pods in their grace period, whose tracer is ending on SIGTERM
anyway); kubectl 1.30's gate (`TestDebugArgv`). Follow-ups: cf4
(agent staging in the JVM's root; built since), df4 (debug
deploy without a byte cap: the 90 % `-disk-limit` stopped every
debug tracer on a node fuller than that, so the test passed
`-disk-limit=0 -disk-max-bytes=1536MiB`; built since: deploy
passes the pod's bounds itself, the tracer spec check asserts
them, and the registry carries only the gateway's agent flags),
ef4 (deploy returned before the tracer was attached; the test
waited for its `attached` log line; built since: deploy waits
for the tracer's attach state and the test only checks deploy's
`attached, session` line — the sidecar's own start, which no
deploy makes, is still awaited by its log line), ff4 (a route
edge never named a sole endpoint's pod; built since: a traced
sole endpoint is named, and the `ingress` check expects the
inventory pod at the end of the routed edge, in the summary and
`model.json`).

## Parts

Each part is its own file; open only the one you need.

- [Microservices as pods (task uh4)](m7-acceptance/pods.md) — the
  microservices scenario as pods (uh4).
- [M7 on an existing cluster](m7-acceptance/existing-cluster.md) — an
  existing cluster (zg4), the Rocky 9.9 run, the load refusal hint (6h4).
- [SELinux reruns](m7-acceptance/selinux.md) — spc_t (5h4), app
  container restart (8h4), the iormesh_container_t type (7h4).
- [kind reruns](m7-acceptance/kind-reruns.md) — debug tracer
  replacement (ah4), rerun with pod fields (6k4).
