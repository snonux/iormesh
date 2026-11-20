//go:build mage

// WorkloadTest checks for M4's microservices scenario (task jb4): the
// four services run as four processes the way a harness runs them —
// leaves first, each released only after the one before announced its
// port — and each role's malformed and failing invocations exit with the
// right code, a broken hop included. Also the cross-host pairs
// tcp-two-host and tcp-nat (task kb4): both roles with explicit
// addresses on loopback, and their usage errors and run failures — the
// second host and the NAT hop are the integration tests' rig.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// msServerOrder is the order the microservices servers start in: each
// only after every service it calls listens. The gateway comes last and
// drives the run.
var msServerOrder = []string{"billing", "inventory", "orders"}

// msCallees names the services each role calls, i.e. the --<svc>-port
// options it needs (Service.hops in the workload).
var msCallees = map[string][]string{
	"gateway": {"orders", "inventory"},
	"orders":  {"inventory", "billing"},
}

// m4UsageErrors are malformed microservices invocations (exit 2): a
// service role is required, each role needs exactly its callees' ports,
// connectable ones, and only servers take --port.
var m4UsageErrors = [][]string{
	{"microservices"},
	{"microservices", "--role", "client"},
	{"microservices", "--role", "server"},
	{"microservices", "--role", "gateway"},
	{"microservices", "--role", "gateway", "--orders-port", "1"},
	{"microservices", "--role", "gateway", "--orders-port", "1", "--inventory-port", "0"},
	{"microservices", "--role", "gateway", "--orders-port", "1", "--inventory-port", "1",
		"--port", "1"},
	{"microservices", "--role", "orders", "--inventory-port", "1"},
	{"microservices", "--role", "orders", "--inventory-port", "abc", "--billing-port", "1"},
	{"microservices", "--role", "billing", "--port", "70000"},
	{"microservices", "--role", "billing", "--inventory-port", "1"},
	{"microservices", "--role", "inventory", "--bogus", "x"},
}

// checkM4Scenarios runs the topology, the usage errors and the run-time
// failures.
func checkM4Scenarios() error {
	if err := checkMicroservices(nil); err != nil {
		return err
	}
	if err := checkMicroservicesSpread(); err != nil {
		return err
	}
	for _, args := range m4UsageErrors {
		if err := expectExit(2, args...); err != nil {
			return err
		}
	}
	if err := checkMicroservicesRunFailures(); err != nil {
		return err
	}
	if err := foreignDirSurvivesRun("billing", "microservices", "--role", "billing"); err != nil {
		return err
	}
	if err := checkBrokenHop(); err != nil {
		return err
	}
	return checkTCPHosts()
}

// tcpHostsScenarios are M4's cross-host pairs (task kb4), one Java
// scenario under two names: the network between the hosts is the
// harness's, so on one host both run the same.
var tcpHostsScenarios = []string{"tcp-two-host", "tcp-nat"}

// tcpHostsUsageErrors are malformed invocations of a cross-host pair
// (exit 2, before any I/O): each role needs its own address option and
// only that, and an address is an IPv4 literal — a name would be resolved
// inside the bracket.
var tcpHostsUsageErrors = [][]string{
	{"--role", "solo"},
	{"--role", "server"},
	{"--role", "server", "--host", "127.0.0.1"},
	{"--role", "server", "--bind", "127.0.0.1", "--host", "127.0.0.1"},
	{"--role", "server", "--bind", "127.0.0.1", "--port", "70000"},
	{"--role", "client", "--port", "1"},
	{"--role", "client", "--host", "127.0.0.1"},
	{"--role", "client", "--host", "127.0.0.1", "--port", "0"},
	{"--role", "client", "--bind", "127.0.0.1", "--port", "1"},
	{"--role", "client", "--host", "localhost", "--port", "1"},
	{"--role", "client", "--host", "127.0.0.256", "--port", "1"},
	{"--role", "client", "--host", "127.0.0", "--port", "1"},
	{"--role", "client", "--host", "127.0.0.1.", "--port", "1"},
	{"--role", "client", "--host", "::1", "--port", "1"},
	{"--role", "client", "--host", "+1.0.0.1", "--port", "1"},
}

