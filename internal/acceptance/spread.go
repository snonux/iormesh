package acceptance

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strconv"
)

// The microservices spread (criterion M4-9, task 1f4): the four services
// on the two hosts, each its own project, every server bound to its
// host's address and its TLS certificate naming that address:
//
//	HOST1 (ADDR1)  gateway, inventory :base+15
//	HOST2 (ADDR2)  orders :base+16 (TLS), billing :base+14 (TLS)
//
// Two hops cross the hosts — gateway→orders (TLS) and orders→inventory
// (plain) — and two stay on one: orders→billing (TLS, on HOST2's own
// address) and gateway→inventory. Two hosts cannot put every service on
// a host of its own; the rig's three emulated hosts do
// (TestMicroservicesAcrossHosts).

// runSpread runs the spread, leaves first, each server only once its
// port listens. The gateway pins orders' certificate from its own --dir
// on HOST1, so the public certificate orders published on HOST2 is
// copied there first, as a deployment would ship it.
func (r *Runner) runSpread(ctx context.Context) error {
	h1, h2 := r.cfg.Host1, r.cfg.Host2
	d1, err := r.scenarioDir(ctx, h1, spreadDir)
	if err != nil {
		return err
	}
	d2, err := r.scenarioDir(ctx, h2, spreadDir)
	if err != nil {
		return err
	}
	waits, err := r.startSpreadServers(ctx, d1, d2)
	if err == nil {
		err = r.copyCert(ctx, h2, d2, h1, d1, "orders.crt")
	}
	if err == nil {
		a1, a2 := r.info[h1].addr, r.info[h2].addr
		err = r.java(ctx, h1, "microservices", "gateway", d1,
			"--orders-host", a2, "--orders-port", r.spreadPort(portSpreadOrders),
			"--inventory-host", a1, "--inventory-port", r.spreadPort(portSpreadInventory))
	}
	if err != nil {
		// Servers on both hosts still wait for their calls: stop them
		// before waiting, or the drain would last roleTimeout.
		r.stopRoles(ctx, h1)
		r.stopRoles(ctx, h2)
	}
	return firstErr(append([]error{err}, drain(waits)...)...)
}

// spreadServer is one server of the spread: its host, role, port and
// options after --role.
type spreadServer struct {
	host, role string
	port       int
	opts       []string
}

// startSpreadServers starts billing, inventory and orders in order, each
// once the one before listens, and returns their results to drain.
func (r *Runner) startSpreadServers(ctx context.Context, d1, d2 string) ([]<-chan error, error) {
	h1, h2 := r.cfg.Host1, r.cfg.Host2
	a1, a2 := r.info[h1].addr, r.info[h2].addr
	p := r.spreadPort
	servers := []spreadServer{
		{h2, "billing", portSpreadBilling, []string{"--bind", a2, "--port", p(portSpreadBilling)}},
		{h1, "inventory", portSpreadInventory, []string{"--bind", a1, "--port", p(portSpreadInventory)}},
		{h2, "orders", portSpreadOrders, []string{"--bind", a2, "--port", p(portSpreadOrders),
			"--inventory-host", a1, "--inventory-port", p(portSpreadInventory),
			"--billing-host", a2, "--billing-port", p(portSpreadBilling)}},
	}
	var waits []<-chan error
	for _, s := range servers {
		dir := d1
		if s.host == h2 {
			dir = d2
		}
		waits = append(waits, r.javaBG(ctx, s.host, "microservices", s.role, dir, s.opts...))
		if err := r.waitListening(ctx, s.host, r.cfg.port(s.port)); err != nil {
			return waits, err
		}
	}
	return waits, nil
}

func (r *Runner) spreadPort(off int) string { return strconv.Itoa(r.cfg.port(off)) }

// copyCert copies the public certificate name from one host's dir to
// another's through the controller, base64 on the wire (a DER file is
// binary). Only a certificate ever travels: the private key never left
// its JVM.
func (r *Runner) copyCert(ctx context.Context, from, fromDir, to, toDir, name string) error {
	b64, err := r.remote(ctx, from, "base64", "-w0", "--", path.Join(fromDir, name))
	if err != nil {
		return fmt.Errorf("%s: reading %s: %w", from, name, err)
	}
	if b64 == "" {
		return errors.New(from + ": " + name + " is empty")
	}
	_, err = r.remote(ctx, to, "sh", "-c", `printf %s "$1" | base64 -d > "$2"`, "sh", b64,
		path.Join(toDir, name))
	if err != nil {
		return fmt.Errorf("%s: writing %s: %w", to, name, err)
	}
	return nil
}

// checkSpread builds the spread's four projects together: the two
// cross-host hops fused across the hosts, nothing unresolved, and every
// server with its connections fused and its TLS hint (orders and billing
// tls, inventory plain with its two callers).
func (r *Runner) checkSpread(ctx context.Context) (string, error) {
	var names []string
	for _, p := range r.cfg.spreadProjects() {
		names = append(names, p.name)
	}
	m, g, err := r.build(ctx, names...)
	if err != nil {
		return "", err
	}
	ids := fusedCrossHost(g)
	if len(ids) != 2 || m.Run.UnresolvedConns != 0 {
		return "", fmt.Errorf("%v: %d fused cross-host connections, %d unresolved; want 2 and 0",
			names, len(ids), m.Run.UnresolvedConns)
	}
	detail, err := checkMicroAt(m, r.cfg,
		microPorts{portSpreadOrders, portSpreadBilling, portSpreadInventory})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("conns %v fused across %v; %s", ids, m.Run.Hosts, detail), nil
}
