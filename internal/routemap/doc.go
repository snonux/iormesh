// Package routemap is the controller's routing metadata (PLAN.md M7
// "Service-NAT correlation", "Ingress route mapping", decision #15): which
// FQDN terminates on which ingress, and which Kubernetes Service — by its
// ClusterIP — fronts which workload and pods. The build uses it to name
// what the traces alone cannot:
//
//   - A ClusterIP connection whose two ends were both traced is fused by
//     the merge already (decision #26: kube-proxy's DNAT rewrites
//     addresses and ports, never sequence numbers, so the ISN pair
//     survives it). The map only names the Service between them and checks
//     that the pod the traffic reached is one of the Service's endpoints;
//     a pod that is not flags the map as stale, and the fused fact stands.
//   - A connection to a ClusterIP whose far end is untraced is completed to
//     the Service and its workload — to one pod only when the Service has
//     exactly one endpoint, never by picking among several.
//   - A connection to an FQDN that terminates on an ingress (an L7 hop no
//     tracer sits on, which ends the TCP connection and so the ISN join) is
//     completed FQDN → ingress → Service → workload from the route.
//
// Sources, in precedence order: the static map
// (~/.config/iormesh/routes.yaml, read under the registry's ownership and
// mode rules: load.go), then the discovery snapshot `iormeshctl routes
// discover` writes from the operator's kubectl (discover.go,
// snapshot.go). A static entry wins over a discovered one for the same
// FQDN or Service. Observed HTTP hosts never change a route; a route they
// contradict is flagged (join.go). Nothing here parses payload: routing
// configuration is the source.
//
// The package is cgo-free: iormeshctl runs on a laptop.
package routemap