// checkTCPHosts runs each cross-host pair's two roles on loopback with
// explicit addresses (the server binds --bind, the client dials --host),
// then its usage errors and run-time failures.
func checkTCPHosts() error {
	for _, scenario := range tcpHostsScenarios {
		err := checkTwoRoles(scenario, []string{"--bind", "127.0.0.1"}, func(addr string) []string {
			if opts := portClient(addr); opts != nil {
				return append(opts, "--host", "127.0.0.1")
			}
			return nil
		})
		if err != nil {
			return err
		}
		for _, args := range tcpHostsUsageErrors {
			if err := expectExit(2, append([]string{scenario}, args...)...); err != nil {
				return err
			}
		}
		if err := checkTCPHostsRunFailures(scenario); err != nil {
			return err
		}
	}
	return nil
}

// checkTCPHostsRunFailures: well-formed runs that fail at run time exit
// 1 — a client nobody answers, a server told to bind an address this
// host does not have (192.0.2.1, TEST-NET-1: a rig whose namespace is
// missing fails like this rather than binding elsewhere), and a --dir
// that does not exist.
func checkTCPHostsRunFailures(scenario string) error {
	port, err := closedPort()
	if err != nil {
		return err
	}
	failing := [][]string{
		{scenario, "--role", "client", "--host", "127.0.0.1", "--port", fmt.Sprint(port)},
		{scenario, "--role", "server", "--bind", "192.0.2.1"},
		{scenario, "--role", "server", "--bind", "127.0.0.1", "--dir", "/nonexistent/iormesh-workload"},
	}
	for _, args := range failing {
		if err := expectExit(1, args...); err != nil {
			return err
		}
	}
	return nil
}

