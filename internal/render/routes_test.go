package render

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// golden compares got with testdata/name (-update rewrites it).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs (go test ./internal/render -update to accept):\n%s", path, got)
	}
}

// routedModel is the M7 ingress scenario's routes: the fused ClusterIP
// edge, an untraced Service of two endpoints, and the FQDN through
// Traefik; plus a flag about the routed Service and one about nothing
// drawn.
func routedModel() viewmodel.Model {
	t0 := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	return viewmodel.Model{
		Routes: []viewmodel.Route{
			{From: "checkout", Service: "prod/backend", Workload: "prod/deployment/backend",
				Pod: "prod/backend-7d9f-a", Basis: "fused", Endpoints: 1, Conns: 2, First: t0, Last: t0},
			{From: "checkout", Service: "prod/db", Workload: "prod/statefulset/db", Basis: "service",
				Endpoints: 2, Conns: 1, First: t0, Last: t0},
			{From: "checkout", FQDN: "shop.example.org", Ingress: "traefik", Service: "prod/backend",
				Workload: "prod/deployment/backend", Basis: "route", Conns: 3, First: t0, Last: t0},
		},
		Health: viewmodel.Health{RouteFlags: []viewmodel.RouteFlag{
			{Kind: "route_contradicted", Subject: "shop.example.org", Detail: "asked for it, reached prod/other",
				Conns: 1},
			{Kind: "unknown_service", Subject: "10.96.0.99:80", Detail: "in a service CIDR, no Service holds it"},
		}},
	}
}

// TestGraphRoutesGolden: the routed edges are drawn below the columns —
// each one row, client → FQDN → ingress → Service → workload (→ pod) —
// a completed edge dashed ("routed"), a fused one solid, and every
// route flag a line of its own.
func TestGraphRoutesGolden(t *testing.T) {
	out := Graph(hostileGraph(), NamesOf(routedModel()))
	wellFormed(t, out)
	golden(t, "graph_routes.golden", out)
	s := string(out)
	for _, want := range []string{`<rect class="fqdn"`, `<rect class="ingress"`, `<rect class="workload"`,
		`<line class="routed"`, `<line class="fused"`, ">shop.example.org<", ">traefik<",
		"route_contradicted shop.example.org", "unknown_service 10.96.0.99:80", "2 endpoints, pod unknown"} {
		if !strings.Contains(s, want) {
			t.Errorf("graph lacks %q", want)
		}
	}
}

// TestGraphWithoutRoutesUnchanged: a model without routes draws the
// graph exactly as the routeless renderer did — no band, no extra style.
func TestGraphWithoutRoutesUnchanged(t *testing.T) {
	m := routedModel()
	m.Routes, m.Health.RouteFlags = nil, nil
	if !bytes.Equal(Graph(hostileGraph(), NamesOf(m)), Graph(hostileGraph(), Names{})) {
		t.Error("an empty route list changed the drawing")
	}
	if s := string(Graph(hostileGraph(), Names{})); strings.Contains(s, "routed") || strings.Contains(s, "rect.fqdn") {
		t.Errorf("the band or its style is drawn without routes:\n%s", s)
	}
}

// TestGraphRoutesHostile: names holding markup, an escape sequence and
// thousands of runes stay text — escaped, Go-quoted, cut — a flag whose
// kind is markup gets no class of its own, and the lists are capped and
// counted.
func TestGraphRoutesHostile(t *testing.T) {
	bad := `</text><script>alert(1)</script>"'&` + "\x1b[2J" + strings.Repeat("x", 5000)
	m := viewmodel.Model{Health: viewmodel.Health{}}
	for range maxRouteRows + 3 {
		m.Routes = append(m.Routes, viewmodel.Route{From: bad, FQDN: bad, Ingress: bad, Service: bad,
			Workload: bad, Pod: bad, Basis: `x" onload="alert(2)`})
		m.Health.RouteFlags = append(m.Health.RouteFlags, viewmodel.RouteFlag{Kind: bad, Subject: bad, Detail: bad})
	}
	out := Graph(schema.Graph{}, NamesOf(m))
	wellFormed(t, out)
	s := string(out)
	for _, bad := range []string{"<script", "\x1b", `onload="`, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"} {
		if strings.Contains(s, bad) {
			t.Errorf("graph holds %q", bad)
		}
	}
	for _, want := range []string{`\x1b[2J`, "&lt;/text&gt;", "+3 more routed edges", "+3 more route flags"} {
		if !strings.Contains(s, want) {
			t.Errorf("graph lacks %q", want)
		}
	}
	if n := strings.Count(s, `<rect class="flag"`); n != maxRouteRows {
		t.Errorf("%d flag lines, want %d", n, maxRouteRows)
	}
}
