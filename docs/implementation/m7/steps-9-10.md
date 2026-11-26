# M7 steps 9–10: transport and registry

Part of [M7 — Kubernetes](../m7.md), in [Implementation
guide](../../implementation.md).

9. `iormeshctl` gains a k8s transport implementing the same
   interface as SSH:

   ```go
   // Transport is how the controller reaches a tracer.
   type Transport interface {
       Run(ctx context.Context, argv ...string) ([]byte, error)
       Fetch(ctx context.Context, remoteDir, localDir string) error
   }
   ```

   M4 codes against this interface from day one, with SSH as the
   only implementation. The k8s `Run` is `kubectl --context C -n
   NS exec POD -c iormesh -- iormesh …`; `Fetch` runs a tracer
   subcommand that takes no path: it streams only the
   configured trace dir's `ready/*.parquet`, each opened
   `O_NOFOLLOW` and sent only if `fstat` shows a regular file,
   as a tar to stdout. The controller treats the stream as
   untrusted: it unpacks only `*.parquet` entries without a
   directory part (no links, no modes, sizes bounded —
   collect's rules), then removes what it took with a second
   exec. No `tar`, `kubectl cp` or rsync in the image.

   Built by task wb4 (`internal/k8s`, cgo-free): `Kubectl`
   implements `federate.Transport` (whose third method, `Push`,
   it refuses — the image carries the tracer, and a deploy is a
   debug container), and adds `Check` and `Debug`. `Resolve` layers
   flag, pod entry, cluster and `k8s:` defaults (#18), refuses a
   context the registry does not list, an image not pinned by
   digest (`DefaultImage` is empty until task yb4 publishes one)
   and non-Kubernetes names. Every call passes `--context=` and
   `--namespace=`; `Run` is `exec POD --container=iormesh --
   argv…`, and returns `*exec.ExitError` only when kubectl's
   stderr ends with "command terminated with exit code N", N its
   own status — kubectl's own failures (forbidden, pod gone,
   unknown context) exit 1 too and come back as `ErrUnreachable`,
   so `test` never reads an unreached pod as "no"; `Fetch` accepts
   only
   `/opt/iormesh/data/traces/<project>/ready`, execs `iormesh ready
   -project P` (internal/cli/ready.go, writer `k8s.WriteReady`),
   unpacks with `k8s.Unpack` (regular `*.parquet` entries without
   a directory part, ≤ 1 GiB each and 8 GiB in all, 0600, through
   an `os.Root` and a dot temporary fsynced before its rename,
   the directory fsynced once after the last; at most 64 KiB
   after the tar's end) and removes what arrived with `iormesh
   ready -project P -remove -- NAME…`, also after a stream
   refused part-way — but only what is durable: a failed file
   sync drops that segment, a failed directory sync all of them,
   and they stay on the tracer for the next collect (task ii4).
   `Debug` reads the pod (`get pod -o json`), refuses it
   without the `tracefs` hostPath and a `sizeLimit`ed
   `iormesh-traces` emptyDir (naming the prepare patch), with
   `hostUsers: false`, without the app container, or with an
   `iormesh` ephemeral container already (ephemeral containers
   cannot be removed), then runs `debug POD --container=iormesh
   --target=APP --image=IMG --attach=false
   --profile=general|sysadmin --custom=FILE -- argv…`, the custom
   file step 8's spec. Tests: a fake kubectl on `PATH`
   ([testing.md](../../testing/k8s-tracer.md#k8s-transport-task-wb4)).
   `--custom` needs kubectl 1.30 or later: on kind (task yb4)
   1.31 and later take it by default, 1.30 only behind
   `KUBECTL_DEBUG_CUSTOM_PROFILE=true` — which `execRunner` now
   sets on every kubectl it runs — and 1.29 not at all
   (docs/build.md). Wired by task af4 (step 10). On kind,
   `--profile=general` plus `--custom` stored exactly the
   custom capabilities, and `kubectl debug … -- iormesh run`
   sets the container's `command` (the image's `PATH` finds
   `iormesh`; its `ENTRYPOINT` is replaced).
10. Registry: the `k8s:` and `pods:` sections (#18). **Provisional
    — owner's examples pending**; every value below is a
    placeholder, not a fleet fact:

    ```yaml
    # ~/.config/iormesh/servers.yaml (same file, same 0600 rules)
    k8s:
      image: <registry>/iormesh@sha256:<digest>  # default: ours
      mode: debug                    # debug | sidecar
      profile: minimal               # minimal (#13) | privileged
      clusters:
        - name: <cluster-label>      # what pods: entries name
          context: <kube-context>    # always passed as --context
          namespace: <namespace>     # default; also the project (#21)
    pods:
      - cluster: <cluster-label>
        namespace: <namespace>       # optional: the cluster's otherwise
        selector: <label>=<value>    # or pod: <pod-name>
        container: <app-container>   # kubectl debug --target
        projects:                    # as under servers:
          - name: <project>
            target: {pattern: "<glob>"}
            flags: ["-spool=<dir>", "-agent=<jar>"]
    ```

    Flags `-context`, `-namespace` and `-image` override per run;
    precedence flag > pod entry > cluster > `k8s:` default. A
    context the registry does not list is refused, as an
    unregistered host is.

    Built by task af4 (the rules: decisions.md #18's "Wired" note).
    `internal/k8sname` holds the name rules both sides check —
    `internal/federate` when it loads the sections
    (`registry_k8s.go`: types, validation, `PodRef`,
    `PodProjects`, `RemovePod`; a pod entry may also set `image`,
    `mode` and `profile`), `internal/k8s` before every argv — since
    k8s imports federate. In `internal/k8s`: `ResolveScope` (the
    layers without a pod; `Resolve` is it plus `Scope.Target`),
    which now lets an empty image through for exec and fetch while
    `Debug` and `Deploy` refuse it; `Lister` (`pods.go`: `Get`,
    `Select` over `get pod(s) -o json`, returning name, UID and
    whether the mode's tracer container is there); `Deploy`
    (`deploy.go`: one `get`, the prepared-pod checks, then start,
    leave a running tracer of the pinned digest alone, or verify a
    sidecar — the `imageID` check); `lockDir` (`lock.go`, a
    non-blocking flock on `.fetch.lock` around `Fetch`, wb4 review
    N9); `PodHostID` and `PodFQDN`, the identity task 7f4's tracer
    reports. In `internal/ctl`: `pods.go` (flags, `podEntry`,
    `resolvePodRef`, the run argv `iormesh run -project P
    <target> <flags>`, `-trace-dir` pinned to the emptyDir),
    `pods_collect.go` (per pod: the pin in `hosts.json` beside the
    service database — refresh's `-db` included — then `Fetch`
    into `<store>/<project>/pod-<uid>`), `pods_deploy.go`,
    `pods_rm.go`; collect, refresh, deploy and rm take pod
    references beside hosts, and deploy needs the signed build and
    key only for hosts. `federate.ValidServer` refuses a registry
    host shaped like `pod-<uid>`, so no server shares a pod's
    directory. Tests: a fake kubectl on `PATH` in both packages
    ([testing.md](../../testing/k8s-controller.md#k8s-controller-wiring-task-af4)).
    On kind (task yb4): pushed to a local registry, a pod's
    `imageID` names the registry digest (the manifest list's for
    a BuildKit build), and deploy, collect, `routes discover` and
    build ran against a real API server. Two fixes came of it: a
    selector no longer matches a terminating pod (`Select` skips a
    `deletionTimestamp`: right after the prepare patch's rollout
    the old pod still reported `Running` and deploy failed on
    it), and `build` takes `-registry` (refresh shares its own),
    since the snapshot's context check and `routes.yaml` follow
    the registry. Deploy returned before the tracer was attached,
    so a workload started on its word went untraced (two tcp-nat
    runs within ~8 s of deploy fused nothing). Since task ef4 a
    debug tracer, new or left running, is reported only once it
    says it is attached: `Kubectl.WaitAttached` (`attach.go`)
    execs `iormesh ready -project P -state` once a second, which
    prints `pending` until the run's `state.json` exists (written
    once every probe is attached, `markRunning`), then `running
    <session>`, `ended <reason>` or `dead`. A running session
    ends the wait (deploy prints `attached, session S`); an ended
    or dead one, or an exec failure with the tracer container
    `terminated` in the pod's status, fails at once; a failed exec
    while the container is still waiting (image pull) is retried;
    `-attach-timeout` (default 2 min) bounds it with
    `ErrNotAttached` naming the last answer and `kubectl logs`.
    The state, not the log's `attached` line, is the signal: it is
    a file the run writes for exactly this, readable without a
    shell, and the one-per-pod emptyDir holds no earlier pod's.
    Hardened by task 5g4: `state.json` is the pod's to write, so
    `parseAttach` takes only what iormesh writes — a session id of
    8 lowercase hex (the one deploy prints), an end reason of
    `[a-z_]`, no word after `pending` or `dead` — and refuses
    anything else, quoted. Each probe runs under its own 30 s
    bound (`attachProbeTimeout`), not the wait's deadline, so a
    probe in flight at the deadline still answers and the message
    names it; the wait can so end up to 30 s late. `iormesh ready
    -state` exiting non-zero in the running container (a remote
    exit status) fails at once, as does an earlier debug tracer
    left running whose command line's `-project` (else the
    namespace, #21) is not the entry's (`verifyProject`): no wait
    changes either. A cancelled context (Ctrl-C) ends the wait as
    "interrupted", wrapping `context.Canceled`, not as a timeout
    (and naming the signal, the root context's cause, task pi4).
    Also from yb4: a route discovery get refused by RBAC read
    `k8s: pod not reached` (`ErrUnreachable`'s text); it is
    `ErrAPI` ("API request failed") with kubectl's reason now,
    taken from the runner's `kubectlFailure` beside the
    `ErrUnreachable` (task 5g4), not trimmed from its text.
    Review round 1 (task af4): the debug container's run is
    `iormesh run -linger` (`internal/cli/linger.go`: after
    `runTrace` returns — `finish` has closed the tracer and
    published the last segment — it waits for SIGTERM; `start`
    neither takes nor forwards the flag), `PodState.Stopped` from
    the tracer's container status, the spec-image digest check
    for an earlier debug container, `pinNewHosts` re-reading
    `hosts.json` under `hosts.json.lock`, `collect -db`, and
    `iormesh ready` failing for a project with no trace dir.
    App container restarts (task ah4, milestones.md M7): a debug
    tracer shares the app container's PID namespace, so it dies
    with that container's init (exit 137). An ephemeral container
    is never restarted, removed or renamed. So
    `internal/k8s/tracers.go` names a pod's successive debug
    tracers `TracerName(n)`: `iormesh`, then `iormesh-2` up to
    `MaxDebugTracers` (8). `tracerIndex` accepts exactly those
    names and nothing like `iormesh-02` or `iormesh-9`.
    `Deploy`'s `deployDebug` looks at the newest one. If it has
    not terminated, it is the pod's tracer: `keepRunning`
    applies the spec-image, `imageID` and project checks and
    leaves it alone, and one still waiting is refused, not
    replaced. If it terminated, `nextTracer` requires every
    earlier one to have terminated too (one tracer per pod) and
    the count to be under the cap, and `debug` starts the next
    name with the same pinned `--image`, `--custom` spec and
    `PodDiskArgs` (outcome `Replaced`). `Target.Tracer` names
    the container that `exec`, `Fetch` and the attach probe
    use. `Deploy` sets it on its `Kubectl`, and collect takes it
    from `PodState.Container`, the newest tracer the `Lister`
    reports. After a replacement the predecessor's
    `state.json` stays in the shared project dir until the new
    run's `markRunning` replaces it. A deploy rerun that leaves
    `iormesh-N` (N > 1) running is in the same spot, since that
    tracer may still be attaching (`Kubectl.superseded`).
    Task eh4 tells the two states apart by time. `readyState`
    ends `running`, `ended` and `dead` with
    `started=<RFC3339Nano>`, the state's `StartedAt`
    (`k8s.AttachStartedKey`). `parseAttach`'s `splitStarted`
    cuts that word off and `parseStarted` reads it strictly:
    at most 35 bytes, RFC3339 or 1 to 19 digits of unix ns, in
    [2020, 2200), else the wait fails quoting the answer. On
    the first answer that carries a time, `attachProbe` reads
    the pod once (`containerStart`) for the new container's
    `running.startedAt` and keeps it in `predecessor.start`,
    since an ephemeral container never restarts.
    `predecessor.whose` then decides. A `StartedAt` more than
    `attachSkew` (2 s) before the container's start is the
    predecessor's, waited past, and the timeout names both
    instants. Anything later is the new run's own: `running`
    returns its session, `ended` or `dead` fails at once and
    names the container's logs. Both stamps are the same
    node's clock (kubelet or runtime, and the tracer's own
    `time.Now`); the window covers the status's whole seconds
    and a small step. Without a time to compare (no `started=`
    from an older image, a failed pod read, no running status
    yet), ah4's rule is the fallback. Only the first `dead` or
    `ended` answer is taken as the predecessor's. An answer
    that differs from it (`dead` turning into `ended`, or
    another `started=` time) comes from the new run's own
    `state.json`, so the wait fails at once. A predecessor's
    answer judged by time is remembered as that first answer
    too. A new run that fails before attaching writes no
    `state.json` and lingers under `-linger`, so its container
    keeps running and the predecessor's state stays. The wait
    then times out, saying the run may have failed and naming
    `kubectl logs --container=iormesh-N`. A run that crashes
    terminates its container, which the probe catches. The new
    run's `segment.New` takes the `.lock` the dead run's flock
    no longer holds, sweeps its `.active-*` files and publishes
    beside its sealed segments, which `iormesh ready` then
    serves. Collect's "session ended" error for a stopped debug
    tracer says to run deploy, or, when it is `iormesh-8` (the
    cap), to restart the pod or use sidecar mode. Sidecar mode is
    unchanged and is the recommendation for pods that restart.
