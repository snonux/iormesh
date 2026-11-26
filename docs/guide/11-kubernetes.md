# 11. Kubernetes

Pods are traced from the inside: the tracer runs as a container
in the target pod, sees only that pod's processes, and the pod
becomes a host of its own (`pod-<uid>`) in the store and the
mesh. The rest of the loop — collect, build, show, query,
ensure — is the one you already know. This chapter is the path;
[kubernetes.md](../kubernetes.md) is the runbook with every
requirement, limit and error.

## What changes for pods

| Hosts | Pods |
|---|---|
| a signed `dist/` and the minisign key | a tracer image you build and pin by digest; no `dist/`, no key |
| SSH and rsync | your own `kubectl`, always with the registry's `--context` |
| `add` writes the registry | pod entries (`k8s:` and `pods:`) are written by hand |
| `bootstrap` once, with sudo | the workload owner applies a prepare patch once (one rollout) |
| the session is a systemd unit | the tracer is a debug (ephemeral) container or a sidecar |
| segments in `/opt/iormesh/traces` | segments in the pod's `emptyDir`, until collect fetches them |

## The path

1. **Prerequisites** — kubectl 1.30 or later on the laptop, a
   node kernel the tracer supports, the namespace at Pod Security
   level `privileged`, and the operator's RBAC
   ([kubernetes.md](../kubernetes.md#prerequisites)).
2. **Image** — build the tracer image from `dist/` and the agent
   jar, push it, and pin it by digest
   (`<registry>/iormesh@sha256:…`); a tag alone is refused
   ([kubernetes.md](../kubernetes.md#tracer-image-build-and-pin-your-own)).
3. **Prepare the pods** — the workload owner applies
   `deploy/prepare-patch.yaml` once
   ([kubernetes.md](../kubernetes.md#prepare-the-pods-both-modes)).
4. **Register** — add the `k8s:` defaults, the cluster and a pod
   entry (by pod name or label selector) to
   `~/.config/iormesh/servers.yaml`
   ([kubernetes.md](../kubernetes.md#the-registry-k8s-and-pods)).
   A pod entry is named on the command line as
   `<cluster>/<namespace>/<pod>` or
   `<cluster>/<namespace>/<selector>`, e.g.
   `prod/shop/app=checkout`.
5. **Pick a mode** — debug for a look at a pod that stays up;
   sidecar for pods that restart or long tracing
   ([kubernetes.md](../kubernetes.md#debug-or-sidecar)).
6. **Deploy and check** —
   `iormeshctl deploy prod/shop/app=checkout` starts a debug
   tracer in every running pod the entry names and waits until it
   attached; `iormeshctl status prod/shop/app=checkout` reports
   it ([kubernetes.md](../kubernetes.md#deploy)).
7. **Collect, build, look** — `collect` (or `refresh`) and
   `build` as in [chapter 6](06-collect-build.md). Collect often:
   segments still in a pod are lost when the pod goes. To look at
   one namespace, `build -namespace prod`, then `show`, `open` or
   `tui` with `-namespace prod`
   ([kubernetes.md](../kubernetes.md#collect-and-build)).
8. **Clean up** — collect, `iormeshctl rm <ref>`, then restart the
   pod (a debug container cannot be removed) or revert the
   sidecar patch, and finally the prepare patch
   ([kubernetes.md](../kubernetes.md#cleanup)).

> **Human step.** The prepare and sidecar patches change the
> workload and roll it out; the namespace's Pod Security level and
> RBAC are the cluster owner's. Whoever may add an ephemeral
> container in a `privileged` namespace can start a BPF-capable
> container there, and BPF is node-wide: grant it like root on the
> nodes.

**Checkpoint:** `deploy` prints, per pod, `started tracer, project
P, image I; attached, session S` (debug) or `sidecar running the
pinned image; …` (sidecar), and after a collect the store holds
`<project>/pod-<uid>/`.

**If not:** the pod errors are in
[kubernetes.md](../kubernetes.md#troubleshooting), SELinux on el9
nodes in [kubernetes.md](../kubernetes.md#selinux), and the limits
you will hit (a debug tracer dies with its app container; at most
8 tracers per pod) in
[kubernetes.md](../kubernetes.md#limits-you-will-hit).

Reference: [kubernetes.md](../kubernetes.md),
[security.md](../security.md) ("Kubernetes"),
[schema.md](../schema/graph.md#pod-fields-on-events-m7) (the pod fields
on every event).

---

← [10. Grow the mesh](10-expand.md) · [Contents](README.md) · Next: [12. When things go wrong](12-troubleshooting.md) →
