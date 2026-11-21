package acceptance

import "fmt"

// remoteDir is the run's own directory in the SSH user's home on each
// server: the workload jar and the scenarios' --dir trees. Nothing of
// the tracer goes there; the runner removes it at the end. Its name
// differs from the controller's work directory (~/.iormesh-m4), so the
// removal can never hit the run's results — and the preflight refuses
// the controller itself as a host anyway.
const remoteDir = ".iormesh-m4-run"

// Port offsets from Config.PortBase. Fixed ports, so a client can be
// given its server's port before the server prints it, and the naming
// rules can name an endpoint by port.
const (
	portLoopback  = 1  // tcp-loopback on HOST1
	portTwoHost   = 2  // tcp-two-host server on HOST2
	portNATInner  = 3  // tcp-nat server on HOST2, behind the operator's DNAT
	portBilling   = 11 // microservices on HOST1
	portInventory = 12
	portOrders    = 13
	// The microservices spread (M4-9, task 1f4): orders and billing on
	// HOST2, inventory on HOST1, each bound to its host's address.
	portSpreadBilling   = 14
	portSpreadInventory = 15
	portSpreadOrders    = 16
	// ports is the highest offset: the run uses PortBase+1..PortBase+ports.
	ports = 16
)

// The microservices runs' --dir names: the loopback run (M4-8) and the
// spread (M4-9). Their projects' patterns end in the --dir, so a session
// of one run never traces the other's process of the same role.
const (
	microDir  = "microservices"
	spreadDir = "microservices-spread"
)

// Project labels. One per scenario role, so cross-project fusion is what
// joins a client to its server (PLAN.md M4 acceptance: "The M0 client and
// server run as two projects").
const (
	projSingle    = "m4single"  // tcp-loopback, both roles: the single-host scenario
	projStream    = "m4stream"  // file-basic, rerun per streaming round
	projShop      = "m4shop"    // tcp-two-host client, HOST1
	projBilling   = "m4billing" // tcp-two-host server, HOST2
	projNATClient = "m4natc"    // tcp-nat client, HOST1
	projNATServer = "m4nats"    // tcp-nat server, HOST2
	projGateway   = "m4gw"      // microservices, HOST1, one project per service
	projOrders    = "m4orders"
	projInventory = "m4inv"
	projBill      = "m4bill"
	projSpreadGW  = "m4sgw"   // the microservices spread, M4-9: gateway, HOST1
	projSpreadInv = "m4sinv"  // inventory, HOST1
	projSpreadOrd = "m4sord"  // orders, HOST2
	projSpreadBil = "m4sbill" // billing, HOST2
	projDisk      = "m4disk"  // the watchdog project, HOST2, added last
)

// project is one registry entry the run adds; agent marks the ones traced
// with the Java agent (agent.go).
type project struct {
	host, name, pattern string
	agent               bool
}

// rolePattern is the -pattern of a session tracing one workload role:
// the JVM's command line as the runner starts it, home-independent.
func rolePattern(scenario, role string) string {
	return fmt.Sprintf("java -jar */%s/workload.jar %s --role %s *", remoteDir, scenario, role)
}

// microPattern is rolePattern for one microservices run, told apart by
// its --dir, which the runner always passes last (Runner.java). The
// " * " between --role and --dir needs at least one option there, which
// every role has (a port, or a callee's port); a role started with no
// option at all would not match.
func microPattern(role, dir string) string {
	return fmt.Sprintf("java -jar */%s/workload.jar microservices --role %s * --dir */%s",
		remoteDir, role, dir)
}

// spreadProjects are the spread's four projects, one per service.
func (c Config) spreadProjects() []project {
	return []project{
		{c.Host1, projSpreadGW, microPattern("gateway", spreadDir), true},
		{c.Host1, projSpreadInv, microPattern("inventory", spreadDir), true},
		{c.Host2, projSpreadOrd, microPattern("orders", spreadDir), true},
		{c.Host2, projSpreadBil, microPattern("billing", spreadDir), true},
	}
}

// projects lists the entries added before the deploy, NAT ones only when
// the operator configured the DNAT. The microservices projects (M4-8,
// M4-9) trace with the Java agent: their TLS label comes from its http
// records (task kf4).
func (c Config) projects() []project {
	ps := []project{
		{c.Host1, projSingle, rolePattern("tcp-loopback", "*"), false},
		{c.Host1, projStream, rolePattern("file-basic", "solo"), false},
		{c.Host1, projShop, rolePattern("tcp-two-host", "client"), false},
		{c.Host2, projBilling, rolePattern("tcp-two-host", "server"), false},
		{c.Host1, projGateway, microPattern("gateway", microDir), true},
		{c.Host1, projOrders, microPattern("orders", microDir), true},
		{c.Host1, projInventory, microPattern("inventory", microDir), true},
		{c.Host1, projBill, microPattern("billing", microDir), true},
	}
	ps = append(ps, c.spreadProjects()...)
	if c.NATPort != 0 {
		ps = append(ps, project{c.Host1, projNATClient, rolePattern("tcp-nat", "client"), false},
			project{c.Host2, projNATServer, rolePattern("tcp-nat", "server"), false})
	}
	return ps
}

// port is PortBase plus an offset.
func (c Config) port(off int) int { return c.PortBase + off }

// tracerFlags are the flags every session of the run gets: bounded well
// below the 4h default, so a run that dies half-way leaves nothing
// tracing for long.
var tracerFlags = []string{"-duration", "1h"}
