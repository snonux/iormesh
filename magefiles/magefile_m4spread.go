//go:build mage

// WorkloadTest checks for the microservices scenario's address options
// (task 1f4): one service per host, as M4's multi-host test and the
// acceptance run spread it. On one host the services get loopback
// addresses of their own (127.0.0.0/8 is all local on Linux), so each
// server binds its own address, each TLS certificate names it, and each
// caller dials it there — the same code path a second host takes, minus
// the network, which is the integration tests' rig.
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// msSpreadAddrs gives every server an address of its own; none is
// 127.0.0.1, so a service that ignored its --bind (or a caller its
// --<svc>-host) would fail to connect or to verify a certificate.
var msSpreadAddrs = map[string]string{
	"billing": "127.0.0.2", "inventory": "127.0.0.3", "orders": "127.0.0.4",
}

// msAddrUsageErrors are malformed address options (exit 2, before any
// I/O): IPv4 literals only — a name would be resolved inside the bracket
// — no wildcard (no certificate names it, no caller can dial it), --bind
// only for a server, and a host option only for a service's own callees.
var msAddrUsageErrors = [][]string{
	{"--role", "billing", "--bind", "localhost"},
	{"--role", "billing", "--bind", "0.0.0.0"},
	{"--role", "billing", "--bind", "127.0.0.256"},
	{"--role", "billing", "--bind", "::1"},
	{"--role", "inventory", "--host", "127.0.0.1"},
	{"--role", "billing", "--orders-host", "127.0.0.1"},
	{"--role", "gateway", "--bind", "127.0.0.1", "--orders-port", "1", "--inventory-port", "1"},
	{"--role", "gateway", "--orders-port", "1", "--inventory-port", "1",
		"--orders-host", "orders.internal"},
	{"--role", "gateway", "--orders-port", "1", "--inventory-port", "1",
		"--billing-host", "127.0.0.1"},
	{"--role", "orders", "--inventory-port", "1", "--billing-port", "1",
		"--billing-host", "0.0.0.0"},
}

// checkMicroservicesSpread runs the topology with an address per
// service, then the address options' usage errors and run failures, and
// a certificate pin that does not match its server.
func checkMicroservicesSpread() error {
	if err := checkMicroservices(msSpreadAddrs); err != nil {
		return fmt.Errorf("spread over addresses: %w", err)
	}
	for _, args := range msAddrUsageErrors {
		if err := expectExit(2, append([]string{"microservices"}, args...)...); err != nil {
			return err
		}
	}
	// An address this host does not have (TEST-NET-1) fails the bind at
	// run time, exit 1: a rig whose namespace is missing fails like this
	// rather than binding somewhere else.
	if err := expectExit(1, "microservices", "--role", "billing", "--bind", "192.0.2.1"); err != nil {
		return err
	}
	return checkPinMismatch()
}

// checkPinMismatch: a caller pins exactly the certificate it finds in
// its --dir. Orders runs in one directory; the gateway runs in another
// whose orders.crt is really billing's certificate (a valid one, for the
// same address, just not the server's). The handshake must fail and the
// gateway exit 1 — never fall back to trusting what the server offers.
// Orders never sees a request and is killed with billing. Both
// certificates name 127.0.0.1, so this tests trust (the pin), not the
// SAN: a SAN mismatch needs a server reachable at an address its
// certificate does not name, which a specific --bind rules out.
func checkPinMismatch() error {
	served, err := os.MkdirTemp("", "iormesh-pin-served-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(served) }()
	pinned, err := os.MkdirTemp("", "iormesh-pin-caller-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(pinned) }()
	ports, stop, err := startOrdersAndBilling(served)
	if err != nil {
		return err
	}
	defer stop()
	crt, err := os.ReadFile(filepath.Join(served, "billing.crt"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(pinned, "orders.crt"), crt, 0o644); err != nil {
		return err
	}
	return expectExitSaying(1, "PKIX", msArgs("gateway", pinned, ports, nil)...)
}

// startOrdersAndBilling starts billing and orders in dir (inventory's
// port closed: no call gets that far) and returns their ports and a
// function that kills both.
func startOrdersAndBilling(dir string) (map[string]string, func(), error) {
	closed, err := closedPort()
	if err != nil {
		return nil, nil, err
	}
	ports := map[string]string{"inventory": fmt.Sprint(closed)}
	billing, port, err := startMsServer("billing", dir, ports, nil)
	if err != nil {
		return nil, nil, err
	}
	ports["billing"] = port
	orders, port, err := startMsServer("orders", dir, ports, nil)
	if err != nil {
		billing.kill()
		return nil, nil, err
	}
	ports["orders"] = port
	return ports, func() { orders.kill(); billing.kill() }, nil
}
