# Graph

Part of [Trace schema (v1, frozen)](../schema.md).

```json
{"schema_version": 1,
 "nodes": [{"type": "process", "id": "proc:<host>/<pid>@<inc>",
            "attrs": {"comm": "java"}}],
 "edges": [{"type": "connected", "from": "proc:…", "to": "conn:<id>",
            "state": "fused"}]}
```

| Node | Id |
|------|----|
| `host` | `host:<host_id>` — a pod for an in-pod tracer |
| `process` | `proc:<host_id>/<pid>@<pid_incarnation>` — for an in-pod tracer `host_id` is the pod's, so the pod-local pid stays unique ([Pods](#pods-as-hosts-m7)) |
| `file` | `file:<host_id>:<path>` — the path exactly as the open event reports it, or the new name of a rename that moved an open fd |
| `socket` | `conn:<conn_id>` — one node per connection, both ends: a TCP or unix stream connection, a UDP flow, a pipe or socketpair (M2 step 7); in a merged graph (M4) a TCP connection fused across traces has a merged conn_id both hosts' ids map to |
| `app` | `app:<project>/<pattern>` — the identity of a pattern-bound session's app (M2): every process incarnation the pattern matched links to it, so a restarted app is a new process node under the same app node |

| Edge | From → to | State |
|------|-----------|-------|
| `runs_on` | process → host | — |
| `opened` | process → file: opened under that name, or held open while a rename moved it there | — |
| `connected` | process → socket | `fused` (both ends traced and joined) or `unresolved` |
| `incarnation` | app → process | — |
| `forked` | process → process: a traced process forked the child (M2) | — |
| `signaled` | process → process: a traced process sent the other a signal (M2 step 7, signal family) | — |

Node attributes stay minimal (`comm` on processes): raw pids or
ports in attributes would survive normalization and make two runs'
graphs differ. A process node's `comm` is the process's newest
name: an exec does not make a new incarnation, so a forked child
that execs keeps its node, and the node is named after the program
it runs, not after its parent; the events keep each moment's
name. An `app` id names nothing run-specific (project and
pattern), so the normalizer passes it through unchanged and two
runs — or two hosts — name the same app the same way; goldens bind
it with `$a1`. A `-pid` or `-listen-port` session has no app node.

