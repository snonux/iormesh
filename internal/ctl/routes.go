package ctl

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"iormesh/internal/federate"
	"iormesh/internal/k8s"
	"iormesh/internal/k8sname"
	"iormesh/internal/routemap"
	"iormesh/internal/termsafe"
)

// The route map (PLAN.md M7, decision #15). Two sources, compiled into
// the table the view model's routed edges are joined from:
//
//   - the static map, ~/.config/iormesh/routes.yaml beside the registry
//     and under its ownership and mode rules (-routes on build and
//     refresh reads another file);
//   - the discovery snapshot `iormeshctl routes discover` writes beside
//     the service database from the operator's kubectl, read-only gets
//     in a registered cluster's context. build reads the snapshot and
//     never runs kubectl itself, so it stays offline and deterministic.
//
// A static entry wins over a discovered one (routemap.Compile). No map
// and no snapshot is no routed edges, never an error; a file that fails
// its checks fails the build.

// addRoutesFlag registers -routes and -registry on build's and refresh's
// flag sets. The registry decides where routes.yaml is by default and
// which contexts a discovery snapshot may name, so a build reads the
// registry that discover and collect were given (task yb4: on kind, a
// build after `routes discover -registry FILE` refused the snapshot,
// having checked it against the default registry).
func addRoutesFlag(fs *flag.FlagSet, f *buildFlags) {
	fs.StringVar(&f.routes, "routes", "", "static route map (default routes.yaml beside the registry)")
	fs.StringVar(&f.registry, "registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
}

// loadRoutes reads the static route map and the discovery snapshot
// beside db and compiles them; nil when there is neither.
func loadRoutes(e env, f buildFlags, db string) (*routemap.Table, error) {
	path := f.routes
	if path == "" && f.registry != "" {
		path = routemap.DefaultPath(f.registry)
	} else if path == "" && e.registry != nil {
		reg, err := e.registry()
		if err != nil {
			return nil, err
		}
		path = routemap.DefaultPath(reg)
	}
	if f.routes != "" { // named on the command line: it must be there
		if _, err := os.Stat(f.routes); err != nil {
			return nil, fmt.Errorf("route map: %w", err)
		}
	}
	var static *routemap.Map
	if path != "" {
		var err error
		if static, err = routemap.Load(path); err != nil {
			return nil, err
		}
	}
	snap, err := routemap.LoadSnapshot(routemap.SnapshotPath(db))
	if err != nil {
		return nil, err
	}
	if err := checkSnapshotContext(e, f.registry, snap); err != nil {
		return nil, err
	}
	if static == nil && snap == nil {
		return nil, nil
	}
	if snap == nil {
		return routemap.Compile(static, nil, nil), nil
	}
	return routemap.Compile(static, &snap.Map, snap.Flags), nil
}

// checkSnapshotContext requires the snapshot's context to be one of the
// registry's clusters: a snapshot of a cluster the operator no longer
// traces would name its ClusterIPs on another cluster's connections. A
// registry that lists several clusters still shares one snapshot (the
// last discovered); the map applies to every flow of the build (PLAN.md
// #15). registry is the build's -registry, "" for the default.
func checkSnapshotContext(e env, registry string, snap *routemap.Snapshot) error {
	if snap == nil {
		return nil
	}
	r, err := loadRegistry(registry, e)
	if err != nil {
		return err
	}
	if !slices.Contains(r.Contexts(), snap.Context) {
		return fmt.Errorf("route snapshot: context %s is no registered cluster's; rediscover or remove %s",
			termsafe.String(snap.Context), routemap.SnapshotName)
	}
	return nil
}

// routesFlags are `routes discover`'s flags.
type routesFlags struct {
	registry, cluster, db string
	namespaces            projectList // repeatable, like -project
	ingressService        string      // the ingress controller's Service
}

// routesCommand runs `iormeshctl routes discover`.
func routesCommand(args []string, e env) error {
	if len(args) == 0 || args[0] != "discover" {
		return fmt.Errorf("%w: routes takes the subcommand discover", errUsage)
	}
	fs := newFlagSet("routes discover", e.stderr)
	var f routesFlags
	fs.StringVar(&f.registry, "registry", "", "registry file (default ~/.config/iormesh/servers.yaml)")
	fs.StringVar(&f.cluster, "cluster", "", "registered cluster to read (default: the only one)")
	fs.Var(&f.namespaces, "namespace", "namespace to read (repeatable; default: the cluster's)")
	fs.StringVar(&f.db, "db", "", "service database the snapshot sits beside (default ~/.local/share/iormesh/services.db)")
	fs.StringVar(&f.ingressService, "ingress-service", "",
		"the ingress controller's own Service, NAMESPACE/NAME, put on discovered routes")
	if err := fs.Parse(args[1:]); err != nil {
		return fmt.Errorf("%w: %v", errUsage, err)
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("%w: routes discover takes no arguments", errUsage)
	}
	if ns, name, ok := strings.Cut(f.ingressService, "/"); f.ingressService != "" &&
		(!ok || !k8sname.Label(ns) || !k8sname.Label(name)) {
		return fmt.Errorf("%w: -ingress-service %q is not NAMESPACE/NAME", errUsage, f.ingressService)
	}
	return discoverRoutes(context.Background(), e, f)
}

// discoverRoutes reads the cluster, builds the discovered map and
// writes the snapshot.
func discoverRoutes(ctx context.Context, e env, f routesFlags) error {
	r, err := loadRegistry(f.registry, e)
	if err != nil {
		return err
	}
	c, namespaces, err := discoveryScope(&r, f)
	if err != nil {
		return err
	}
	objs, err := readRouteObjects(ctx, c.Context, namespaces)
	if err != nil {
		return err
	}
	objs.IngressService = f.ingressService
	m, flags, err := routemap.Discover(objs)
	if err != nil {
		return err
	}
	if f.db == "" {
		if f.db, err = e.servicedb(); err != nil {
			return err
		}
	}
	path := routemap.SnapshotPath(f.db)
	snap := routemap.Snapshot{Taken: time.Now().UTC(), Context: c.Context, Namespaces: namespaces,
		Map: *m, Flags: flags}
	if err := routemap.SaveSnapshot(path, snap); err != nil {
		return err
	}
	return printDiscovery(e, snap, path)
}

// discoveryScope is the registered cluster to read and its namespaces:
// -cluster, or the registry's only cluster; -namespace, or the
// cluster's default. Only a registered cluster's context ever reaches
// kubectl (principle #3).
func discoveryScope(r *federate.Registry, f routesFlags) (federate.Cluster, []string, error) {
	name := f.cluster
	if name == "" {
		if r.K8s == nil || len(r.K8s.Clusters) != 1 {
			return federate.Cluster{}, nil, fmt.Errorf("%w: -cluster names one of the registry's k8s clusters", errUsage)
		}
		name = r.K8s.Clusters[0].Name
	}
	c, ok := r.Cluster(name)
	if !ok {
		return federate.Cluster{}, nil, fmt.Errorf("%w: cluster %q is not registered", errUsage, name)
	}
	namespaces := slices.Compact(slices.Sorted(slices.Values(f.namespaces)))
	if len(namespaces) == 0 && c.Namespace != "" {
		namespaces = []string{c.Namespace}
	}
	if len(namespaces) == 0 {
		return federate.Cluster{}, nil, fmt.Errorf("%w: cluster %s has no namespace; pass -namespace", errUsage, name)
	}
	return c, namespaces, nil
}

// readRouteObjects gets every route resource in every namespace.
// Traefik's IngressRoute not being served (no Traefik, or only one of
// its two API groups) is no objects, not a failure.
func readRouteObjects(ctx context.Context, kubeContext string, namespaces []string) (routemap.Objects, error) {
	var o routemap.Objects
	into := map[string]*[][]byte{
		"ingresses.networking.k8s.io": &o.Ingresses, "ingressroutes.traefik.io": &o.IngressRoutes,
		"ingressroutes.traefik.containo.us": &o.IngressRoutes, "services": &o.Services,
		"endpointslices.discovery.k8s.io": &o.EndpointSlices, "pods": &o.Pods, "replicasets.apps": &o.ReplicaSets,
	}
	for _, ns := range namespaces {
		rr, err := k8s.NewRouteReader(kubeContext, ns)
		if err != nil {
			return o, err
		}
		for _, res := range k8s.RouteResources {
			data, err := rr.Get(ctx, res)
			if errors.Is(err, k8s.ErrNoResource) && strings.HasPrefix(res, "ingressroutes.") {
				continue
			}
			if err != nil {
				return o, err
			}
			*into[res] = append(*into[res], data)
		}
	}
	return o, nil
}

// printDiscovery says what was found and where it went, and lists the
// flags: what discovery left out and why.
func printDiscovery(e env, s routemap.Snapshot, path string) error {
	q := termsafe.String // the flags quote names the cluster chose
	var b strings.Builder
	fmt.Fprintf(&b, "discovered %d routes, %d services in %s (%s)\nsnapshot   %s\n", len(s.Map.Routes),
		len(s.Map.Services), s.Context, strings.Join(s.Namespaces, ", "), q(path))
	for _, f := range s.Flags {
		fmt.Fprintf(&b, "route flag %s %s: %s\n", f.Kind, q(f.Subject), q(f.Detail))
	}
	_, err := fmt.Fprint(e.stdout, b.String())
	return err
}