// checkMicroservices runs the whole topology in one shared --dir and
// expects every service to finish its bracket and exit 0 — the gateway
// first, then the servers in reverse start order — and the directory to
// be empty afterwards: no service directory, no certificate left. addrs
// gives a service its own address (msArgs); nil runs the loopback
// defaults.
func checkMicroservices(addrs map[string]string) error {
	dir, err := os.MkdirTemp("", "iormesh-microservices-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	ports := map[string]string{}
	var runs []*gatedRun
	defer func() {
		for _, r := range runs {
			r.kill()
		}
	}()
	for _, svc := range msServerOrder {
		r, port, err := startMsServer(svc, dir, ports, addrs)
		if err != nil {
			return err
		}
		runs = append(runs, r)
		ports[svc] = port
	}
	gw, err := startMsGateway(dir, ports, addrs)
	if err != nil {
		return err
	}
	runs = append(runs, gw)
	for i := len(runs) - 1; i >= 0; i-- {
		if err := finish(runs[i], "microservices"); err != nil {
			return err
		}
	}
	return expectEmptyDir(dir, "microservices")
}

// msArgs is the command line of service svc in dir, with the ports of
// the services it calls. A service with an address in addrs binds it
// (--bind) and its callers dial it there (--<svc>-host), as on a host of
// its own; without one both stay at their 127.0.0.1 default.
func msArgs(svc, dir string, ports, addrs map[string]string) []string {
	args := []string{"microservices", "--role", svc, "--dir", dir}
	if a := addrs[svc]; a != "" && svc != "gateway" {
		args = append(args, "--bind", a)
	}
	for _, callee := range msCallees[svc] {
		args = append(args, "--"+callee+"-port", ports[callee])
		if a := addrs[callee]; a != "" {
			args = append(args, "--"+callee+"-host", a)
		}
	}
	return args
}

// startMsServer starts server svc and returns it with the port its
// `listening <port>` line announced.
func startMsServer(svc, dir string, ports, addrs map[string]string) (*gatedRun, string, error) {
	r, err := startRun(nil, msArgs(svc, dir, ports, addrs)...)
	if err != nil {
		return nil, "", err
	}
	if err := r.expectPID(); err != nil {
		r.kill()
		return nil, "", err
	}
	addr, err := r.expectListening()
	if err != nil {
		r.kill()
		return nil, "", err
	}
	if portClient(addr) == nil {
		r.kill()
		return nil, "", fmt.Errorf("%s: bad listening address %q", r.name(), addr)
	}
	r.drainStdout()
	return r, addr, nil
}

// startMsGateway starts the gateway, whose checkout drives the run.
func startMsGateway(dir string, ports, addrs map[string]string) (*gatedRun, error) {
	r, err := startRun(nil, msArgs("gateway", dir, ports, addrs)...)
	if err != nil {
		return nil, err
	}
	if err := r.expectPID(); err != nil {
		r.kill()
		return nil, err
	}
	r.drainStdout()
	return r, nil
}

// expectEmptyDir fails if a run left anything in dir.
func expectEmptyDir(dir, what string) error {
	left, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(left) != 0 {
		return fmt.Errorf("%s left %s behind in its --dir", what, left[0].Name())
	}
	return nil
}

// checkMicroservicesRunFailures: well-formed runs that fail at run time
// exit 1 — a caller whose --dir holds no certificate of its TLS callee
// (it must never fall back to trusting anything else), and a server
// whose --dir does not exist.
func checkMicroservicesRunFailures() error {
	port, err := closedPort()
	if err != nil {
		return err
	}
	empty, err := os.MkdirTemp("", "iormesh-no-cert-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(empty) }()
	p := fmt.Sprint(port)
	// The callee port is closed too, so a refused connection would also
	// exit 1: the stderr names the missing certificate, so the check pins
	// that the cert, read at boot, is what failed (review of jb4).
	noCert := map[string][]string{
		"no orders certificate": {"microservices", "--role", "gateway", "--dir", empty,
			"--orders-port", p, "--inventory-port", p},
		"no billing certificate": {"microservices", "--role", "orders", "--dir", empty,
			"--inventory-port", p, "--billing-port", p},
	}
	for want, args := range noCert {
		if err := expectExitSaying(1, want, args...); err != nil {
			return err
		}
	}
	if err := expectExit(1, "microservices", "--role", "billing",
		"--dir", "/nonexistent/iormesh-workload"); err != nil {
		return err
	}
	return expectEmptyDir(empty, "a failed microservices run")
}

// expectExitSaying is expectExit that also requires want in the jar's
// stderr, so a run failing for another reason does not pass the check.
func expectExitSaying(code int, want string, args ...string) error {
	_, err := runBounded(nil, args...)
	if werr := wantExitCode(err, code, fmt.Sprintf("workload %q", args)); werr != nil {
		return werr
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), want) {
		return fmt.Errorf("workload %q: stderr does not say %q", args, want)
	}
	return nil
}

// checkBrokenHop: a hop that fails in the middle of the chain fails the
// run end to end, never a pass. Orders is pointed at an inventory port
// nobody listens on; its handler's call fails, so it answers the gateway
// 500 and exits 1, and the gateway, refused its order, exits 1 too.
// Billing is never called and is killed.
func checkBrokenHop() error {
	dir, err := os.MkdirTemp("", "iormesh-broken-hop-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	closed, err := closedPort()
	if err != nil {
		return err
	}
	billing, port, err := startMsServer("billing", dir, nil, nil)
	if err != nil {
		return err
	}
	defer billing.kill()
	ports := map[string]string{"billing": port, "inventory": fmt.Sprint(closed)}
	orders, port, err := startMsServer("orders", dir, ports, nil)
	if err != nil {
		return err
	}
	defer orders.kill()
	ports["orders"] = port
	gw, err := startMsGateway(dir, ports, nil)
	if err != nil {
		return err
	}
	defer gw.kill()
	if err := wantExit(gw, 1); err != nil {
		return err
	}
	if err := wantExit(orders, 1); err != nil {
		return err
	}
	// Orders withdrew its certificate even though it failed.
	if _, err := os.Stat(filepath.Join(dir, "orders.crt")); !os.IsNotExist(err) {
		return fmt.Errorf("failed orders left its certificate behind (stat: %v)", err)
	}
	return nil
}

// wantExit waits for r to exit with code within gateWait.
func wantExit(r *gatedRun, code int) error {
	select {
	case err := <-r.done:
		return wantExitCode(err, code, r.name())
	case <-time.After(gateWait):
		return fmt.Errorf("%s: no exit within %v", r.name(), gateWait)
	}
}
