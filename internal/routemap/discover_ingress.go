package routemap

import (
	"cmp"
	"regexp"
	"strconv"
)

// The ingress objects: which host each routes to which Service. Paths
// are not kept — a route is per FQDN (decision #15's map), so a host
// whose paths go to different Services has no single answer and is
// flagged ambiguous rather than routed by a guess.

type ingressObj struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Rules []struct {
			Host string `json:"host"`
			HTTP *struct {
				Paths []struct {
					Backend struct {
						Service *struct {
							Name string `json:"name"`
						} `json:"service"`
					} `json:"backend"`
				} `json:"paths"`
			} `json:"http"`
		} `json:"rules"`
	} `json:"spec"`
}

// addIngresses adds each rule's host with its paths' Services. A rule
// without a host (the catch-all) names no FQDN, and a resource backend
// no Service; both add nothing.
func (d *discovery) addIngresses(data []byte) error {
	items, err := list[ingressObj](data)
	for _, ing := range items {
		label := "ingress/" + ing.Metadata.Namespace + "/" + ing.Metadata.Name
		for _, r := range ing.Spec.Rules {
			if r.Host == "" || r.HTTP == nil {
				continue
			}
			for _, p := range r.HTTP.Paths {
				if p.Backend.Service != nil {
					d.backend(r.Host, label, ing.Metadata.Namespace, p.Backend.Service.Name)
				}
			}
		}
	}
	return err
}

type ingressRouteObj struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Routes []struct {
			Match    string `json:"match"`
			Services []struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
				Kind      string `json:"kind"`
			} `json:"services"`
		} `json:"routes"`
	} `json:"spec"`
}

// addIngressRoutes adds Traefik's IngressRoutes: the hosts of each
// route's match rule, with its Services. A TraefikService (weighted or
// mirrored) has no single Service behind it and is skipped.
func (d *discovery) addIngressRoutes(data []byte) error {
	items, err := list[ingressRouteObj](data)
	for _, ir := range items {
		md := ir.Metadata
		label := "ingressroute/" + md.Namespace + "/" + md.Name
		for _, r := range ir.Spec.Routes {
			for _, host := range MatchHosts(r.Match) {
				for _, s := range r.Services {
					if s.Kind != "" && s.Kind != "Service" {
						d.skip(label, "backend kind "+strconv.Quote(s.Kind)+" names no single service")
						continue
					}
					d.backend(host, label, cmp.Or(s.Namespace, md.Namespace), s.Name)
				}
			}
		}
	}
	return err
}

// backend records that ingress label routes host to ns/service, when
// the names pass the map's checks.
func (d *discovery) backend(host, label, ns, service string) {
	host = NormalizeFQDN(host)
	ref := ns + "/" + service
	switch {
	case !validFQDN(host):
		d.skip(label, "host "+strconv.Quote(host)+" fails the map's checks")
		return
	case !validRef(ref) || len(label) > maxIngress || !ingressLabel.MatchString(label):
		d.skip(label, "service or object name fails the map's checks")
		return
	}
	if d.hosts == nil {
		d.hosts = map[string]map[backend]bool{}
	}
	if d.hosts[host] == nil {
		d.hosts[host] = map[backend]bool{}
	}
	d.hosts[host][backend{ingress: label, service: ref}] = true
}

// hostCall finds Traefik's Host matcher — not HostSNI, HostRegexp or
// HostHeader — and its argument list of backquoted names: Host(`a`) in
// v3, Host(`a`, `b`) in v2.
var (
	hostCall = regexp.MustCompile("(?:^|[^A-Za-z0-9_])Host\\(((?:\\s*`[^`]*`\\s*,?)+)\\)")
	quoted   = regexp.MustCompile("`([^`]*)`")
)

// MatchHosts returns the host names a Traefik match rule's Host matchers
// name, in order. It reads the routing rule, the configuration — never a
// request. A host under a negation is still returned: the rule is
// configuration the operator wrote, and an odd one shows up as an
// ambiguous or contradicted route rather than vanishing.
func MatchHosts(match string) []string {
	var out []string
	for _, call := range hostCall.FindAllStringSubmatch(match, -1) {
		for _, q := range quoted.FindAllStringSubmatch(call[1], -1) {
			out = append(out, q[1])
		}
	}
	return out
}
