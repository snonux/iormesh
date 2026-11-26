# Microservices as pods (task uh4)

Part of [M7 acceptance on kind (task yb4)](../m7-acceptance.md), in
[Testing strategy](../../testing.md).

milestones.md M0 runs the microservices scenario "on plain hosts
(M4/M6) and as pods (M7)"; `TestKindMicroservices` is the pod
run (milestones.md M7 "Microservices as pods"). `mage
k8sMicroservices` runs it alone, with the same setup, disk
guard, judging and scoped teardown as `mage k8sAcceptance`,
which runs it too, after `TestKindAcceptance`; a failure there
does not skip `TestKindClusterIP`.

In namespace `iormesh-m7-micro` it applies
`testdata/microservices.yaml`: one Deployment per service
(billing, inventory, orders, gateway), each app container
sleeping, the prepare patch on each. The registry gives every
pod a debug entry with the agent flags, all in one project.
Then, leaves first, for each service it:

1. ships in the certificate the service pins (billing's into
   orders' `/tmp/w`, orders' into the gateway's), `cat` out of
   one pod and into the other through `kubectl exec`, so the
   copy matches no tracer's pattern;
2. starts the JVM behind the startup gate, deploys the pod's
   debug tracer, waits for the agent's `joined` line, and opens
   the gate;
3. waits for a server's `listening` line, which comes after its
   certificate is published.

The servers bind their pod addresses at the rig spread's ports
(billing and orders 443 with TLS, inventory 80), and the callers
dial those addresses: a TLS certificate names the one address
its server binds, so a ClusterIP would fail the pinning. Then
it collects and builds.

| Check | What it asserts |
|---|---|
| summary | the build saw 4 hosts and 4 sessions, `tcp 4 fused, 0 unresolved` |
| `mesh` | `internal/meshcheck.Check`, the function `TestMicroservicesAcrossHosts` calls too: the four pods' traces, merged with their method tables, fuse the four hops at their server pods' addresses, inventory's end twice (orders and the gateway each call it from their own pod), every 4-tuple mirrored, all in the test's project, nothing unresolved; the merged trace holds `expect-pods.json`, the spread golden with the gateway on a host of its own, at the pods' addresses; every request's two http events agree and are timed; a wrong golden (orders on another host, orders dialing inventory on loopback, the TLS hop to orders declared plain) fails |
| `pod fields` | every event names its pod's namespace and name (task sh4); a process's events also carry the app container's id from the pod status, host-level events none |
| `identity` | each pod is its own `pod-<uid>` host with its FQDN, all on one `boot_id` |

`internal/meshcheck`'s unit tests pin `expect-pods.json` to
`expect-spread.json` (only the gateway's four edges move to
`$h4`). They also check that two hops may share a server end,
which the rig's spread never needed, that each mutation changes
both goldens, and the request pairing. `TestMicroTemplate` and
`TestMicroAddresses` check the template, each service's options
and the address map.

Status: green on kind, 2026-11-22. The first `mage
k8sMicroservices` run stopped at the disk guard (`/var/lib/docker
has 16 GiB free, need 20`) before it created anything; after
space was freed, the rerun passed: `TestKindMicroservices` with
its `mesh`, `pod_fields` and `identity` subtests in 152 s, and
the teardown removed exactly the cluster, the registry, its
volume and the images it had made. Green on el9 k3s under both
kube-proxy modes, 2026-11-26 ("Rerun with pod fields (task
6k4)" below).
