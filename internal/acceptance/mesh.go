package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"iormesh/internal/normalize"
	"iormesh/internal/viewmodel"
)

// phaseMesh refreshes once over the whole registry and checks the merged
// mesh, criterion by criterion, from offline builds of the store.
func (r *Runner) phaseMesh(ctx context.Context) {
	r.phase("refresh and the merged mesh")
	all, err := r.checkRefresh(ctx)
	r.res.setErr(cRefresh, err, "one refresh: every project and both hosts, no damaged segment")
	r.check(cSingle, func() (string, error) { return r.checkSingle(ctx) })
	r.check(cCross, func() (string, error) { return r.checkCross(ctx) })
	if r.cfg.NATPort == 0 {
		r.res.set(cNAT, Skip, "%s unset: no DNAT rule on HOST2 to cross (runbook step 4)", EnvNATPort)
	} else {
		r.check(cNAT, func() (string, error) { return r.checkNAT(ctx) })
	}
	if err == nil {
		r.check(cMicro, func() (string, error) { return checkMicro(all, r.cfg) })
	} else {
		r.res.set(cMicro, NotRun, "blocked: refresh failed")
	}
	r.check(cMicroHost, func() (string, error) { return r.checkSpread(ctx) })
	r.check(cOffline, func() (string, error) { return r.checkOffline(ctx) })
	r.check(cNaming, func() (string, error) { return r.checkNaming(ctx) })
}

// check runs one criterion's check unless its workload already failed.
func (r *Runner) check(id string, f func() (string, error)) {
	if err := r.workloadErr[id]; err != nil {
		r.res.set(id, Fail, "workload: %v", err)
		return
	}
	detail, err := f()
	r.res.setErr(id, err, detail)
}

// meshProjects are the projects refresh builds together: every one the
// deploy started.
func (r *Runner) meshProjects() []string {
	var ps []string
	for _, p := range r.cfg.projects() {
		ps = append(ps, p.name)
	}
	return ps
}

// checkRefresh runs `refresh` (collect, then build of every project in
// the store) and checks the merged build saw everything.
func (r *Runner) checkRefresh(ctx context.Context) (viewmodel.Model, error) {
	if _, err := r.ctlRun(ctx, "refresh"); err != nil {
		return viewmodel.Model{}, err
	}
	m, g, err := readBuild(r.buildDir(r.meshProjects()))
	if err != nil {
		return m, err
	}
	want := slices.Sorted(slices.Values(r.meshProjects()))
	switch {
	case !slices.Equal(m.Run.Projects, want):
		return m, fmt.Errorf("refresh built projects %v, want %v", m.Run.Projects, want)
	case len(m.Run.Hosts) != 2:
		return m, fmt.Errorf("refresh built hosts %v, want both", m.Run.Hosts)
	case len(m.Health.BadSegments) > 0:
		return m, fmt.Errorf("damaged segments: %v", m.Health.BadSegments)
	case len(g.Nodes) == 0:
		return m, errors.New("empty graph export")
	}
	return m, nil
}

// checkSingle holds the single-host scenario's merged mesh to the
// scenario's golden: what the controller rebuilt from the collected
// segments must satisfy what a single-host run satisfies (the rig checks
// the rebuild equals graph.json exactly on every integration run).
func (r *Runner) checkSingle(ctx context.Context) (string, error) {
	_, g, err := r.build(ctx, projSingle)
	if err != nil {
		return "", err
	}
	out, err := r.ctlRun(ctx, "query", "-project", projSingle, "-max-bytes", "268435456")
	if err != nil {
		return "", err
	}
	evs, err := readSlice(out)
	if err != nil {
		return "", err
	}
	want, err := normalize.LoadExpectation(filepath.Join(r.cfg.Goldens, "tcp-loopback", "expect.json"))
	if err != nil {
		return "", err
	}
	got, err := normalize.Normalize(normalize.Trace{Events: evs, Graph: g},
		normalize.Options{Bracket: "tcp-loopback", TmpPrefix: r.remoteBase(r.cfg.Host1) + "/tcp-loopback"})
	if err != nil {
		return "", err
	}
	if vs := normalize.Check(got, want); len(vs) > 0 {
		return "", fmt.Errorf("tcp-loopback golden: %d violations, first: %s", len(vs), vs[0])
	}
	return fmt.Sprintf("tcp-loopback golden holds on the build of %d collected events", len(evs)), nil
}

