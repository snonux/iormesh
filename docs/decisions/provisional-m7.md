# Provisional — M7 (2026-11-20)

Part of [Decisions](../decisions.md).

Settled provisionally under the sequencing relaxation (see
[milestones.md](../milestones.md)) on the leanings the project had
already recorded, task ub4. Each names
what would overturn it and what the owner must confirm. When
settled none was proven on kind (the settling host ran no
Docker); M7's acceptance on kind and el9 (tasks yb4, cf4–ff4,
5h4) has since exercised them, but they stay provisional until
the owner confirms them. Fleet facts — kubectl contexts,
namespaces, registries, kernels — are the owner's and appear
here only as placeholders.

- **#13 K8s privilege profile** — *Provisional (2026-11-20):*
  the minimal capability set, `privileged: true` the documented
  fallback; the tracer container — never the pod — runs as
  SELinux type `spc_t` (task 5h4, below). The tracer container
  runs as uid 0 with `allowPrivilegeEscalation: false` in a pod
  with `hostUsers: true` (a user-namespaced pod's capabilities do not reach BPF
  or the host kernel's tracing), drops ALL and adds:
  - `BPF` and `PERFMON` — loading tracing programs (tracepoint,
    raw tracepoint, uprobe_multi), the ring buffer and stack map,
    and the `perf_event_open` that attaches each tracepoint;
  - `SYS_RESOURCE` — only because libbpfgo v0.9.2's
    `NewModuleFromBuffer` raises RLIMIT_MEMLOCK to 512 MiB
    before every load; the kernel (≥ 5.11) charges BPF memory to
    the memcg and needs no limit. A load path that skips the bump
    removes it (M7 code);
  - for Java attribution (`-spool`, `-agent`, `attach`):
    `SYS_PTRACE` (the JVM's `/proc/<pid>/root`, `cwd`, `maps`,
    `environ` across uids — also `-listen-port`'s fd scan and
    `-native-stacks`), `DAC_OVERRIDE` (the JVM's 0600 attach
    socket, its 0600 ring, its working directory), `KILL` (the
    one SIGQUIT to a JVM of another uid) and
    `CHECKPOINT_RESTORE` (`/proc/<pid>/map_files`, by which the
    Java mark opens the very `libjava.so` mapped). M7 may drop
    it: open the library through `/proc/<pid>/root/<path>`
    (`RESOLVE_IN_ROOT`, as for unmapped libraries today) and
    accept it only if its device and inode equal the mapping's
    in `/proc/<pid>/maps` — the same file, proved without
    `map_files`.
  No `SYS_ADMIN`: every attach type the tracer uses (classic
  tracepoint, raw tracepoint, uprobe_multi) checks
  `CAP_BPF`/`CAP_PERFMON`. Kernel floors: 5.8 for the two
  capabilities and the ring buffer, 5.9 for
  `CAP_CHECKPOINT_RESTORE`, and 6.10 (or a 6.6–6.9 kernel with
  commit 46ba0e49, internal/bpf/javamark.go) for Java
  attribution's uprobe_multi marks — upstream versions; Rocky
  9.9's el9 5.14 carries all of these as backports (task lb4,
  see "Target platform"). Measured on the host, not
  assumed: this set traces and attaches as root does, and each
  capability left out alone breaks a step it is listed for
  ([testing.md](../testing/k8s-tracer.md#k8s-capability-set-task-ub4)).
  Also required — volumes the pod must already carry, since an
  ephemeral container can mount only the pod's own volumes:
  - tracefs from the host, read-only (hostPath
    `/sys/kernel/tracing`): libbpf reads each classic
    tracepoint's id there, a container's sysfs has none, and
    mounting it needs `SYS_ADMIN`. Without it nothing attaches
    (`errNothingAttached`). Raw or BTF tracepoints would need
    only BTF, but task 8e4 measured them skipping runs on 7.2
    (internal/c/dispatch.c), so the classic hooks — and tracefs
    — stay;
  - a size-bounded `emptyDir` (`sizeLimit`) for the trace dir,
    so segments never fill the container's writable layer
    unbounded.
  No third volume for the host id: the node's
  `/etc/machine-id` is not mounted — an in-pod tracer's host is
  the pod, named from the downward API (M7 "pod is the host",
  task 7f4).
  BTF comes from the container's own sysfs
  (`/sys/kernel/btf/vmlinux`). Seccomp: `RuntimeDefault` is the
  provisional default — the containerd and Moby default
  profiles allow `bpf` with `CAP_BPF` and `perf_event_open`
  with `CAP_PERFMON`, and `openat2`, `pidfd_open` and
  `pidfd_send_signal` outright (believed from containerd 1.6
  and CRI-O 1.23, whose profiles gained the two capabilities;
  pin the minimum on kind) — with a `Localhost` profile, then
  `Unconfined`, as fallbacks for older runtimes.
  Found in the code: the tracer targets *host* PIDs (target_map
  is keyed by the kernel's tgid, `/proc` read as the host's), so
  in a pod's PID namespace `-pid` would name nothing. M7 makes
  targeting namespace-aware — the tracer passes its PID
  namespace and the BPF side keys by the pid at that
  namespace's level, walked the way `ior_current_namespace_tgid`
  (internal/c/filter.c) walks levels, since
  `bpf_get_ns_current_pid_tgid` matches only a task's innermost
  namespace and a pod may nest one — which also scopes the
  tracer to the pod by construction (built by task 4f4,
  internal/c/pidns.c; checked on the host in `unshare --pid`
  namespaces, nested ones included); `hostPID` is the rejected
  shortcut (it hands the app containers the node's process
  table). Fallback to `privileged` (which must not set
  `allowPrivilegeEscalation: false`; the API refuses the pair):
  a kernel before 5.8; a runtime that rejects the newer
  capability names; an SELinux-enforcing node whose policy has
  no `spc_t` (or denies it what the tracer needs); an AppArmor
  or seccomp default that cannot be relaxed per container; a
  node-level debug pod. Privileged does not help under kernel
  lockdown
  (confidentiality): the tracer's kernel reads are refused
  there. Overturned if: the kind run (or a fleet node) needs a
  capability beyond this list, or namespace-aware targeting
  proves unworkable (then a node-level tracer with `hostPID`
  takes over the in-pod design). Owner to confirm: node kernels
  and LSMs, runtime and seccomp defaults, and the Pod Security
  level of target namespaces — `baseline` and `restricted`
  refuse this set and `privileged` alike, so the tracer needs a
  `privileged`-level namespace or an exemption.
  On kind (task yb4, containerd 2.2.0, `RuntimeDefault`
  seccomp, no LSM active): the set sufficed — the tracer loaded
  its programs, attached through the tracefs hostPath, read BTF
  from `/sys`, live-attached the agent across containers, and
  ran with exactly these seven capabilities (CapEff
  0x1c001080022). Older runtimes were not run.
  On el9 (task zg4, Rocky 9.9 k3s node, SELinux enforcing,
  container-selinux 2.250, k3s-selinux 1.6, containerd 2.2.10,
  `RuntimeDefault` seccomp): the SELinux trigger above fires.
  The set is not short — the container type is: `container_t` is
  denied `bpf { prog_load }` (no boolean allows it), and
  Unconfined seccomp or an added `SYS_ADMIN` changes nothing.
  Measured on probe pods: `seLinuxOptions.type: spc_t` with
  these seven capabilities and `RuntimeDefault` attaches, and so
  does `privileged`. In permissive and then enforcing runs the
  tracer needed, beyond what `container_t` has: `bpf {
  map_create map_read map_write prog_load prog_run }`,
  `perf_event { open cpu kernel read write tracepoint }`, and
  `tracefs_t` `dir { search read open getattr }`, `file { open
  read getattr }`; with exactly that granted to `container_t`
  the whole acceptance passed and no AVC was logged. BTF from
  the container's sysfs needed nothing. So on an
  SELinux-enforcing node the choice is between `privileged`,
  `spc_t` (unconfined in SELinux, yet keeping the capability
  set, seccomp and no privilege escalation), and an iormesh
  container type shipped as a policy module with just the
  permissions above.
  *Provisional (2026-11-21, task 5h4, owner-delegated advisor
  decision):* `spc_t`. The minimal profile's tracer container
  carries `securityContext.seLinuxOptions: {type: spc_t, level:
  s0}` — container level only, never the pod's securityContext, so the
  app containers stay `container_t` — beside the seven
  capabilities, `RuntimeDefault` and `allowPrivilegeEscalation:
  false`, all unchanged. It is in `kubectl debug --custom`'s
  spec and the sidecar patch (`deploy/`); the prepare patch and
  the privileged profile (already `spc_t`) are unchanged. On a
  node without SELinux the field is inert. `iormeshctl deploy
  -selinux-type`, or `selinux_type` in the registry's `k8s:` or
  a pod entry (#18's precedence), picks another type; an empty
  one omits the field; a value must match `[a-z0-9_]+`. The
  `iormesh_container_t` policy module (task 7h4, below) is the
  opt-in confined alternative; `privileged` stays the last
  fallback. The level is not optional: given a
  type alone, containerd gives the container a fresh MCS pair
  and relabels the pod's shared files (`/etc/hosts`,
  `resolv.conf`, the trace `emptyDir`) to it, and on el9 the
  gateway's JVM (`container_t` at the pod's pair) was then denied
  reading `/etc/hosts` (task 5h4's first run); at `s0`, which
  every pair dominates, the app keeps access, and those files
  lose the pod's MCS pair: every `container_t` process on the
  node dominates `s0`, so MCS no longer separates them from
  other pods — only mount namespaces do, and a hostPath over
  `/var/lib/kubelet/pods` or an escape reaches them; the label
  stays after a debug tracer exits. Advisor confirmed `s0`
  (2026-11-21). For owners who pin pod levels (task 8h4):
  `iormeshctl deploy -selinux-level`, or `selinux_level` in the
  registry's `k8s:` or a pod entry (#18's precedence), sets the
  level — the pod's own pair, which keeps the shared files at
  it and so their MCS isolation; default `s0`; a value must be
  `s0` or `s0:cN,cM` with distinct N, M ≤ 1023; empty omits the
  level (the type-alone behaviour above); an empty type omits
  the whole field, level included. The sidecar patch is static
  at `s0`: its header tells such an owner to edit the level to
  the pod's pair.
  *Opt-in (owner, 2026-11-21, task 7h4):* the dedicated type
  `iormesh_container_t`, shipped as the `iormesh-selinux` RPM
  (`packaging/selinux/`: `iormesh.te`, the spec, `mage
  selinuxRPM docker|podman` building on Rocky 9 in a container;
  `%post` `semodule -X 200 -i`, `%postun` `semodule -X 200 -r`
  on erase — priority 200, the distribution modules', so 400
  stays free for local overrides; requires container-selinux;
  stop every `iormesh_container_t` tracer before `dnf remove`,
  or its processes become `unlabeled_t`). `spc_t` stays the default; an
  owner whose nodes accept the RPM deploys with
  `-selinux-type iormesh_container_t -selinux-level s0:cN,cM`
  (or the registry's `selinux_type`/`selinux_level`). The type
  comes from container-selinux's `container_domain_template`,
  as `container_t` does (entrypoint and management of
  `container_file_t`, MCS-constrained, `system_r`), with
  `container_t`'s attributes `container_net_domain`,
  `container_user_domain`, `sandbox_net_domain`,
  `sandbox_caps_domain` and `svirt_sandbox_domain`, and exactly
  these allow rules: the tracer's seven capabilities (task gh4),
  `self:capability { dac_override kill sys_ptrace sys_resource }`
  and `self:capability2 { bpf perfmon checkpoint_restore }`; zg4's
  `self:bpf { map_create map_read map_write prog_load prog_run }`,
  `self:perf_event { open cpu kernel read write tracepoint }`,
  `tracefs_t:dir { search read open getattr }` and
  `tracefs_t:file { open read getattr }`; and, for the Java
  attach (#14), `container_t:dir { search getattr }`,
  `container_t:file { open read getattr }`,
  `container_t:lnk_file read`, `container_t:process signal` and
  `container_t:unix_stream_socket connectto`. The second group
  came from an audit2allow review of a permissive run (the type
  permissive, dontaudit rules off) — zg4's diagnostic module had
  granted `container_t` itself, which owns the JVM's `/proc`
  entries and socket, so it needed none; without them BPF
  tracing passes and the agent never joins, with no AVC logged
  (container-selinux dontaudits a container domain searching
  another domain's `/proc/<pid>`). No wildcard, no permissive,
  no MCS exemption: unit tests pin the rule text. The module
  grants the seven capabilities itself, so the type no longer
  depends on container-selinux's `virt_sandbox_use_all_caps`
  boolean, through which six of them (all but `KILL`) reach
  `container_t` and, before task gh4, reached this type too (with
  the boolean off its BPF load failed on `capability2 { bpf
  perfmon }`). On el9 the M7 acceptance passed with the boolean
  off and on (task gh4); the boolean still matters to the app
  containers, `container_t`: with it off the workload's JVM,
  running as root, was denied `capability dac_override` opening
  its jar, so that run gave `container_t` back `dac_override`
  alone through a temporary local module. MCS is why the level
  must be the pod's pair, but it
  does not gate every rule: it gates the signal, the `file` and
  `lnk_file` reads, `dir` search and read, and writes to
  filesystem sockets, which at the pod's pair reach the pod's
  own app containers and any `container_t` at plain `s0`, and
  no pod at another pair; the pod's shared files keep the
  pair. `unix_stream_socket connectto` is not MCS-constrained
  (an abstract socket is bounded by the pod's network
  namespace), nor are `file { open getattr }` and `dir
  getattr` (bounded by what the tracer's PID namespace shows).
  Where owners pin one pair per Deployment or namespace, shared
  by its replicas, MCS separates none of those pods and
  containment rests on the namespaces alone. A tracer at another
  pair than the app's is kept out: on el9 a tracer at
  `s0:c100,c200` beside an app container at `s0:c300,c400` loaded
  its BPF programs, but its pattern never matched the JVM and
  `iormesh attach` found no such process, with `avc: denied {
  search }` on `/proc/<pid>` (`tcontext=container_t:s0:c300,c400`,
  logged with dontaudit rules off; task gh4). At `s0` the tracer
  can neither read the app's files through `/proc/<pid>/root`
  nor signal or search its processes, so the Java attach is
  denied (BPF tracing still works; this follows from the same MCS
  constraint and was not run at `s0`). An MCS exemption
  (`mcs_constrained` left out, or `mcsreadall`) would make `s0`
  work but let the tracer read any pod's `container_file_t`
  files and reach any container's processes its namespaces
  show — rejected. A pod that does not pin its level gets a
  random pair, so the type suits owners who pin MCS levels; a
  debug tracer does not discover the pair itself. On el9 (one
  Rocky 9.9 k3s node, the versions of 5h4) the M7 acceptance
  passed with it under iptables and nftables, no AVC, app
  containers `container_t` at the pod's pair
  ([testing.md](../testing/m7-acceptance/selinux.md#dedicated-type-iormesh_container_t-task-7h4-2026-11-21)),
  and `spc_t` still passed with the module absent; task gh4's
  RPM with the capability rules loaded and unloaded at priority
  200 and passed under iptables with the boolean off and on.
  Owner to confirm: `spc_t` and `s0`
  as the fleet default, no tenant hostPath to the kubelet
  directories, whether fleet pods pin MCS levels, where nodes
  accept the `iormesh-selinux` RPM (and so the opt-in type),
  OpenShift (its SCCs) out of scope.
- **#14 K8s Java attach** — *Provisional (2026-11-20):* live
  attach through the shared process namespace is the k8s
  default; `-javaagent` via `JAVA_TOOL_OPTIONS` stays the
  documented alternative for pods that restart anyway (with the
  sidecar: a debug tracer ends with its target container's
  restart, task ah4, see M7), for JVMs
  run with `-XX:-EnableDynamicAgentLoading`, and for a future
  JDK that disables dynamic loading by default (#2's caveats
  apply unchanged). The ephemeral container joins the target
  container's namespace (`kubectl debug --target`), so the JVM
  is never restarted for the attach — but the pod must already
  carry #13's two volumes (tracefs, the bounded trace dir), so
  a pod is prepared once by the deployment owner's patch (one
  rollout, no tracer container in it); a sidecar needs that
  patch plus `shareProcessNamespace: true`. The handshake is
  ge4's, unchanged: trigger file and
  socket reached through `/proc/<pid>/root` and `cwd`,
  `SO_PEERCRED` naming the target, as uid 0 (HotSpot accepts
  root or its own euid). M7 code: the JVM resolves the jar and
  spool paths in its own mount namespace, and an ephemeral
  container cannot add a volume to the app container, so the
  tracer stages both inside the JVM's root — a root-owned
  directory under its `/tmp`, created exclusively with
  `RESOLVE_IN_ROOT` and refused if it exists or is not root's,
  removed when the session ends — and reads the ring through
  `/proc/<pid>/root` (#27 foresaw this). That staging is
  bounded: the jar (≈280 KiB) and one
  ring per JVM of a fixed size (8 MiB by default, `ringsize=`
  at most 64 MiB; AgentArgs.java, Ring.java), charged to the app
  container's ephemeral storage — or its memory where `/tmp` is
  a tmpfs. Capabilities: #13's Java four. Overturned if: the
  owner's JVMs run with dynamic loading disabled or a
  read-only root without a writable `/tmp` (then `-javaagent`
  becomes the default), or the kind run shows the runtime's
  AppArmor profile blocks the cross-container `/proc` reads.
  Owner to confirm: JDK versions and flags in the fleet, and
  whether app containers keep a writable `/tmp`.
  Built (task cf4): `run -agent` stages for every JVM whose mount
  namespace is not the tracer's and whose PID namespace is (the
  agent names its ring by its own pid; a JVM in another PID
  namespace gets a note naming `-javaagent`). The JVM's `/` and
  `/tmp` must also be root's or the JVM user's and not writable
  by group or others unless sticky (the jar's trust rule, inside
  the JVM's root) — so an `emptyDir` mounted at `/tmp` (0777, not
  sticky) is refused too; a stage name already taken is skipped
  for a fresh random one, never adopted. Every check runs for
  every JVM; two JVMs share a stage only with the same euid and
  the same `/tmp` directory object (device and inode), not
  merely the same mount namespace (review of cf4). `iormesh
  attach` stages nothing: its spool belongs to a separate
  `run`, which would not read a stage it made. Proven
  in `mage integrationTest` with JVMs in private mount
  namespaces (`TestJavaAttachMountNS`). On kind (task yb4),
  before cf4: `run -spool` in the scratch image failed with a
  spool under `/tmp` (the image has none), and the jar path was
  the tracer's — so no in-pod tracer attributed Java. With the
  paths made to agree by hand (the app mounting
  the trace emptyDir, its image carrying the jar), live attach
  through the shared PID namespace worked: the agent joined and
  its http and dns events carried the gateway's FQDN. Neither
  AppArmor nor SELinux was active on that Fedora host, so the
  AppArmor question stays open.
- **#15 Route-map source of truth** — *Provisional
  (2026-11-20):* a static YAML map first, k8s API discovery
  second, Traefik's API and access logs not in v1. The map lives
  on the controller (`~/.config/iormesh/routes.yaml`, the
  registry's ownership and mode rules) and maps FQDN → ingress →
  service → deployment/statefulset; it works from outside any
  cluster. Discovery, once built, runs on the controller with
  the operator's kubectl (read-only `get` of Ingress,
  IngressRoute, Service, EndpointSlice and the owning workload),
  never from a tracer. Precedence: a static entry wins over a
  discovered one for the same FQDN; discovery fills only FQDNs
  the file lacks; observed http events (host + path from the
  agent) never change a route — a route they contradict is
  flagged in the build. Traefik's API needs a connection the
  controller does not otherwise make, and its logs need log
  access; both wait for a need. Overturned if: the owner's
  ingress routes change too often for a hand-kept file
  (discovery moves first), or the ingress is not Traefik.
  Owner to confirm: the ingress controller and CRDs in use, and
  a sample of real routes.
  Built (task xb4): the static map and discovery
  (docs/implementation.md M7 step 12); discovery writes a
  snapshot the build reads, so `build` itself never runs
  kubectl. Its `services:` section — ClusterIPs, ports,
  endpoints, the Service CIDR — is an addition the ClusterIP join
  needs (a route names a Service, not its address); a route's
  workload may be left out and is then the Service's (discovered
  routes leave it to the Service, so a static Service shadowing a
  discovered one keeps its workload). A route's optional
  `ingress_service` names the ingress controller's Service;
  discovery fills it from an Ingress's status addresses matching
  a discovered Service, else from `routes discover
  -ingress-service`. On kind (task yb4) Traefik's ClusterIP
  Service gave the Ingress no status address, so once Traefik's
  namespace was discovered too, only `-ingress-service` kept the
  gateway's edge the route's instead of a `route_contradicted`
  edge to Traefik's Service. A path-restricted ingress rule
  becomes a whole-host route: the map routes FQDNs, not paths. Scoping: the
  table applies to every flow of a build, whichever cluster or
  host it came from — a ClusterIP another traced cluster reuses
  would be named after this map's Service; the build refuses a
  snapshot whose context no registered cluster names, and one
  snapshot (the last discovered) serves the registry.
- **#18 K8s debug-container config in iormeshctl** —
  *Provisional — owner's examples pending (2026-11-20):* k8s
  targets live in the registry beside `servers:`, as a `k8s:`
  section of defaults and clusters plus a `pods:` list whose
  entries carry the same `projects:` as a server; flags
  override per run (`-context`, `-namespace`, `-image`,
  `-selinux-type`, task 5h4; `-selinux-level`, task 8h4). Order:
  flag, then pod entry, then cluster, then the built-in default
  image (ours, pinned by digest). Every kubectl call passes
  `--context` explicitly — the registry, never the kubeconfig's
  current context, picks the cluster, as it picks hosts for SSH.
  Mode `debug` (ephemeral container, the default) or `sidecar`
  (a patch the deployment owner applies); profile `minimal`
  (#13's set, via `kubectl debug --custom`) or `privileged`
  (`--profile=sysadmin`). Either mode needs a pod prepared with
  #13's volumes: before creating the ephemeral container,
  iormeshctl checks the pod spec for them and refuses an
  unprepared pod, naming the patch. Trace fetch (#12):
  `kubectl exec` of a tracer subcommand that takes no path — it
  streams only the configured trace dir's `ready/*.parquet`,
  each opened `O_NOFOLLOW` and sent only if a regular file — as
  a tar to stdout; the controller treats the stream as
  untrusted and unpacks it under collect's rules (only
  `*.parquet` directly in `ready/`, no links, no modes, sizes
  bounded), then removes what it took by a second exec — no
  `tar`, `kubectl cp` or rsync needed in the image. Schema
  sketch, placeholders only:
  [implementation.md](../implementation/m7.md#m7--kubernetes).
  *Wired (task af4, 2026-11-21, provisional like the rest):*
  commands name a pod entry `<cluster>/<namespace>/<pod>` or
  `<cluster>/<namespace>/<selector>` (the "/" sets it apart from
  a host); a project's name may be left out, and is then the
  namespace (#21). A `pod:` entry is one pod, refused when not
  running; a `selector:` (equality-based, `k=v[,k!=v…]`, at
  least one positive term — never empty) picks *every* running
  pod it matches, each its own session, at most 32 (more is
  refused as a selector broader than meant); one matching no
  running pod fails the command for that entry — a pod gone took
  its emptyDir, so it is never "nothing to collect". A debug-mode
  entry carries exactly one project (an ephemeral container runs
  one process); a sidecar's sessions are its patch's command
  line, and deploy only verifies it. Digest pinning holds in
  both modes: a tracer container already in the pod (the
  sidecar, or an earlier debug container, which is then left
  alone) must run the pinned digest by its status `imageID`;
  another digest, or an `imageID` without a registry digest, is
  refused — and an earlier debug container's spec image must carry
  the pinned digest too (iormeshctl always creates it so; the status
  is the node's word, the spec the API's). An image is needed to
  deploy, not to collect. A debug session runs `iormesh run
  -linger`: when its `-duration` ends it detaches and frees its
  BPF, publishes its last segment and then waits for SIGTERM
  instead of exiting, because an exited ephemeral container can
  never be exec'd into again and its last segments would be
  stranded in the emptyDir. A tracer container stopped anyway (an
  OOM kill, a crash, or an app container restart, which takes a
  debug tracer with it: task ah4) is reported by collect as
  "session ended", without an exec. A sidecar's segments are then
  stranded until the pod goes. In debug mode, deploy starts the
  next tracer (`iormesh-2` … `iormesh-8`), which serves them. Collect
  asks the API server which pods an entry is and whether each
  has a tracer container (none: nothing to collect, said), then
  fetches through `kubectl exec … iormesh ready`; a pod kubectl
  cannot reach fails, and a missing `ready/` is an empty stream —
  no `true`, `test` or `find` in the image — but a project with no
  trace dir at all in the container fails, so a project mismatch
  never reads as an empty collect. One fetch per local directory
  at a time (a flock, failing fast). Each pod's store directory is
  `pod-<uid>`, not its name (a StatefulSet pod recreated under its
  name is a new host), pinned in the build's host pins
  (`hosts.json`, beside the service database: collect's and
  refresh's `-db`, under a lock shared with build) to `host_id`
  `pod-<uid>` and the FQDN task 7f4 gives an in-pod tracer. Those
  pins grow with pod churn, one per pod incarnation ever
  collected, and will need pruning later. A selector pod that was
  replaced drops out of the selector's answer, and its emptyDir —
  whatever it had not yet been collected from — goes with it:
  collect often on churning workloads. `-context` may only restate
  the entry's cluster's context, and neither `-context` nor
  `-namespace` is taken by rm or by a collect of every target.
  rm collects one last time, then drops
  the entry; nothing in the pod is stopped (an ephemeral
  container cannot be removed or signalled without a shell), so
  the session runs until its `-duration` or the pod ends.
  Overturned by: the owner's examples. Owner to provide: real
  contexts, namespaces, the image registry, and how pods are
  picked (labels or names).
