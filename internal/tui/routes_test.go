package tui

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"iormesh/internal/viewmodel"
)

// golden compares got with testdata/name. -update (the flag teatest
// already registers for its own goldens) rewrites it.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Errorf("%s differs (go test ./internal/tui -update to accept):\n%s", path, got)
	}
}

// routedFixture is the fixture with the M7 ingress scenario's routed
// edges and two flags, one about the routed FQDN.
func routedFixture() viewmodel.Model {
	m := fixture()
	t0 := time.Date(2025, 11, 20, 12, 0, 0, 0, time.UTC)
	m.Routes = []viewmodel.Route{
		{From: "orders", Service: "prod/db", Workload: "prod/statefulset/db", Basis: "service", Endpoints: 2,
			Origin: "static", Conns: 1, First: t0, Last: t0},
		{From: "gateway", FQDN: "shop.example.org", Ingress: "traefik", Service: "prod/backend",
			Workload: "prod/deployment/backend", Basis: "route", Origin: "discovered", Conns: 3, First: t0, Last: t0},
	}
	m.Health.RouteFlags = []viewmodel.RouteFlag{
		{Kind: "route_contradicted", Subject: "shop.example.org", Detail: "asked for it, reached prod/other", Conns: 1},
		{Kind: "skipped", Subject: "ingress/prod/bad", Detail: "a name fails the map's checks"},
	}
	return m
}

// routedSized is the explorer over m at w×h.
func routedSized(m viewmodel.Model, w, h int) *Model {
	e := New(m)
	e.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return e
}

// TestRoutesGolden: the edges screen lists the routed edges after the
// traced ones — client => FQDN -> ingress -> Service -> workload — and
// each route flag as a row; a routed edge's detail spells out its hops,
// its basis (never read as traced) and the flags about it; a flag's
// detail says what it is. ⏎ on either says why nothing opens.
func TestRoutesGolden(t *testing.T) {
	m := routedSized(routedFixture(), 160, 30)
	press(m, "2", "down", "down", "down")
	route := plain(m)
	press(m, "down")
	flagged := plain(m)
	golden(t, "routes_edges.golden", route+"\n----\n"+flagged+"\n")
	for _, want := range []string{"=>  prod/deployment/backend", "shop.example.org -> traefik -> prod/backend", "routed route",
		"route flag", "completed from the map, not traced", "flag       route_contradicted shop.example.org",
		"2 edges · 2 routed"} {
		if !strings.Contains(route+flagged, want) {
			t.Errorf("edges screen lacks %q", want)
		}
	}
	press(m, "enter")
	if m.cur != scrEdges || !strings.Contains(plain(m), "nothing to open") {
		t.Errorf("enter on a flag moved to screen %d or said nothing:\n%s", m.cur, plain(m))
	}
	press(m, "1", "down", "down")
	if !strings.Contains(plain(m), "routed     prod/db -> prod/statefulset/db (service, 1 connections; 2 endpoints") {
		t.Errorf("the service detail lacks its routed edge:\n%s", plain(m))
	}
}

// TestRoutesHostile: route names holding an escape sequence, markup and
// thousands of runes print Go-quoted and cut to the terminal, on every
// screen and size, with the cursor on every row.
func TestRoutesHostile(t *testing.T) {
	bad := "<b>" + esc + strings.Repeat("y", 4000)
	vm := routedFixture()
	vm.Routes = append(vm.Routes, viewmodel.Route{From: bad, FQDN: bad, Ingress: bad, Service: bad, Workload: bad,
		Pod: bad, Basis: bad, Origin: bad})
	vm.Health.RouteFlags = append(vm.Health.RouteFlags, viewmodel.RouteFlag{Kind: bad, Subject: bad, Detail: bad})
	for _, sz := range sizes {
		m := routedSized(vm, sz[0], sz[1])
		press(m, "2")
		for range len(vm.Edges) + len(vm.Routes) + len(vm.Health.RouteFlags) {
			checkFrame(t, m, sz[0], sz[1])
			if strings.Contains(m.Render(), esc) {
				t.Fatalf("%dx%d: raw escape on the edges screen", sz[0], sz[1])
			}
			press(m, "down")
		}
	}
}

// TestNoRoutesUnchanged: a model whose routes and flags are empty lists
// draws every screen exactly as one that has none at all.
func TestNoRoutesUnchanged(t *testing.T) {
	empty := fixture()
	empty.Routes, empty.Health.RouteFlags = []viewmodel.Route{}, []viewmodel.RouteFlag{}
	for s := range numScreens {
		a, b := newSized(160, 48), routedSized(empty, 160, 48)
		press(a, string(rune('1'+s)), "down")
		press(b, string(rune('1'+s)), "down")
		if a.Render() != b.Render() {
			t.Errorf("screen %d differs with empty route lists", s)
		}
	}
	if strings.Contains(plain(newSized(160, 48)), "routed") {
		t.Error("a routeless model mentions routed edges")
	}
}
