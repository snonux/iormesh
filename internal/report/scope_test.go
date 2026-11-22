package report

import (
	"testing"

	"iormesh/internal/schema"
	"iormesh/internal/servicedb"
)

// TestReportLocalEndpointPerHost: two hosts each call their own
// 127.0.0.1:5432, which the service database lists as two endpoints
// (one per host). Each host's call is labelled with its own host's
// endpoint, never the other machine's; a routable address is shared.
func TestReportLocalEndpointPerHost(t *testing.T) {
	c := NewCollector(map[string]map[uint64]schema.Method{"s1": methods})
	for _, h := range []struct{ host, conn string }{{"hA", "a1"}, {"hB", "b1"}} {
		tr := &trace{host: h.host}
		tr.add(10, 11, schema.KindConnect, 5, 0, h.conn, schema.Args{RemoteAddr: ptr("127.0.0.1"),
			RemotePort: ptr(uint16(5432))}, nil)
		tr.add(10, 11, schema.KindConnect, 6, 0, h.conn+"r", schema.Args{RemoteAddr: ptr("10.0.0.9"),
			RemotePort: ptr(uint16(443))}, nil)
		for i := range tr.events {
			c.Add(Source{Project: "shop", Host: h.host, Session: "s1"}, &tr.events[i])
		}
	}
	r := Build(c, Input{Endpoints: []servicedb.Endpoint{
		{IP: "10.0.0.9", Port: 443},
		{IP: "127.0.0.1", Port: 5432, Host: "hA"},
		{IP: "127.0.0.1", Port: 5432, Host: "hB"},
	}})
	got := map[string][]string{}
	for _, p := range r.Processes {
		for _, cn := range p.Conns {
			got[p.Host] = append(got[p.Host], cn.Service)
		}
	}
	want := map[string][]string{
		"hA": {"127.0.0.1:5432@hA", "10.0.0.9:443"},
		"hB": {"127.0.0.1:5432@hB", "10.0.0.9:443"},
	}
	for host, w := range want {
		if g := got[host]; len(g) != 2 || g[0] != w[0] || g[1] != w[1] {
			t.Errorf("host %s services %v, want %v", host, g, w)
		}
	}
}
