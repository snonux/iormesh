# Kubernetes

Part of [Security & scope](../security.md).

- **Kubernetes** (M7; decisions #13, #14, #18 are provisional —
  decisions.md "Provisional — M7"): eBPF privileges are
  node-kernel-wide — a container with `CAP_BPF` and
  `CAP_PERFMON` can in principle read any process's memory and
  the kernel's. What the minimal profile buys over `privileged`
  is no devices, no mounts, no namespace escapes and no
  `CAP_SYS_ADMIN`; it does not make BPF pod-local. The tracer
  container:
  - runs as uid 0, `allowPrivilegeEscalation: false`, in a pod
    with `hostUsers: true`, drops ALL and adds `BPF`, `PERFMON`,
    `SYS_RESOURCE` (libbpfgo's memlock bump, not the kernel),
    plus `SYS_PTRACE`, `DAC_OVERRIDE`, `KILL` and
    `CHECKPOINT_RESTORE` when it attributes Java — each traced
    to the code that needs it in decisions.md #13 and checked by
    leaving it out
    ([testing.md](../testing/k8s-tracer.md#k8s-capability-set-task-ub4));
  - runs as SELinux type `spc_t` at level `s0` — the tracer
    container alone, never the pod, so the app containers keep
    `container_t` and the pod's MCS pair. The `s0` level keeps
    the pod's `/etc/hosts`, `resolv.conf` and trace `emptyDir`,
    which the runtime relabels to the tracer's level, open to the
    app; they no longer carry the pod's own MCS pair. Every
    `container_t` process on the node dominates `s0`, so MCS no
    longer separates these files from other pods — only mount
    namespaces do, and a hostPath over `/var/lib/kubelet/pods`
    or an escape reaches them; the label stays after a debug
    tracer exits. An owner who pins the pod's MCS level gives
    the tracer that same pair (`iormeshctl deploy
    -selinux-level s0:cN,cM`, or `selinux_level` in the
    registry; in the sidecar patch, its `level:` line), which
    keeps the files at the pod's pair and their MCS isolation. On
    an SELinux-enforcing node (el9) `container_t` is denied
    `bpf { prog_load }` outright (task zg4); `spc_t` is
    unconfined by SELinux, so on such a node the capability set,
    `RuntimeDefault` seccomp and no privilege escalation are what
    bound the tracer. `iormeshctl deploy -selinux-type` (or
    `selinux_type` in the registry) names another type, empty
    omits it. Opt-in where nodes accept the RPM: the
    `iormesh-selinux` policy module (`packaging/selinux/`,
    `mage selinuxRPM`) confines the tracer to its own type,
    `iormesh_container_t` — container-selinux's container
    template as for `container_t` (MCS-constrained), plus only
    the tracer's seven capabilities, `bpf`, `perf_event`,
    read-only tracefs, and the Java attach to `container_t`
    processes (their `/proc` entries, one signal, the attach
    socket). Granting the capabilities itself, the type does not
    depend on container-selinux's `virt_sandbox_use_all_caps`
    boolean (task gh4: M7 passed on el9 with it off and on); the
    app containers, `container_t`, still get six of those
    capabilities only through it. Run it at the pod's own MCS
    pair (`-selinux-type iormesh_container_t -selinux-level
    s0:cN,cM`), and the pod's shared files keep the pair. MCS
    then confines the signal, the `/proc` file and link reads and
    directory search, and filesystem-socket writes to the pod's
    own app containers (and any `container_t` at plain `s0`); a
    tracer at another pair than the app's was denied the search
    of the JVM's `/proc/<pid>` on el9 and never attached (task
    gh4), and at `s0` the Java attach is denied the same way. It
    does not confine the
    attach socket's `connectto` (an abstract socket, bounded by
    the pod's network namespace) or `/proc` `open`/`getattr`
    (bounded by the tracer's PID namespace). Where owners pin one
    pair per Deployment or namespace, shared by replicas, MCS
    separates none of those pods and containment rests on the
    namespaces. Stop its tracers before removing the RPM, or they
    become `unlabeled_t`. The rules and
    the el9 run are in decisions.md #13 and
    [testing.md](../testing/m7-acceptance/selinux.md#dedicated-type-iormesh_container_t-task-7h4-2026-11-21)
    (tasks 7h4, gh4);
  - mounts the host's tracefs read-only and nothing else of the
    host; traces live in a size-bounded `emptyDir` of the pod.
    The pod must carry both volumes before the tracer comes
    (an ephemeral container cannot add one), so it is prepared
    once by the deployment owner's patch;
  - scopes itself to the pod by its PID namespace: targeting
    keys by the pid in the tracer's own namespace (task 4f4), so
    a process outside the pod cannot be named as a target (the
    kernel side gives it no number at all), and `SYS_PTRACE` and
    `KILL` reach only what that namespace's `/proc` shows — the
    pod. `DAC_OVERRIDE` is not
    so bounded: it overrides file permissions on everything the
    container's mount namespace holds — its own image and
    volumes, and through `/proc/<pid>/root` the pod's other
    containers, whose files a pod's root could not otherwise
    open. `hostPID` is not used: it would give the app
    containers the node's process table too;
  - stages the agent jar and spool inside the JVM's root for a
    live attach (decisions.md #14; built by task cf4): a root-owned
    0755 directory under the JVM's `/tmp`, created exclusively
    (`mkdirat` on the `RESOLVE_IN_ROOT` fd) under a random name
    — a name that already exists is never adopted, the JVM's
    user could have planted it; the next fresh name is tried —
    and checked to be root's on its fd; the jar copied in with
    `O_EXCL|O_NOFOLLOW`; the spool beside it 1733, read through
    its held fd, its rings checked as on a host. Refused unless
    the JVM's `/` and `/tmp` are root's or the JVM user's and
    not writable by group or others without the sticky bit:
    otherwise another user could rename the directory away and
    have the JVM load their own jar (an `emptyDir` at `/tmp`,
    0777 without the sticky bit, is refused for that). The
    checks run for every JVM, and a JVM shares another's stage
    only with the same euid and the same `/tmp` directory object.
    A JVM in another PID namespace than the tracer's is not
    staged for: its ring's name would be read as another
    process's pid. It costs the app
    container's ephemeral storage, or its memory where `/tmp` is
    a tmpfs (the jar and one fixed-size ring per JVM). The
    tracer removes the directory through its fds when its
    session ends; a session killed before that leaves it to the
    container's lifetime — a later session does not remove it,
    since it cannot tell a dead session's stage from a live
    one's;
  - is reached only through the operator's kubectl, with the
    cluster named by the registry (`--context` always explicit,
    never the kubeconfig's current context) — the registry stays
    the deploy boundary. The operator's RBAC for this —
    `patch` on `pods/ephemeralcontainers` and `create` on
    `pods/exec` in a namespace at the `privileged` level — is
    itself node-wide read access: whoever holds it can start a
    BPF-capable container there, so grant it like root on the
    nodes;
  - comes as an image pinned by digest — the static tracer and
    the agent jar on `scratch`, no shell or package manager
    (task vb4; the manifests refuse a tag). Nothing signs or
    publishes the image yet: `DefaultImage` is empty
    (internal/k8s/target.go), so a deploy names the operator's
    own digest-pinned build (`-image` or the registry's
    `image:`) and is refused without one. The digest pins the
    bytes; no signature vouches for them. (The release
    archives of the host builds ship unsigned too; the
    operator signs those with their own key, decisions.md
    #32.) A sidecar is added only by
    the deployment owner's explicit patch, a debug container
    only by the operator's `iormeshctl`. The patches and the debug
    profile are generated into `deploy/` from internal/k8s and
    test-checked field by field
    ([testing.md](../testing/k8s-controller.md#k8s-manifests-and-pod-identity-task-vb4)).
    `iormeshctl deploy` checks a tracer container already in the
    pod — the sidecar, or an earlier debug container — against
    its status `imageID`, the digest the kubelet pulled, and
    refuses another digest or an `imageID` naming none (a
    locally loaded image), so a sidecar patch that names a tag
    still runs only the pinned bytes (task af4). A debug tracer
    that ended (killed with its app container) is replaced by
    one under the next name, `iormesh-2` up to `iormesh-8`. The
    new one is created with the pinned `--image` too, never
    while an earlier one still runs, and never past eight per
    pod (task ah4). Collect does not check the image. It execs
    into the newest container with a tracer name, whatever image
    it runs. A principal allowed to add ephemeral containers to
    the pod could name one `iormesh-N` and have collect read its
    stream. That stream is already treated as untrusted:
    unpacked under collect's rules into the pod's own
    `pod-<uid>`, pinned from the API server. Such a principal
    can also exec into the pod's tracer directly, so collect
    adds no new reach. A later `deploy` checks that container's
    digest and refuses it;
  - is collected into a store directory named by the pod's UID
    (`pod-<uid>`), pinned to the host identity the API server's
    answer implies before any segment arrives — never by what
    the pod's own segments claim (task af4);
  `privileged: true` is the fallback where the minimal profile
  cannot run (old kernels, an SELinux policy without `spc_t`,
  runtimes without the newer capability names; it cannot be combined
  with `allowPrivilegeEscalation: false`); it widens what a
  compromised tracer could do to the whole node, so use it only
  where the minimal set was tried. On an SELinux-enforcing node
  a `container_t` tracer is refused `bpf { prog_load }`; the
  tracer's load error then names SELinux and its context instead
  of libbpf's memlock hint (task 6h4; the denial shows in the
  raw audit.log, not always in `ausearch -m avc`), and the el9
  choices are decisions.md #13's (task 5h4). Pod Security `baseline` and
  `restricted` refuse both profiles: the tracer needs a
  namespace at the `privileged` level or an exemption — the
  owner's call per namespace.
