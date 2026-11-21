package routemap

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"iormesh/internal/k8sname"
)

// ErrInvalid marks a map entry that fails validation, for errors.Is.
var ErrInvalid = errors.New("routemap: invalid entry")

// Bounds on one map, so a hand-edited or forged file cannot make every
// build pay for millions of entries. A cluster with more Services than
// this is beyond what one build's map is meant to name.
const (
	maxRoutes    = 4096
	maxServices  = 4096
	maxPorts     = 64   // per Service
	maxEndpoints = 1024 // per Service
	maxCIDRs     = 16
	maxIngress   = 253
)

// Workload kinds a route or Service may name (decision #15: "deployment
// or statefulset"). A DaemonSet or a bare pod behind a Service is left
// unnamed rather than mislabelled.
const (
	KindDeployment  = "deployment"
	KindStatefulSet = "statefulset"
)

// Map is one route map: the static file's content, or one discovery
// snapshot's.
type Map struct {
	Routes   []Route   `yaml:"routes,omitempty" json:"routes"`
	Services []Service `yaml:"services,omitempty" json:"services"`
	// ServiceCIDRs are the clusters' Service ranges: a connection to an
	// address inside one that no Service of the map holds is flagged
	// (unknown_service) rather than left a bare address.
	ServiceCIDRs []string `yaml:"service_cidrs,omitempty" json:"service_cidrs,omitempty"`
}

// Route maps one FQDN to the ingress it terminates on and the Service and
// workload behind it.
type Route struct {
	// FQDN is the name clients ask for; "*.example.org" matches one
	// label, as an Ingress host wildcard does.
	FQDN string `yaml:"fqdn" json:"fqdn"`
	// Ingress names the ingress: a label ("traefik"), or the discovered
	// object ("ingress/<ns>/<name>", "ingressroute/<ns>/<name>").
	Ingress string `yaml:"ingress" json:"ingress"`
	// Service is "<namespace>/<service>".
	Service string `yaml:"service" json:"service"`
	// Workload is "<namespace>/deployment/<name>" or
	// "<namespace>/statefulset/<name>"; empty takes the Service's.
	Workload string `yaml:"workload,omitempty" json:"workload,omitempty"`
	// IngressService is the ingress controller's own Service
	// ("kube-system/traefik"), optional: an in-cluster client reaching
	// it — by ClusterIP, or fused to one of its pods — while asking for
	// FQDN is this route's ingress hop, not a contradiction.
	IngressService string `yaml:"ingress_service,omitempty" json:"ingress_service,omitempty"`
}

// Service is one Kubernetes Service: what its ClusterIP is and what is
// behind it.
type Service struct {
	Service    string   `yaml:"service" json:"service"` // "<namespace>/<service>"
	ClusterIPs []string `yaml:"cluster_ips" json:"cluster_ips"`
	Ports      []Port   `yaml:"ports" json:"ports"`
	// Endpoints are the pods behind it, as the EndpointSlices listed
	// them when the map was written; empty means unknown, which skips
	// the endpoint checks rather than failing them.
	Endpoints []Endpoint `yaml:"endpoints,omitempty" json:"endpoints,omitempty"`
	Workload  string     `yaml:"workload,omitempty" json:"workload,omitempty"`
}

// Port is one TCP port of a Service: the port clients dial on the
// ClusterIP, and the pods' port behind it (zero means the same). A named
// targetPort is resolved per endpoint by discovery, not here.
type Port struct {
	Port       uint16 `yaml:"port" json:"port"`
	TargetPort uint16 `yaml:"target_port,omitempty" json:"target_port,omitempty"`
}

// Endpoint is one pod behind a Service.
type Endpoint struct {
	Addr string `yaml:"addr" json:"addr"`
	Pod  string `yaml:"pod,omitempty" json:"pod,omitempty"` // "<namespace>/<pod>"
	// UID is the pod's UID: an in-pod tracer's host id is "pod-<uid>"
	// (PLAN.md M7 "The pod is the host"), so a fused server end is
	// matched to its endpoint by it before any address.
	UID string `yaml:"uid,omitempty" json:"uid,omitempty"`
}