**Rebuilt from the events** (decision #3; task ze4): every node
and edge follows from the events in `seq` order — process nodes
from any event naming the process (an `attach` covers a target
that never made a traced call), `app`/`incarnation` from an
`attach` with `args.pattern`, `forked` and the child's inherited
fds from `fork`, `opened` from the opens and from renames moving
the open fds the events track (the fd lifecycle above),
`connected` from `conn_id` (fused for a pipe or socketpair, a
connect and an accept on the host, or mirrored address pairs of
a loopback UDP flow), `signaled` from a `signal` whose sender is
traced. `iormeshctl build` does this
(`internal/federate/rebuild.go`), and the integration suite holds
every single-host run's rebuild to its `graph.json`.

## Pods as hosts (M7)

An in-pod tracer (all three downward-API variables `POD_NAME`,
`POD_NAMESPACE`, `POD_UID` set; milestones.md M7, task 7f4) describes
the pod, not the node: `host_id` is `pod-<uid>` and the session's
`fqdn` `<name>.<namespace>.<uid[:8]>` (the UID prefix tells apart
a pod recreated under the same name). So `host:pod-<uid>` is the
pod's host node, and `proc:pod-<uid>/<pid>@<inc>` keys a process
by the pod — its pids are the pod's PID namespace's, unique only
within the pod, and one tracer per pod (milestones.md M7) makes the
pod and the PID namespace one. Two pods of one node with the same
pod-local pid and start tick are two process nodes. The pods of a
node share the session's `boot_id`, which groups them by node.
The tracer refuses to start when only some of the three variables
are set or one is malformed (a lower-case UID as the API server
issues it — a static pod's kubelet hash is refused; an RFC 1123
subdomain and label; an FQDN of at most 190 bytes, so every
segment name, and the controller's unpack temporary of it, fits
255) rather than fall back to a machine-id,
which the image does not carry and which would not tell pods
apart. The controller derives the same `host_id` and `fqdn` from
the pod's API metadata and pins the pod's store directory
(`pod-<uid>`) to them, so a session claiming another identity is
dropped; iormeshctl checks the same before it deploys a tracer
into a pod. Outside a pod
nothing changes.

## Pod fields on events (M7)

An in-pod tracer also stamps every event with the `pod` object
(milestones.md M7 "Pod fields on events", task sh4), last in the
envelope:

```json
"pod": {"namespace": "prod", "name": "checkout-7d9f",
        "container_id": "4f6c0f1e…7b6c5d"}
```

| Key | Meaning |
|-----|---------|
| `namespace` | the pod's namespace (`POD_NAMESPACE`), an RFC 1123 label |
| `name` | the pod's name (`POD_NAME`), an RFC 1123 subdomain |
| `container_id` | optional: the 64-hex runtime id of the container the event's process runs in, as `kubectl get pod -o json` shows it after `containerd://` |

- The pod is the host already (`host_id` `pod-<uid>`), so the UID
  is not repeated; a `pod` object on any other host id is
  invalid.
- `container_id` comes from the process's `/proc/<pid>/cgroup`,
  read once when the tracer meets the process (its attach or
  first record) and inherited at fork — never per event. Inside
  the tracer's private cgroup namespace a sibling container reads
  `0::/../cri-containerd-<id>.scope` (systemd driver: kind, k3s
  on el9) or `0::/../<id>` (cgroupfs); the full kubelet paths of
  both drivers are read too. It is absent on host-level events
  (`session`, `drop`, `end`), for the tracer's own processes
  (their cgroup reads `0::/`), for a process in the pod cgroup
  itself and for one whose cgroup names another pod. In sidecar
  mode with a shared PID namespace it tells the app's, the
  sidecars' and the tracer's processes apart. The pod sandbox's
  `pause` (pid 1 there) has a scope of its own,
  `cri-containerd-<sandbox id>.scope`, so its events would carry
  the sandbox's id, which no container status lists (k3s on el9,
  task 6k4; pause does no traced I/O in practice). The build
  shows that id bare, never a guessed name
  ([decisions.md #34](../decisions.md)).
- The container's *name* is not in the cgroup and the tracer has
  no API access; the controller joins it (milestones.md M7 "Pod fields
  on events", task jk4). Every collect of a pod records the pod's
  container map, from the status the API server reports, in
  `<store>/<project>/pod-<uid>/containers.json` beside the
  segments:

  ```json
  {"4f6c0f1e…7b6c5d": "app", "9a0b1c2d…3e4f5a": "sidecar"}
  ```

  Each id is a `containerID` of `containerStatuses`,
  `initContainerStatuses` or `ephemeralContainerStatuses` (the
  running one and `lastState.terminated`'s) without its
  `containerd://`, `cri-o://` or `docker://` scheme — the form
  `container_id` has. An id keeps its first name, and the map
  only grows. `iormeshctl build` reads it offline: each process
  in the model (and `show processes`) carries `container_id` and
  `container`, the name. Without a map (a session collected
  before task jk4) or for an id it lacks, the id is shown. A map
  that does not read (bad JSON, a bad id or name, over 1 MiB, a
  symlink, a FIFO or other non-regular file) names nothing and is
  listed, with why, under health's ignored entries; collect then
  refuses to merge into it and says to remove it, and the next
  collect writes it anew from the pod's current status.
- The Java join's `http` and `dns` events carry their JVM's.
- In parquet the three keys are `pod.namespace`, `pod.name` and
  `pod.container_id`, dictionary-encoded. A segment written
  before task sh4 has no pod columns and reads as before, with
  no `pod`.
- The controller holds each event's `pod` to its session's
  pinned FQDN (`<name>.<namespace>.<uid[:8]>`); a segment that
  claims another pod is left out of the build as bad.
  `iormeshctl build -namespace NS` keeps only the sessions whose
  pinned identity is a pod in NS — segments from before task sh4
  included — and writes `<projects>@NS/`, read by `show
  -namespace NS` ([kubernetes.md](../kubernetes.md#collect-and-build)).
