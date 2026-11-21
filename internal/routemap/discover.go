package routemap

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Discovery (decision #15, "k8s API discovery second"): the objects
// `kubectl get -o json` lists — Ingress, Traefik's IngressRoute, Service,
// EndpointSlice, and the pods and ReplicaSets behind the endpoints — are
// turned into a Map like the static file's. The controller runs it with
// the operator's kubectl (internal/k8s RouteReader); no tracer is
// involved and nothing is parsed off the wire.
//
// The rule throughout: one answer or none. A host routed to two
// Services (by path, or by two objects) gets no route; a Service whose
// endpoints belong to two workloads names none; an object whose names
// fail the map's checks is skipped and counted. Each is a Flag, carried
// into the snapshot and printed by the build.

// Objects are the raw `kubectl get -o json` lists, any number per kind
// (one per namespace read).
type Objects struct {
	Ingresses, IngressRoutes, Services, EndpointSlices, Pods, ReplicaSets [][]byte
}

// FlagSkipped: a discovered object whose names or addresses fail the
// map's checks, left out.
const FlagSkipped = "skipped"

// Discover builds the discovered map from objs. The result passed
// Validate; an error means a list that is not kubectl's JSON.
func Discover(objs Objects) (*Map, []Flag, error) {
	d := discovery{owners: map[string]string{}, workloads: map[string]string{}}
	steps := []struct {
		lists [][]byte
		add   func([]byte) error
	}{
		{objs.ReplicaSets, d.addReplicaSets}, {objs.Pods, d.addPods},
		{objs.Services, d.addServices}, {objs.EndpointSlices, d.addSlices},
		{objs.Ingresses, d.addIngresses}, {objs.IngressRoutes, d.addIngressRoutes},
	}
	for _, s := range steps {
		for _, l := range s.lists {
			if err := s.add(l); err != nil {
				return nil, nil, fmt.Errorf("route discovery: %w", err)
			}
		}
	}
	m := d.finish()
	if err := m.Validate(); err != nil {
		return nil, nil, fmt.Errorf("route discovery: %w", err)
	}
	return m, d.sortedFlags(), nil
}

// discovery accumulates the objects.
type discovery struct {
	// owners maps "<ns>/<replicaset>" to its Deployment's name.
	owners map[string]string
	// workloads maps a pod UID to its workload ("" when it has none of
	// the two kinds).
	workloads map[string]string
	pods      map[string]podRef // by UID
	services  map[string]*svcAcc
	hosts     map[string]map[backend]bool // FQDN → the backends routing it
	flags     []Flag
}

// podRef is a pod's name, as an endpoint names it.
type podRef struct{ ns, name string }

// backend is one ingress object's backend for a host.
type backend struct{ ingress, service string }

// meta is the metadata every object carries.
type meta struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	UID             string            `json:"uid"`
	Labels          map[string]string `json:"labels"`
	OwnerReferences []struct {
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Controller bool   `json:"controller"`
	} `json:"ownerReferences"`
}

// controller is the object's controlling owner: kind and name.
func (m meta) controller() (kind, name string) {
	for _, o := range m.OwnerReferences {
		if o.Controller {
			return o.Kind, o.Name
		}
	}
	return "", ""
}

// list decodes a kubectl list's items into T.
func list[T any](data []byte) ([]T, error) {
	var l struct {
		Items []T `json:"items"`
	}
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return l.Items, nil
}

func (d *discovery) addReplicaSets(data []byte) error {
	items, err := list[struct {
		Metadata meta `json:"metadata"`
	}](data)
	for _, rs := range items {
		if kind, name := rs.Metadata.controller(); kind == "Deployment" {
			d.owners[rs.Metadata.Namespace+"/"+rs.Metadata.Name] = name
		}
	}
	return err
}

func (d *discovery) addPods(data []byte) error {
	items, err := list[struct {
		Metadata meta `json:"metadata"`
	}](data)
	if d.pods == nil {
		d.pods = map[string]podRef{}
	}
	for _, p := range items {
		md := p.Metadata
		d.pods[md.UID] = podRef{md.Namespace, md.Name}
		switch kind, name := md.controller(); kind {
		case "ReplicaSet":
			if dep, ok := d.owners[md.Namespace+"/"+name]; ok {
				d.workloads[md.UID] = md.Namespace + "/" + KindDeployment + "/" + dep
			}
		case "StatefulSet":
			d.workloads[md.UID] = md.Namespace + "/" + KindStatefulSet + "/" + name
		}
	}
	return err
}

func (d *discovery) skip(what, why string) {
	d.flags = append(d.flags, Flag{Kind: FlagSkipped, Subject: what, Detail: why})
}

// sortedFlags returns the flags in (Kind, Subject, Detail) order.
func (d *discovery) sortedFlags() []Flag {
	out := slices.Clone(d.flags)
	slices.SortFunc(out, func(a, b Flag) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Detail, b.Detail))
	})
	return slices.Compact(out)
}

// finish turns what was gathered into a map: the Services in name
// order, then one route per host with exactly one backend.
func (d *discovery) finish() *Map {
	m := &Map{Routes: []Route{}, Services: []Service{}}
	for _, ref := range slices.Sorted(maps.Keys(d.services)) {
		m.Services = append(m.Services, d.services[ref].service(d))
	}
	for _, host := range slices.Sorted(maps.Keys(d.hosts)) {
		r, ok := d.route(host, m)
		if ok {
			m.Routes = append(m.Routes, r)
		}
	}
	return m
}

// route is host's route, when every object routing it names one Service.
func (d *discovery) route(host string, m *Map) (Route, bool) {
	bs := slices.SortedFunc(maps.Keys(d.hosts[host]), func(a, b backend) int {
		return cmp.Or(cmp.Compare(a.service, b.service), cmp.Compare(a.ingress, b.ingress))
	})
	if bs[0].service != bs[len(bs)-1].service {
		var names []string
		for _, b := range bs {
			names = append(names, b.ingress+" -> "+b.service)
		}
		d.flags = append(d.flags, Flag{Kind: FlagAmbiguousRoute, Subject: host,
			Detail: "routed to several services: " + strings.Join(slices.Compact(names), ", ")})
		return Route{}, false
	}
	r := Route{FQDN: host, Ingress: bs[0].ingress, Service: bs[0].service}
	for _, s := range m.Services {
		if s.Service == r.Service {
			r.Workload = s.Workload
		}
	}
	return r, true
}
