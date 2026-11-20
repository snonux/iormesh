package acceptance

import "fmt"

// remoteDir is the run's own directory in the SSH user's home on each
// server: the workload jar and the scenarios' --dir trees. Nothing of
// the tracer goes there; the runner removes it at the end.
const remoteDir = ".iormesh-m4"

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
	// ports is the highest offset: the run uses PortBase+1..PortBase+ports.
	ports = 13
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
	projDisk      = "m4disk" // the watchdog project, HOST2, added last
)

// project is one registry entry the run adds.
type project struct {
	host, name, pattern string
}

// rolePattern is the -pattern of a session tracing one workload role:
// the JVM's command line as the runner starts it, home-independent.
func rolePattern(scenario, role string) string {
	return fmt.Sprintf("java -jar */%s/workload.jar %s --role %s *", remoteDir, scenario, role)
}

// projects lists the entries added before the deploy, NAT ones only when
// the operator configured the DNAT.
func (c Config) projects() []project {
	ps := []project{
		{c.Host1, projSingle, rolePattern("tcp-loopback", "*")},
		{c.Host1, projStream, rolePattern("file-basic", "solo")},
		{c.Host1, projShop, rolePattern("tcp-two-host", "client")},
		{c.Host2, projBilling, rolePattern("tcp-two-host", "server")},
		{c.Host1, projGateway, rolePattern("microservices", "gateway")},
		{c.Host1, projOrders, rolePattern("microservices", "orders")},
		{c.Host1, projInventory, rolePattern("microservices", "inventory")},
		{c.Host1, projBill, rolePattern("microservices", "billing")},
	}
	if c.NATPort != 0 {
		ps = append(ps, project{c.Host1, projNATClient, rolePattern("tcp-nat", "client")},
			project{c.Host2, projNATServer, rolePattern("tcp-nat", "server")})
	}
	return ps
}

// port is PortBase plus an offset.
func (c Config) port(off int) int { return c.PortBase + off }

// tracerFlags are the flags every session of the run gets: bounded well
// below the 4h default, so a run that dies half-way leaves nothing
// tracing for long.
var tracerFlags = []string{"-duration", "1h"}
