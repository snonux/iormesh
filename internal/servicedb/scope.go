package servicedb

import (
	"database/sql/driver"
	"net/netip"

	"modernc.org/sqlite"
)

// EndpointHost is the host that scopes an endpoint at ip, given the
// traced host (the row's anchor) that saw it: host itself when ip is a
// loopback, link-local or unspecified address — one that names a
// different machine on every host, so two hosts' 127.0.0.1:5432 are two
// services — and "" for any other address, which names the same server
// whoever connects. It is the inverse of internal/expand's routable for
// unicast addresses; an address that does not parse keeps "" (one
// endpoint, as before).
// Exported so the view model keys endpoints as the database does.
func EndpointHost(ip, host string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsUnspecified() {
		return host
	}
	return ""
}

// endpointHostSQL is EndpointHost in SQL, so the endpoint queries group
// by it like by hostname, ip and port. It is computed at read time, not
// stored: no column, no migration, and rows imported before it scope
// the same way.
const endpointHostSQL = "iormesh_endpoint_host(ip, host)"

func init() {
	// Registered with the driver for every connection opened later; a
	// deterministic function, so sqlite may fold repeated calls.
	sqlite.MustRegisterDeterministicScalarFunction("iormesh_endpoint_host", 2,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ip, _ := args[0].(string)
			host, _ := args[1].(string)
			return EndpointHost(ip, host), nil
		})
}
