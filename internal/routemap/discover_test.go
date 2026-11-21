package routemap

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type obj = map[string]any

// items wraps objects as a kubectl list.
func items(t *testing.T, objs ...obj) []byte {
	t.Helper()
	data, err := json.Marshal(obj{"apiVersion": "v1", "kind": "List", "items": objs})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func md(name string, extra obj) obj {
	m := obj{"name": name, "namespace": "prod"}
	for k, v := range extra {
		m[k] = v
	}
	return obj{"metadata": m}
}

func owner(kind, name string) obj {
	return obj{"ownerReferences": []obj{{"kind": kind, "name": name, "controller": true}}}
}

func uidN(n string) string { return "0b6e7a52-0000-4c3e-9a63-00000000000" + n }

func pod(name, n string, own obj) obj {
	own["uid"] = uidN(n)
	return md(name, own)
}

func svc(name, ip string, ports []obj, typ string) obj {
	o := md(name, nil)
	o["spec"] = obj{"type": typ, "clusterIP": ip, "clusterIPs": []string{ip}, "ports": ports}
	return o
}

func slice(service string, ports []obj, eps ...obj) obj {
	o := md(service+"-abcde", obj{"labels": obj{"kubernetes.io/service-name": service}})
	o["addressType"], o["ports"], o["endpoints"] = "IPv4", ports, eps
	return o
}

func ep(addr, n string, ready bool) obj {
	return obj{"addresses": []string{addr}, "conditions": obj{"ready": ready},
		"targetRef": obj{"kind": "Pod", "uid": uidN(n)}}
}

func ingress(name string, rules ...obj) obj {
	o := md(name, nil)
	o["spec"] = obj{"rules": rules}
	return o
}

func rule(host string, services ...string) obj {
	var paths []obj
	for _, s := range services {
		paths = append(paths, obj{"path": "/" + s, "backend": obj{"service": obj{"name": s}}})
	}
	return obj{"host": host, "http": obj{"paths": paths}}
}

func ingressRoute(name, match string, services ...obj) obj {
	o := md(name, nil)
	o["spec"] = obj{"routes": []obj{{"match": match, "services": services}}}
	return o
}

// clusterObjects is a namespace as kubectl lists it: a Deployment's
// backend (named targetPort) and a StatefulSet's db, a Service whose
// endpoints span two workloads, one with an endpoint naming no pod, one
// whose endpoint's pod is another namespace's, headless, ExternalName,
// UDP-only and 65-port Services, a not-ready and a forged endpoint,
// Traefik's own Service with a load-balancer address; an Ingress routing
// the shop FQDN whose status names that address, one host split by path
// across two Services, a Traefik IngressRoute naming two hosts, one
// behind a TraefikService and one negated, and a host carrying an escape
// sequence. The operator names Traefik's Service for the IngressRoutes.
func clusterObjects(t *testing.T) Objects {
	tcp := []obj{{"name": "http", "protocol": "TCP", "port": 80, "targetPort": "web"}}
	var many []obj
	for p := range 65 {
		many = append(many, obj{"port": 1000 + p})
	}
	traefik := svc("traefik", "10.96.0.100", []obj{{"port": 80}}, "LoadBalancer")
	traefik["status"] = obj{"loadBalancer": obj{"ingress": []obj{{"ip": "203.0.113.10"}}}}
	web := ingress("web", rule("Shop.Example.org", "backend"), rule(""),
		rule("split.example.org", "backend", "db"), rule("bad\x1b[31m.example.org", "backend"))
	web["status"] = obj{"loadBalancer": obj{"ingress": []obj{{"ip": "203.0.113.10"}}}}
	foreign := ep("10.244.8.8", "5", true)
	return Objects{
		IngressService: "kube-system/traefik",
		ReplicaSets: [][]byte{items(t, md("backend-7d9f", owner("Deployment", "backend")),
			md("worker-55", owner("Deployment", "worker")))},
		Pods: [][]byte{items(t, pod("backend-7d9f-a", "1", owner("ReplicaSet", "backend-7d9f")),
			pod("backend-7d9f-b", "2", owner("ReplicaSet", "backend-7d9f")),
			pod("db-0", "3", owner("StatefulSet", "db")), pod("worker-55-x", "4", owner("ReplicaSet", "worker-55")),
			obj{"metadata": obj{"name": "intruder", "namespace": "other", "uid": uidN("5")}})},
		Services: [][]byte{items(t, svc("backend", "10.96.0.20", tcp, "ClusterIP"),
			svc("db", "10.96.0.30", []obj{{"port": 5432, "targetPort": 5432}}, "ClusterIP"),
			svc("mixed", "10.96.0.31", []obj{{"port": 80}}, "ClusterIP"),
			svc("headless", "None", []obj{{"port": 80}}, "ClusterIP"),
			svc("dns", "10.96.0.10", []obj{{"port": 53, "protocol": "UDP"}}, "ClusterIP"),
			svc("ext", "", []obj{{"port": 80}}, "ExternalName"), svc("wide", "10.96.0.32", many, "ClusterIP"),
			svc("bare", "10.96.0.33", []obj{{"port": 80}}, "ClusterIP"),
			svc("borrowed", "10.96.0.34", []obj{{"port": 80}}, "ClusterIP"), traefik)},
		EndpointSlices: [][]byte{items(t,
			slice("backend", []obj{{"name": "web", "port": 8080}}, ep("10.244.1.5", "1", true),
				ep("10.244.2.6", "2", true), ep("10.244.2.9", "9", false)),
			slice("db", nil, ep("10.244.4.1", "3", true), obj{"addresses": []string{"10.244.4.1\x1b[2J"}}),
			slice("mixed", nil, ep("10.244.1.5", "1", true), ep("10.244.5.5", "4", true)),
			slice("bare", nil, ep("10.244.1.5", "1", true), obj{"addresses": []string{"10.244.6.6"}}),
			slice("borrowed", nil, foreign))},
		Ingresses: [][]byte{items(t, web)},
		IngressRoutes: [][]byte{items(t,
			ingressRoute("api", "Host(`api.example.org`) || Host(`api2.example.org`) && PathPrefix(`/v1`)",
				obj{"name": "backend", "port": 80}),
			ingressRoute("canary", "HostSNI(`x.example.org`) || Host(`canary.example.org`)",
				obj{"name": "weighted", "kind": "TraefikService"}),
			ingressRoute("catchall", "!Host(`internal.example.org`)", obj{"name": "backend"}))},
	}
}

// TestDiscoverGolden: the namespace's objects become the map in
// testdata/discovered.golden — Services with their ClusterIPs, resolved
// target ports, serving endpoints by pod and workload, routes for hosts
// with one Service — and every object left out is a flag.
func TestDiscoverGolden(t *testing.T) {
	m, flags, err := Discover(clusterObjects(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(struct {
		Map   *Map
		Flags []Flag
	}{m, flags}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "discovered.golden", string(got)+"\n")
}

// TestDiscoverRefusesGarbage: a list that is not kubectl's JSON fails
// discovery whole.
func TestDiscoverRefusesGarbage(t *testing.T) {
	if _, _, err := Discover(Objects{Services: [][]byte{[]byte("{not json")}}); err == nil {
		t.Error("garbage accepted")
	}
}

// TestMatchHosts: only Host matchers count, in v2's list form too.
func TestMatchHosts(t *testing.T) {
	for match, want := range map[string]string{
		"Host(`a.example.org`)":                        "a.example.org",
		"Host(`a.example.org`, `b.example.org`)":       "a.example.org b.example.org",
		"HostRegexp(`.*`) || HostSNI(`c.example.org`)": "",
		"PathPrefix(`/x`) && Host(`d.example.org`)":    "d.example.org",
		"MyHost(`e.example.org`)":                      "",
	} {
		if got := strings.Join(MatchHosts(match), " "); got != want {
			t.Errorf("%s: %q, want %q", match, got, want)
		}
	}
}

// TestSnapshotRoundTrip: a snapshot saved is the one loaded, 0600; a
// missing one is none; an unknown field, data after the JSON value, a
// writable file or a forged context is refused.
func TestSnapshotRoundTrip(t *testing.T) {
	m, flags, err := Discover(clusterObjects(t))
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "share")
	path := SnapshotPath(filepath.Join(dir, "services.db"))
	s := Snapshot{Taken: time.Unix(1_763_600_000, 0).UTC(), Context: "kind-test", Namespaces: []string{"prod"},
		Map: *m, Flags: flags}
	if err := SaveSnapshot(path, s); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("snapshot mode: %v %v", info, err)
	}
	back, err := LoadSnapshot(path)
	if err != nil || len(back.Map.Services) != len(m.Services) || !slices.Equal(back.Flags, flags) {
		t.Fatalf("loaded %+v, %v", back, err)
	}
	if s, err := LoadSnapshot(filepath.Join(dir, "none.json")); s != nil || err != nil {
		t.Errorf("missing: %v %v", s, err)
	}
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), `"taken"`, `"extra": 1, "taken"`, 1)),
		0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); err == nil {
		t.Error("unknown field accepted")
	}
	if err := os.WriteFile(path, append(append([]byte{}, data...), []byte(`{"taken":"x"}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); !errors.Is(err, ErrInvalid) {
		t.Errorf("trailing data: %v", err)
	}
	s.Context = "-oProxy"
	if err := SaveSnapshot(path, s); !errors.Is(err, ErrInvalid) {
		t.Errorf("forged context saved: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); !errors.Is(err, ErrUnsafeFile) {
		t.Errorf("writable snapshot: %v", err)
	}
}