// checkCross builds the two-host pair's projects together (one fused
// cross-project edge, nothing unresolved) and the client's alone (the
// connection stays unresolved: the fusion needs both projects).
func (r *Runner) checkCross(ctx context.Context) (string, error) {
	m, g, err := r.build(ctx, projShop, projBilling)
	if err != nil {
		return "", err
	}
	r.clockResult(m)
	ids := fusedCrossHost(g)
	if len(ids) != 1 || m.Run.UnresolvedConns != 0 {
		return "", fmt.Errorf("%s+%s: %d fused cross-host connections, %d unresolved; want 1 and 0",
			projShop, projBilling, len(ids), m.Run.UnresolvedConns)
	}
	if err := r.keepBytes(); err != nil {
		return "", err
	}
	alone, _, err := r.build(ctx, projShop)
	if err != nil {
		return "", err
	}
	if alone.Run.FusedConns != 0 || alone.Run.UnresolvedConns == 0 {
		return "", fmt.Errorf("%s alone: %d fused, %d unresolved; want its end unresolved",
			projShop, alone.Run.FusedConns, alone.Run.UnresolvedConns)
	}
	return fmt.Sprintf("conn %s fused across %v; the client project alone stays unresolved", ids[0], m.Run.Hosts), nil
}

// checkNAT finds the NAT pair's fused connection and reads the ports
// each end saw: the client dialed the DNAT'ed port, the server accepted
// on its own.
func (r *Runner) checkNAT(ctx context.Context) (string, error) {
	_, g, err := r.build(ctx, projNATClient, projNATServer)
	if err != nil {
		return "", err
	}
	ids := fusedCrossHost(g)
	if len(ids) != 1 {
		return "", fmt.Errorf("%d fused cross-host connections, want 1 (DNAT rule in place?)", len(ids))
	}
	out, err := r.ctlRun(ctx, "query", "-project", projNATClient, "-project", projNATServer, "-conn", ids[0])
	if err != nil {
		return "", err
	}
	evs, err := readSlice(out)
	if err != nil {
		return "", err
	}
	dialed, accepted := natEnds(evs)
	if int(dialed) != r.cfg.NATPort || int(accepted) != r.cfg.port(portNATInner) {
		return "", fmt.Errorf("client dialed port %d, server accepted on %d; want %d and %d",
			dialed, accepted, r.cfg.NATPort, r.cfg.port(portNATInner))
	}
	return fmt.Sprintf("conn %s fused: client saw :%d, server :%d", ids[0], dialed, accepted), nil
}

// checkMicro checks the refresh's model: each microservices server has
// its connections, all fused, with the TLS hint its hop has (orders and
// billing serve TLS, inventory plain).
func checkMicro(m viewmodel.Model, c Config) (string, error) {
	return checkMicroAt(m, c, microPorts{portOrders, portBilling, portInventory})
}

// microPorts are one microservices run's server port offsets.
type microPorts struct{ orders, billing, inventory int }

// checkMicroAt is checkMicro for the run whose servers listen on ps.
func checkMicroAt(m viewmodel.Model, c Config, ps microPorts) (string, error) {
	want := []struct {
		port      int
		tls       string
		minConns  int64
		component string
	}{{ps.orders, "tls", 1, "orders"}, {ps.billing, "tls", 1, "billing"},
		{ps.inventory, "plain", 2, "inventory"}}
	var seen []string
	for _, w := range want {
		s, ok := serviceByPort(m, c.port(w.port))
		if !ok {
			return "", fmt.Errorf("%s (:%d) not among the services", w.component, c.port(w.port))
		}
		if s.Conns < w.minConns || s.Unresolved != 0 || s.TLS != w.tls {
			return "", fmt.Errorf("%s: %d conns, %d unresolved, tls %q; want >= %d, 0, %q",
				s.Name, s.Conns, s.Unresolved, s.TLS, w.minConns, w.tls)
		}
		seen = append(seen, fmt.Sprintf("%s %s x%d", s.Name, s.TLS, s.Conns))
	}
	for _, e := range m.Edges {
		if microPort(e.To, c, ps) && e.State != viewmodel.StateFused {
			return "", fmt.Errorf("edge %s -> %s is %s", e.From, e.To, e.State)
		}
	}
	return "all hops fused: " + strings.Join(seen, ", "), nil
}

// serviceByPort finds the service with an endpoint on port.
func serviceByPort(m viewmodel.Model, port int) (viewmodel.Service, bool) {
	suffix := fmt.Sprintf(":%d", port)
	for _, s := range m.Services {
		for _, ep := range s.Endpoints {
			if strings.HasSuffix(ep, suffix) {
				return s, true
			}
		}
	}
	return viewmodel.Service{}, false
}

// microPort reports whether a service label names one of ps's ports.
func microPort(label string, c Config, ps microPorts) bool {
	for _, off := range []int{ps.orders, ps.billing, ps.inventory} {
		if strings.HasSuffix(label, fmt.Sprintf(":%d", c.port(off))) {
			return true
		}
	}
	return false
}
