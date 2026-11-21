package tui

import (
	"fmt"
	"strings"

	"iormesh/internal/routemap"
	"iormesh/internal/viewmodel"
)

// The routed edges on the edges screen (task bf4, PLAN.md M7 "Ingress
// route mapping"). A routed edge is a row after the traced edges — its
// client, "=>", its last hop (the workload, else the pod or Service) and
// "routed BASIS" — and every route flag a row after those ("flag",
// subject, kind), so a flag is on screen beside the edges it qualifies,
// not only in the health text. The rows stay narrow — the FROM column
// says "flag", not "route flag" — so STATE and CONNS still show at 160
// columns, where a flag kind like route_contradicted widens STATE;
// the whole chain, client -> FQDN -> ingress -> Service -> workload, is
// the detail beside it. A row's model index runs on past the edges:
// len(Edges)+j is route j, len(Edges)+len(Routes)+k flag k. A model
// without routes lists exactly the edges, as before.

// What an edges-screen row shows.
const (
	itemEdge = iota
	itemRoute
	itemFlag
)

// edgeItem says what the edges screen's model index i is: a traced edge,
// a routed edge or a route flag, with its index in that list.
func (m *Model) edgeItem(i int) (kind, j int) {
	ne, nr := len(m.vm.Edges), len(m.vm.Routes)
	switch {
	case i < ne:
		return itemEdge, i
	case i < ne+nr:
		return itemRoute, i - ne
	}
	return itemFlag, i - ne - nr
}

// routeRows are the routed edges' and the flags' rows, their indices
// continuing after the edges'.
func (m *Model) routeRows(cells [][]string, idx []int) ([][]string, []int) {
	n := len(m.vm.Edges)
	for _, r := range m.vm.Routes {
		hops := r.Hops()
		cells = append(cells, []string{r.From, "=>", hops[len(hops)-1], "routed " + r.Basis, fmt.Sprint(r.Conns)})
		idx = append(idx, n)
		n++
	}
	for _, f := range m.vm.Health.RouteFlags {
		conns := "-" // a flag of the map itself (skipped, shadowed, ambiguous_*) counts none
		if f.Conns > 0 {
			conns = fmt.Sprint(f.Conns)
		}
		cells = append(cells, []string{"flag", "!", f.Subject, f.Kind, conns})
		idx = append(idx, n)
		n++
	}
	return cells, idx
}

// edgesDetail is the edges screen's detail for model index i.
func (m *Model) edgesDetail(b *strings.Builder, i int) {
	switch kind, j := m.edgeItem(i); kind {
	case itemEdge:
		m.edgeDetail(b, m.vm.Edges[j])
	case itemRoute:
		m.routeDetail(b, m.vm.Routes[j])
	default:
		flagDetail(b, m.vm.Health.RouteFlags[j])
	}
}

// routeDetail is one routed edge: its hops one per line, what it stands
// on, when it was seen and the flags about it.
func (m *Model) routeDetail(b *strings.Builder, r viewmodel.Route) {
	fmt.Fprintf(b, "routed     %s\n", strings.Join(r.Hops(), " -> "))
	for _, h := range [][2]string{{"client", r.From}, {"fqdn", r.FQDN}, {"ingress", r.Ingress},
		{"service", r.Service}, {"workload", r.Workload}, {"pod", r.Pod}} {
		if h[1] != "" {
			fmt.Fprintf(b, "  %-9s%s\n", h[0], h[1])
		}
	}
	fmt.Fprintf(b, "basis      %s\n", basisText(r.Basis))
	fmt.Fprintf(b, "carries    %s\n", viewmodel.RouteNote(r))
	if r.Origin != "" {
		fmt.Fprintf(b, "map        %s\n", r.Origin)
	}
	if !r.First.IsZero() {
		fmt.Fprintf(b, "seen       %s - %s UTC\n", r.First.Format(timeLayout), r.Last.Format(timeLayout))
	}
	for _, f := range viewmodel.FlagsOf(r, m.vm.Health.RouteFlags) {
		fmt.Fprintf(b, "flag       %s\n", viewmodel.FlagText(f))
	}
}

// flagDetail is one route flag in full.
func flagDetail(b *strings.Builder, f viewmodel.RouteFlag) {
	fmt.Fprintf(b, "route flag %s\n", f.Kind)
	fmt.Fprintf(b, "subject    %s\n", f.Subject)
	fmt.Fprintf(b, "detail     %s\n", f.Detail)
	if f.Conns > 0 {
		fmt.Fprintf(b, "conns      %d\n", f.Conns)
	}
	b.WriteString("\nthe route map and the traces disagree here, or the map left it open\n")
}

// basisText says what a routed edge's basis means, so an edge the map
// completed is never read as traced.
func basisText(basis string) string {
	switch basis {
	case routemap.BasisFused:
		return basis + " (both ends traced; the map names the Service dialed)"
	case routemap.BasisService:
		return basis + " (the pod behind the ClusterIP was not traced; completed from the map)"
	case routemap.BasisRoute:
		return basis + " (the FQDN's route through an ingress; completed from the map, not traced)"
	}
	return basis
}

// servicesRoutes lists the routed edges leaving service name.
func (m *Model) servicesRoutes(b *strings.Builder, name string) {
	for _, r := range m.vm.Routes {
		if r.From == name {
			fmt.Fprintf(b, "routed     %s (%s)\n", strings.Join(r.Hops()[1:], " -> "), viewmodel.RouteNote(r))
		}
	}
}
