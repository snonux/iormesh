package routemap

import (
	"encoding/json"
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// The Services and their EndpointSlices: what the ClusterIP join reads.

// svcAcc accumulates one Service and its slices.
type svcAcc struct {
	ref        string
	clusterIPs []string
	ports      []svcPort
	endpoints  map[string]Endpoint // by address
	slicePorts map[string]uint16   // the slices' named ports
}

// svcPort is one TCP port of a Service; a named targetPort is resolved
// through the slices' ports.
type svcPort struct {
	port       uint16
	target     uint16
	targetName string
}

type serviceObj struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Type       string   `json:"type"`
		ClusterIP  string   `json:"clusterIP"`
		ClusterIPs []string `json:"clusterIPs"`
		Ports      []struct {
			Protocol   string          `json:"protocol"`
			Port       int             `json:"port"`
			TargetPort json.RawMessage `json:"targetPort"`
		} `json:"ports"`
	} `json:"spec"`
	Status struct {
		LoadBalancer struct {
			Ingress []struct {
				IP string `json:"ip"`
			} `json:"ingress"`
		} `json:"loadBalancer"`
	} `json:"status"`
}

// addServices keeps every Service with a ClusterIP: headless ones
// ("None") and ExternalName ones have none, so no connection can dial
// them by address.
func (d *discovery) addServices(data []byte) error {
	items, err := list[serviceObj](data)
	if d.services == nil {
		d.services = map[string]*svcAcc{}
	}
	for _, s := range items {
		ref := s.Metadata.Namespace + "/" + s.Metadata.Name
		ips := s.Spec.ClusterIPs
		if len(ips) == 0 && s.Spec.ClusterIP != "" {
			ips = []string{s.Spec.ClusterIP}
		}
		if s.Spec.Type == "ExternalName" || len(ips) == 0 || ips[0] == "None" {
			continue
		}
		if !validRef(ref) || !validIPs(ips) {
			d.skip("service "+strconv.Quote(ref), "name or cluster ip fails the map's checks")
			continue
		}
		acc := &svcAcc{ref: ref, clusterIPs: ips, endpoints: map[string]Endpoint{}, slicePorts: map[string]uint16{}}
		for _, p := range s.Spec.Ports {
			if (p.Protocol == "" || p.Protocol == "TCP") && p.Port > 0 && p.Port <= 65535 {
				acc.ports = append(acc.ports, newSvcPort(p.Port, p.TargetPort))
			}
		}
		switch {
		case len(acc.ports) > maxPorts: // one odd Service, not a failed discovery
			d.skip("service "+ref, "over "+strconv.Itoa(maxPorts)+" ports")
		case len(acc.ports) > 0: // a UDP-only Service carries no TCP connection
			d.services[ref] = acc
			d.addressed(ref, ips, s)
		}
	}
	return err
}

// addressed records the addresses that name a kept Service: its
// ClusterIPs and its load balancer's, which an Ingress's status may
// list when the Service is the ingress controller's.
func (d *discovery) addressed(ref string, ips []string, s serviceObj) {
	for _, ip := range ips {
		d.svcIPs[ip] = ref
	}
	for _, lb := range s.Status.LoadBalancer.Ingress {
		if lb.IP != "" {
			d.svcIPs[lb.IP] = ref
		}
	}
}

// newSvcPort reads a port and its targetPort, a number or a port name.
func newSvcPort(port int, target json.RawMessage) svcPort {
	p := svcPort{port: uint16(port)}
	var n int
	if json.Unmarshal(target, &n) == nil && n > 0 && n <= 65535 {
		p.target = uint16(n)
	} else {
		_ = json.Unmarshal(target, &p.targetName) // absent: the same port
	}
	return p
}

func validIPs(ips []string) bool {
	if len(ips) > 2 {
		return false
	}
	for _, ip := range ips {
		if a, err := netip.ParseAddr(ip); err != nil || a.IsUnspecified() || a.Zone() != "" {
			return false
		}
	}
	return true
}

type sliceObj struct {
	Metadata    meta   `json:"metadata"`
	AddressType string `json:"addressType"`
	Ports       []struct {
		Name string `json:"name"`
		Port int    `json:"port"`
	} `json:"ports"`
	Endpoints []struct {
		Addresses  []string `json:"addresses"`
		Conditions struct {
			Ready   *bool `json:"ready"`
			Serving *bool `json:"serving"`
		} `json:"conditions"`
		TargetRef *struct {
			Kind string `json:"kind"`
			UID  string `json:"uid"`
		} `json:"targetRef"`
	} `json:"endpoints"`
}