var (
	// ingressLabel is what an ingress name may be: no space, quote or
	// control character, nothing a terminal or a shell would read.
	ingressLabel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
	// podUID is a Kubernetes UID: a lowercase RFC 4122 string.
	podUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// Validate checks the whole map; the first problem is the error. Names
// are held to Kubernetes' own rules (internal/k8sname), so nothing a
// renderer prints from the map can carry an escape sequence, and the
// duplicates a lookup would have to choose between are refused.
func (m *Map) Validate() error {
	if len(m.Routes) > maxRoutes || len(m.Services) > maxServices || len(m.ServiceCIDRs) > maxCIDRs {
		return fmt.Errorf("%w: at most %d routes, %d services and %d service_cidrs",
			ErrInvalid, maxRoutes, maxServices, maxCIDRs)
	}
	fqdns := map[string]bool{}
	for i := range m.Routes {
		r := &m.Routes[i]
		r.FQDN = NormalizeFQDN(r.FQDN)
		if err := r.validate(); err != nil {
			return err
		}
		if fqdns[r.FQDN] {
			return fmt.Errorf("%w: route %s listed twice", ErrInvalid, r.FQDN)
		}
		fqdns[r.FQDN] = true
	}
	if err := m.validateServices(); err != nil {
		return err
	}
	for _, c := range m.ServiceCIDRs {
		if p, err := netip.ParsePrefix(c); err != nil || p != p.Masked() {
			return fmt.Errorf("%w: service_cidrs: %q is not a canonical prefix", ErrInvalid, c)
		}
	}
	return nil
}

// validateServices checks each Service and refuses two of one name, or
// two claiming one ClusterIP and port: a lookup by address must have one
// answer.
func (m *Map) validateServices() error {
	names := map[string]bool{}
	addrs := map[netip.AddrPort]string{}
	for i := range m.Services {
		s := &m.Services[i]
		if err := s.validate(); err != nil {
			return err
		}
		if names[s.Service] {
			return fmt.Errorf("%w: service %s listed twice", ErrInvalid, s.Service)
		}
		names[s.Service] = true
		for _, a := range s.addrs() {
			if other, ok := addrs[a]; ok {
				return fmt.Errorf("%w: services %s and %s both claim %s", ErrInvalid, other, s.Service, a)
			}
			addrs[a] = s.Service
		}
	}
	return nil
}

func (r *Route) validate() error {
	switch {
	case !validFQDN(r.FQDN):
		return fmt.Errorf("%w: route fqdn %q", ErrInvalid, r.FQDN)
	case len(r.Ingress) > maxIngress || !ingressLabel.MatchString(r.Ingress):
		return fmt.Errorf("%w: route %s: ingress %q", ErrInvalid, r.FQDN, r.Ingress)
	case !validRef(r.Service):
		return fmt.Errorf("%w: route %s: service %q is not <namespace>/<service>", ErrInvalid, r.FQDN, r.Service)
	case r.IngressService != "" && !validRef(r.IngressService):
		return fmt.Errorf("%w: route %s: ingress_service %q is not <namespace>/<service>", ErrInvalid,
			r.FQDN, r.IngressService)
	}
	if r.Workload != "" {
		if err := validWorkload(r.Workload, r.Service); err != nil {
			return fmt.Errorf("route %s: %w", r.FQDN, err)
		}
	}
	return nil
}

func (s *Service) validate() error {
	if !validRef(s.Service) {
		return fmt.Errorf("%w: service %q is not <namespace>/<service>", ErrInvalid, s.Service)
	}
	if len(s.ClusterIPs) == 0 || len(s.ClusterIPs) > 2 || len(s.Ports) == 0 || len(s.Ports) > maxPorts {
		return fmt.Errorf("%w: service %s: 1 or 2 cluster_ips and 1 to %d ports", ErrInvalid, s.Service, maxPorts)
	}
	for _, ip := range s.ClusterIPs {
		if a, err := netip.ParseAddr(ip); err != nil || a.IsUnspecified() || a.Zone() != "" {
			return fmt.Errorf("%w: service %s: cluster ip %q", ErrInvalid, s.Service, ip)
		}
	}
	for _, p := range s.Ports {
		if p.Port == 0 {
			return fmt.Errorf("%w: service %s: port 0", ErrInvalid, s.Service)
		}
	}
	if s.Workload != "" {
		if err := validWorkload(s.Workload, s.Service); err != nil {
			return fmt.Errorf("service %s: %w", s.Service, err)
		}
	}
	return s.validateEndpoints()
}

func (s *Service) validateEndpoints() error {
	if len(s.Endpoints) > maxEndpoints {
		return fmt.Errorf("%w: service %s: over %d endpoints", ErrInvalid, s.Service, maxEndpoints)
	}
	ns, _, _ := strings.Cut(s.Service, "/")
	for _, e := range s.Endpoints {
		if a, err := netip.ParseAddr(e.Addr); err != nil || a.IsUnspecified() || a.Zone() != "" {
			return fmt.Errorf("%w: service %s: endpoint %q", ErrInvalid, s.Service, e.Addr)
		}
		if e.Pod != "" && (!validPodRef(e.Pod) || !strings.HasPrefix(e.Pod, ns+"/")) {
			return fmt.Errorf("%w: service %s: endpoint pod %q is not <%s>/<pod>", ErrInvalid, s.Service, e.Pod, ns)
		}
		if e.UID != "" && !podUID.MatchString(e.UID) {
			return fmt.Errorf("%w: service %s: endpoint uid %q", ErrInvalid, s.Service, e.UID)
		}
	}
	return nil
}

// addrs are the ClusterIP:port pairs clients dial to reach s.
func (s *Service) addrs() []netip.AddrPort {
	var out []netip.AddrPort
	for _, ip := range s.ClusterIPs {
		a, err := netip.ParseAddr(ip)
		if err != nil {
			continue // Validate refused it already
		}
		for _, p := range s.Ports {
			out = append(out, netip.AddrPortFrom(a.Unmap(), p.Port))
		}
	}
	return out
}

// NormalizeFQDN lowercases a name and drops one trailing dot: the form
// routes are keyed by and hostnames are looked up in.
func NormalizeFQDN(s string) string {
	return strings.TrimSuffix(strings.ToLower(s), ".")
}

// validFQDN accepts a DNS name of LDH labels, optionally led by one "*"
// label, at least two labels long: a single label is a cluster-local
// short name, which names a different Service in every namespace.
func validFQDN(s string) bool {
	rest := strings.TrimPrefix(s, "*.")
	if len(s) > 253 || !strings.Contains(rest, ".") {
		return false
	}
	for l := range strings.SplitSeq(rest, ".") {
		if !ldhLabel(l) {
			return false
		}
	}
	return true
}

// ldhLabel is one lowercase DNS label: letters, digits and inner hyphens.
func ldhLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range []byte(l) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// validRef is "<namespace>/<service>": two DNS-1123 labels.
func validRef(s string) bool {
	ns, name, ok := strings.Cut(s, "/")
	return ok && k8sname.Label(ns) && k8sname.Label(name)
}

// validPodRef is "<namespace>/<pod>".
func validPodRef(s string) bool {
	ns, name, ok := strings.Cut(s, "/")
	return ok && k8sname.Label(ns) && k8sname.Pod(name)
}

// validWorkload checks "<namespace>/<kind>/<name>" and that it lives in
// the Service's namespace: a Service selects only pods of its own.
func validWorkload(w, service string) error {
	parts := strings.Split(w, "/")
	if len(parts) != 3 || !k8sname.Label(parts[0]) || !k8sname.Pod(parts[2]) ||
		(parts[1] != KindDeployment && parts[1] != KindStatefulSet) {
		return fmt.Errorf("%w: workload %q is not <namespace>/deployment|statefulset/<name>", ErrInvalid, w)
	}
	if ns, _, _ := strings.Cut(service, "/"); parts[0] != ns {
		return fmt.Errorf("%w: workload %s is outside service %s's namespace", ErrInvalid, w, service)
	}
	return nil
}
