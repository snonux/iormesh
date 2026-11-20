package acceptance

import (
	"context"
	"fmt"
	"path"
	"strconv"
)

// phaseWorkloads runs the scenarios once each while the sessions trace
// them. A failure is kept for the criterion that reads that scenario.
func (r *Runner) phaseWorkloads(ctx context.Context) {
	r.phase("workloads")
	r.workloadErr[cSingle] = r.runLoopback(ctx)
	r.workloadErr[cCross] = r.runPair(ctx, "tcp-two-host", r.info[r.cfg.Host2].addr,
		r.cfg.port(portTwoHost), r.cfg.port(portTwoHost))
	if r.cfg.NATPort != 0 {
		// The client dials the DNAT'ed port; the operator's rule on HOST2
		// sends it to the server's port, so the two ends see two 4-tuples.
		r.workloadErr[cNAT] = r.runPair(ctx, "tcp-nat", r.info[r.cfg.Host2].addr,
			r.cfg.port(portNATInner), r.cfg.NATPort)
	}
	r.workloadErr[cMicro] = r.runMicro(ctx)
	for id, err := range r.workloadErr {
		if err != nil {
			r.logf("  workload for %s failed: %v", id, err)
		}
	}
}

// java runs one workload role on host in the foreground; dir is the
// scenario's --dir under the run's directory.
func (r *Runner) java(ctx context.Context, host, scenario, role, dir string, opts ...string) error {
	argv := []string{"java", "-jar", path.Join(r.remoteBase(host), "workload.jar"), scenario, "--role", role}
	argv = append(argv, opts...)
	_, err := r.remote(ctx, host, append(argv, "--dir", dir)...)
	if err != nil {
		return fmt.Errorf("%s %s --role %s: %w", host, scenario, role, err)
	}
	return nil
}

// javaBG starts a role in the background; the channel yields its result.
func (r *Runner) javaBG(ctx context.Context, host, scenario, role, dir string, opts ...string) <-chan error {
	ch := make(chan error, 1)
	go func() { ch <- r.java(ctx, host, scenario, role, dir, opts...) }()
	return ch
}

// scenarioDir creates and returns a scenario's --dir on host.
func (r *Runner) scenarioDir(ctx context.Context, host, name string) (string, error) {
	dir := path.Join(r.remoteBase(host), name)
	_, err := r.remote(ctx, host, "mkdir", "-p", dir)
	return dir, err
}

// runLoopback is the single-host scenario: tcp-loopback's server and
// client on HOST1, both in project m4single.
func (r *Runner) runLoopback(ctx context.Context) error {
	h := r.cfg.Host1
	dir, err := r.scenarioDir(ctx, h, "tcp-loopback")
	if err != nil {
		return err
	}
	port := strconv.Itoa(r.cfg.port(portLoopback))
	srv := r.javaBG(ctx, h, "tcp-loopback", "server", dir, "--port", port)
	if err := r.waitListening(ctx, h, r.cfg.port(portLoopback)); err != nil {
		return firstErr(err, <-srv)
	}
	cerr := r.java(ctx, h, "tcp-loopback", "client", dir, "--port", port)
	return firstErr(<-srv, cerr)
}

// runPair runs a two-host scenario: its server on HOST2 bound to addr
// and listenPort, its client on HOST1 dialing addr:dialPort.
func (r *Runner) runPair(ctx context.Context, scenario, addr string, listenPort, dialPort int) error {
	d2, err := r.scenarioDir(ctx, r.cfg.Host2, scenario)
	if err != nil {
		return err
	}
	d1, err := r.scenarioDir(ctx, r.cfg.Host1, scenario)
	if err != nil {
		return err
	}
	srv := r.javaBG(ctx, r.cfg.Host2, scenario, "server", d2, "--bind", addr, "--port", strconv.Itoa(listenPort))
	if err := r.waitListening(ctx, r.cfg.Host2, listenPort); err != nil {
		return firstErr(err, <-srv)
	}
	cerr := r.java(ctx, r.cfg.Host1, scenario, "client", d1, "--host", addr, "--port", strconv.Itoa(dialPort))
	return firstErr(<-srv, cerr)
}

// runMicro runs the microservices topology on HOST1, leaves first as the
// harness releases it, each service traced by its own project.
func (r *Runner) runMicro(ctx context.Context) error {
	h := r.cfg.Host1
	dir, err := r.scenarioDir(ctx, h, "microservices")
	if err != nil {
		return err
	}
	p := func(off int) string { return strconv.Itoa(r.cfg.port(off)) }
	servers := []struct {
		role string
		port int
		opts []string
	}{
		{"billing", portBilling, []string{"--port", p(portBilling)}},
		{"inventory", portInventory, []string{"--port", p(portInventory)}},
		{"orders", portOrders, []string{"--port", p(portOrders), "--inventory-port", p(portInventory),
			"--billing-port", p(portBilling)}},
	}
	var waits []<-chan error
	for _, s := range servers {
		waits = append(waits, r.javaBG(ctx, h, "microservices", s.role, dir, s.opts...))
		if err := r.waitListening(ctx, h, r.cfg.port(s.port)); err != nil {
			return firstErr(append([]error{err}, drain(waits)...)...)
		}
	}
	gerr := r.java(ctx, h, "microservices", "gateway", dir, "--orders-port", p(portOrders),
		"--inventory-port", p(portInventory))
	return firstErr(append(drain(waits), gerr)...)
}

// drain waits for every background role.
func drain(waits []<-chan error) []error {
	errs := make([]error, 0, len(waits))
	for _, w := range waits {
		errs = append(errs, <-w)
	}
	return errs
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