// addSlices adds each slice's serving endpoints to its Service: a
// terminating pod that still serves can answer a connection, one that
// neither is ready nor serves cannot.
func (d *discovery) addSlices(data []byte) error {
	items, err := list[sliceObj](data)
	for _, sl := range items {
		acc := d.services[sl.Metadata.Namespace+"/"+sl.Metadata.Labels["kubernetes.io/service-name"]]
		if acc == nil || (sl.AddressType != "IPv4" && sl.AddressType != "IPv6") {
			continue
		}
		for _, p := range sl.Ports {
			if p.Name != "" && p.Port > 0 && p.Port <= 65535 {
				acc.slicePorts[p.Name] = uint16(p.Port)
			}
		}
		for _, e := range sl.Endpoints {
			c := e.Conditions
			if (c.Ready != nil && !*c.Ready) && (c.Serving == nil || !*c.Serving) {
				continue
			}
			for _, a := range e.Addresses {
				if ip, err := netip.ParseAddr(a); err != nil || ip.IsUnspecified() || ip.Zone() != "" {
					d.skip("endpoint "+strconv.Quote(a), "not an address")
					continue
				}
				acc.endpoints[a] = d.endpoint(a, e.TargetRef)
			}
		}
	}
	return err
}

// endpoint names one address by its pod, when the slice says which.
func (d *discovery) endpoint(addr string, ref *struct {
	Kind string `json:"kind"`
	UID  string `json:"uid"`
}) Endpoint {
	e := Endpoint{Addr: addr}
	if ref == nil || ref.Kind != "Pod" || !podUID.MatchString(ref.UID) {
		return e
	}
	e.UID = ref.UID
	if p, ok := d.pods[ref.UID]; ok {
		e.Pod = p.ns + "/" + p.name
	}
	return e
}

// service finishes one Service: ports with their targets resolved,
// endpoints in address order, and the workload when all endpoints share
// one.
func (a *svcAcc) service(d *discovery) Service {
	s := Service{Service: a.ref, ClusterIPs: a.clusterIPs}
	for _, p := range a.ports {
		t := p.target
		if p.targetName != "" {
			t = a.slicePorts[p.targetName]
		}
		s.Ports = append(s.Ports, Port{Port: p.port, TargetPort: t})
	}
	for _, addr := range slices.Sorted(maps.Keys(a.endpoints)) {
		s.Endpoints = append(s.Endpoints, d.checked(a.ref, a.endpoints[addr]))
	}
	if len(s.Endpoints) > maxEndpoints {
		d.skip("service "+a.ref, "over "+strconv.Itoa(maxEndpoints)+" endpoints: endpoint list left out")
		s.Endpoints = nil
	}
	s.Workload = d.workloadOf(a.ref, s.Endpoints)
	return s
}

// checked drops what of e fails the map's checks: an endpoint is still
// worth its address when its pod's name is odd.
func (d *discovery) checked(ref string, e Endpoint) Endpoint {
	ns, _, _ := strings.Cut(ref, "/")
	if e.Pod != "" && (!validPodRef(e.Pod) || !strings.HasPrefix(e.Pod, ns+"/")) {
		// A pod of another namespace (or an odd name): its UID would let
		// a server end match an endpoint the Service cannot have.
		d.skip("endpoint "+strconv.Quote(e.Pod), "pod name fails the map's checks or is outside "+ns)
		e.Pod, e.UID = "", ""
	}
	return e
}

// workloadOf is the one workload every endpoint's pod belongs to, or ""
// — flagged when the endpoints disagree or one has none of the two
// kinds.
func (d *discovery) workloadOf(ref string, eps []Endpoint) string {
	found, unnamed := map[string]bool{}, false
	for _, e := range eps {
		found[d.workloads[e.UID]] = true
		unnamed = unnamed || e.UID == ""
	}
	if len(found) == 1 {
		for w := range found {
			if w != "" && validWorkload(w, ref) == nil {
				return w
			}
		}
	}
	detail := "endpoints are not all pods of one deployment or statefulset"
	if unnamed {
		detail = "an endpoint names no pod of its namespace (no targetRef, or another namespace's): workload unknown"
	}
	if len(found) > 0 {
		d.flags = append(d.flags, Flag{Kind: FlagAmbiguousWorkload, Subject: ref, Detail: detail})
	}
	return ""
}
