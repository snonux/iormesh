package k8s

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"iormesh/internal/k8sname"
)

// Route discovery's reads (PLAN.md M7 "Ingress route mapping", decision
// #15): read-only `kubectl get` of the objects internal/routemap turns
// into a route map — Ingress, Traefik's IngressRoute, Service,
// EndpointSlice and the pods and ReplicaSets that own the endpoints —
// through the operator's kubectl and a registered context, never from a
// tracer. Nothing here writes to the cluster.

// ErrNoResource: the API server does not serve the resource — Traefik's
// CRD absent, the usual case for a cluster without Traefik. Discovery
// reads it as no objects, not as a failure.
var ErrNoResource = errors.New("k8s: resource type not served")

// ErrAPI: a route discovery get failed in kubectl or the API server
// (forbidden, unknown context, server unreachable). It replaces the
// runner's ErrUnreachable, whose "pod not reached" misled for a list of
// ingresses no pod is part of (task ef4).
var ErrAPI = errors.New("k8s: API request failed")

// RouteResources are the resources route discovery reads, by the fully
// qualified names kubectl takes: no short name another CRD could shadow.
// Traefik serves IngressRoute under traefik.io since v3 and under
// traefik.containo.us before; both are read.
var RouteResources = []string{
	"ingresses.networking.k8s.io",
	"ingressroutes.traefik.io",
	"ingressroutes.traefik.containo.us",
	"services",
	"endpointslices.discovery.k8s.io",
	"pods",
	"replicasets.apps",
}

// routeTimeout bounds one get: a list that takes longer is an API server
// or credential plugin hanging, and discovery would otherwise wait
// forever. The output itself is bounded by the runner (outputLimit).
const routeTimeout = 2 * time.Minute

// RouteReader reads route discovery's objects in one context and
// namespace.
type RouteReader struct {
	context, namespace string
	run                runner
}

// NewRouteReader returns the reader for a context (the registry must
// list it: the caller checks, as for every kubectl call) and namespace.
func NewRouteReader(kubeContext, namespace string) (*RouteReader, error) {
	return newRouteReader(kubeContext, namespace, execRunner{bin: "kubectl"})
}

func newRouteReader(kubeContext, namespace string, r runner) (*RouteReader, error) {
	if !k8sname.Context(kubeContext) {
		return nil, fmt.Errorf("%w: %q", ErrContext, kubeContext)
	}
	if !k8sname.Label(namespace) {
		return nil, fmt.Errorf("%w: namespace %q", ErrName, namespace)
	}
	return &RouteReader{context: kubeContext, namespace: namespace, run: r}, nil
}

// Get runs `kubectl get <resource> --output=json` in the reader's
// context and namespace and returns the list. Only RouteResources are
// read. A resource the server does not serve is ErrNoResource, any
// other failure of kubectl's ErrAPI.
func (r *RouteReader) Get(ctx context.Context, resource string) ([]byte, error) {
	if !slices.Contains(RouteResources, resource) {
		return nil, fmt.Errorf("%w: resource %q is not one route discovery reads", ErrName, resource)
	}
	argv := []string{"--context=" + r.context, "--namespace=" + r.namespace, "get", resource, "--output=json"}
	ctx, cancel := context.WithTimeout(ctx, routeTimeout)
	defer cancel()
	out, err := r.run.output(ctx, argv)
	if err != nil {
		if strings.Contains(err.Error(), "the server doesn't have a resource type") {
			return nil, fmt.Errorf("%w: %s", ErrNoResource, resource)
		}
		if errors.Is(err, ErrUnreachable) {
			msg := strings.TrimPrefix(err.Error(), ErrUnreachable.Error()+": ")
			return nil, fmt.Errorf("%w: kubectl get %s in %s: %s", ErrAPI, resource, r.namespace, msg)
		}
		return nil, fmt.Errorf("kubectl get %s in %s: %w", resource, r.namespace, err)
	}
	return out, nil
}
