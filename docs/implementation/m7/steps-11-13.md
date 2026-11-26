# M7 steps 11–13: ClusterIP join, route map, routed edges

Part of [M7 — Kubernetes](../m7.md), in [Implementation
guide](../../implementation.md).

11. ClusterIP join: S2's key survives kube-proxy's DNAT (milestones.md
    M7 "Service-NAT correlation", settled by task xb4), so M4's
    `federate.Fuse` joins both ends unchanged and no conntrack is
    read. `internal/routemap` (`join.go`) then names the Service
    the client dialed: `Table.Join` takes one `Flow` per traced
    client end (`viewmodel/routes.go` builds them from
    `servicedb.Observation` and the fused conns' server halves)
    and returns routed edges and flags. A fused flow to a
    ClusterIP is a `fused` edge whose pod is the server end's
    endpoint (by UID from its `pod-<uid>` host id, else by
    address — for an in-pod end only among endpoints without a
    UID, as pod IPs are reused) — or, not listed, the host id plus a
    `stale_service` flag; an unfused one is a `service` edge to
    the workload, with a pod only for a sole endpoint. A `route`
    edge (FQDN → ingress → Service → workload) names its
    Service's sole endpoint too, but only when that pod was traced
    (task ff4): `Join` takes the build's traced host ids
    (`viewmodel.tracedHosts`: the tally's sessions and every
    traced end of an observation) and `addRoute` names the
    endpoint whose `pod-<uid>` is among them — its pod name, else
    the host id. Two endpoints, one without a UID or an untraced
    pod name none. Why a route asks more than a ClusterIP edge
    (milestones.md M7, task 5g4): an unfused ClusterIP flow's own trace
    shows the client dialing the Service's address, which
    kube-proxy forwards only to the Service's endpoints, so a sole
    endpoint is one map lookup from what was observed. A route's
    flow ends at the ingress; the ingress controller picks the
    backend from its own endpoint cache and rules (canary weights,
    a default backend), and the map's FQDN → Service → endpoint is
    two lookups no trace confirms — the pod's own trace in the
    same build is the evidence the endpoint was not stale. A
    traced pod behind a ClusterIP fuses instead, so the two rules
    never apply to the same edge. `pod-` is
    `k8sname.PodHostPrefix`, which `k8s.PodHostID`, `routemap`
    and the registry's `podDir` share. On kind the inventory
    backend under its sidecar is that pod. Tests:
    `internal/routemap/join_test.go` merges two synthetic pods'
    traces with the unchanged merge and checks the joined result
    against `testdata/clusterip.golden`.
12. `internal/routemap` (#15): the static map first —
    `~/.config/iormesh/routes.yaml`, loaded with the registry's
    ownership and mode checks (`federate.ReadPrivate`); built by
    task xb4 (placeholders):

    ```yaml
    routes:
      - fqdn: <fqdn>                    # or "*.<domain>", one label
        ingress: <ingress>              # Traefik in the M0 scenario
        service: <namespace>/<service>
        workload: <namespace>/deployment/<name>  # or statefulset/;
                                                 # optional
        ingress_service: <namespace>/<service>   # optional: the
                                                 # ingress's own
    services:                           # what the ClusterIP join reads
      - service: <namespace>/<service>
        cluster_ips: [<ip>]             # one, or two (dual stack)
        ports: [{port: <port>, target_port: <port>}]
        workload: <namespace>/deployment/<name>
        endpoints:                      # optional; none skips checks
          - {addr: <pod ip>, pod: <namespace>/<pod>, uid: <pod uid>}
    service_cidrs: [<cidr>]             # optional: unknown_service
    path_templates:                     # optional (task xh4): how
      - /shops/{shop}/orders/{order}    # the services view counts
                                        # HTTP paths
    ```

    `path_templates` are the operator's rules for the per-path
    counts (decision #22's templating, `internal/pathtmpl`): a
    `{name}` segment matches any one segment, the first matching
    template names the request, ahead of the built-in folding of
    numeric, UUID and long-hex segments to `{id}`. Only the
    static map's count; discovery writes none. A bad template
    (relative, a query, a half placeholder, a duplicate, past
    256 templates or 512 bytes) refuses the file like any entry.

    Names follow `internal/k8sname`; unknown keys, a second YAML
    document, duplicates (an FQDN, a Service, a ClusterIP and
    port) and the caps (4096 routes and Services, 64 ports and
    1024 endpoints a Service, 4 MiB) refuse the whole file.
    `build -routes <file>` reads another file, which must exist;
    `refresh` takes it too. `ingress_service` names the ingress
    controller's Service: a flow that dialed its ClusterIP, or
    fused to one of its listed pods, while asking for the route's
    FQDN is the route's edge (`Table.Join` tries it first, and the
    HTTP-host check skips it), so an in-cluster client calling
    the FQDN through Traefik is no contradiction. A path-restricted
    rule becomes a whole-host route.

    Discovery (`discover.go`, `discover_services.go`,
    `discover_ingress.go`, `snapshot.go`; built by task xb4):
    `iormeshctl routes discover [-cluster C] [-namespace NS]...`
    resolves a registered cluster (the only one by default) and
    its namespace, runs `k8s.RouteReader.Get` — `kubectl
    --context=… --namespace=… get <resource> --output=json` for
    each of `k8s.RouteResources`, an unserved IngressRoute CRD
    read as none — and `routemap.Discover` turns the lists into a
    `Map` plus flags: Services with a ClusterIP and a TCP port,
    named target ports resolved through the slices' ports,
    serving endpoints by pod UID and name, the workload through
    pod → ReplicaSet → Deployment or pod → StatefulSet; Ingress
    rule hosts and Traefik `Host(…)` matchers (`MatchHosts`, the
    rule text only) to their Services, one Service per host or no
    route, its workload left to the Service and its
    `ingress_service` from the Ingress's status addresses (a
    discovered Service's ClusterIP or load-balancer address), else
    `-ingress-service`. A rule with a negation, a Service over 64
    ports, an endpoint pod of another namespace (its UID dropped
    too) are skipped and flagged, not fatal. Each get has a
    two-minute timeout, and the runner caps kubectl's stdout at
    128 MiB. The snapshot (`routes-discovered.json` beside
    `services.db`, 0600, validated on load like the static map,
    nothing after its JSON value) feeds `routemap.Compile` under
    the static map; the build refuses one whose context no
    registered cluster names.

    Then discovery through the controller's kubectl (Ingress,
    IngressRoute, Service, EndpointSlice, owner references),
    filling only FQDNs the file lacks; a static entry always
    wins. Observed http events never change a route; a route
    they contradict is flagged in the build.
13. Rendering the routed edges (task bf4): `render/routes.go`
    (the band below `graph.svg`'s columns; `render.Names` carries
    the model's routes and flags), `tui/routes.go` (rows after the
    edges, their details) and `site/assets/app.js` ("routed
    edges": the band under the mesh, `#/mesh/route/N`,
    `#/mesh/flag/N`); the shared text — `Route.Hops`,
    `RouteNote`, `FlagText`, `FlagsOf` — is in `viewmodel`.
    Design and tests: [visualization.md](../../visualization.md),
    "Routed edges".

**Done when**: milestones.md's M7 acceptance holds.
