package servicedb

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strconv"
)

// Untraced labels the client of a connection only its server end saw.
const Untraced = "(untraced)"

// Filter narrows a query to projects: a connection belongs to a project
// when either end was traced in it. Project and Projects together name
// the projects; none is every project. Projects is what a build of
// several projects together reads (PLAN.md M4 "Build": a cross-project
// connection is one edge, counted once).
type Filter struct {
	Project  string
	Projects []string
}

// Endpoint is one observed server endpoint (hostname, IP, port) and what
// talked to it, named by the rules.
type Endpoint struct {
	Hostname, IP string
	Port         uint16
	// Host is the traced host a loopback or link-local IP belongs to,
	// "" for any other address. Such an address names a different
	// machine on every host, so it scopes the endpoint: two hosts each
	// running a local service on 127.0.0.1:5432 are two endpoints.
	Host        string
	Service     string // "" when no rule names it
	Conns       int64  // connections seen
	Unresolved  int64  // of which the far end stayed unresolved
	TLS         string // "tls", "plain", "mixed" or "" (unknown)
	First, Last uint64 // wall ns
	Clients     []string
	// ServerApps are the traced apps that accepted these connections:
	// what lets a client be named after the service it serves too.
	ServerApps []string
	Requests   []EndpointRequest
}

// EndpointRequest counts one method and path at an endpoint.
type EndpointRequest struct {
	Method, Path string
	Count        int64
}

// Label is the endpoint's display name: its service, else
// hostname:port, else ip:port; the unnamed forms of a host-scoped
// endpoint (a loopback or link-local IP) end in "@host", so two hosts'
// 127.0.0.1:5432 stay two nodes of the interaction graph, which keys by
// label. A service name is the rules' choice and stands alone.
func (e Endpoint) Label() string {
	if e.Service != "" {
		return e.Service
	}
	at := ""
	if e.Host != "" {
		at = "@" + e.Host
	}
	if e.Hostname != "" {
		return e.Hostname + ":" + strconv.Itoa(int(e.Port)) + at
	}
	if a, err := netip.ParseAddr(e.IP); err == nil {
		return netip.AddrPortFrom(a, e.Port).String() + at
	}
	return e.IP + ":" + strconv.Itoa(int(e.Port)) + at
}

// Endpoints lists the observed endpoints — unnamed ones first, the ones
// a human still has to name (PLAN.md M4), then by hostname, IP and port.
// Connections whose anchor had no role name no endpoint and are left out.
func (d *DB) Endpoints(ctx context.Context, f Filter) ([]Endpoint, error) {
	rules, err := d.Rules(ctx)
	if err != nil {
		return nil, err
	}
	eps, err := d.endpointRows(ctx, f)
	if err != nil {
		return nil, err
	}
	byKey := map[epKey]*Endpoint{}
	for i := range eps {
		eps[i].Service = resolve(rules, eps[i].Hostname, eps[i].IP, eps[i].Port)
		byKey[keyOf(eps[i])] = &eps[i]
	}
	if err := d.endpointLists(ctx, f, byKey); err != nil {
		return nil, err
	}
	if err := d.endpointRequests(ctx, f, byKey); err != nil {
		return nil, err
	}
	slices.SortStableFunc(eps, func(a, b Endpoint) int {
		return cmp.Compare(btoi(a.Service != ""), btoi(b.Service != ""))
	})
	return eps, nil
}

// epKey identifies an endpoint row: hostname, IP and port, plus the
// scoping host of a loopback or link-local IP (EndpointHost).
type epKey struct {
	hostname, ip string
	port         uint16
	host         string
}

func keyOf(e Endpoint) epKey { return epKey{e.Hostname, e.IP, e.Port, e.Host} }

// projectClause selects the rows of a Filter's projects, bound once as ?1
// to Filter.arg's JSON array (json_each: one bind for any number of
// projects).
const projectClause = `(?1 = '[]' OR project IN (SELECT value FROM json_each(?1))
	OR client_project IN (SELECT value FROM json_each(?1))
	OR server_project IN (SELECT value FROM json_each(?1)))`

// arg is the filter's projects as the JSON array projectClause binds,
// "[]" for every project.
func (f Filter) arg() string {
	ps := slices.Clone(f.Projects)
	if f.Project != "" {
		ps = append(ps, f.Project)
	}
	if ps == nil {
		ps = []string{}
	}
	b, _ := json.Marshal(ps) // a []string always marshals
	return string(b)
}

// endpointRows aggregates the connections per endpoint: hostname, IP,
// port and, for a loopback or link-local IP, the anchor host it was seen
// on (endpointHostSQL), so two hosts' local services never fold into one.
func (d *DB) endpointRows(ctx context.Context, f Filter) ([]Endpoint, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT hostname, ip, port, `+endpointHostSQL+`,
		COUNT(*), SUM(state = 'unresolved'), MIN(first_seen), MAX(last_seen), MIN(tls), MAX(tls)
		FROM conn WHERE role <> 'unknown' AND ip <> '' AND `+projectClause+`
		GROUP BY 1, 2, 3, 4 ORDER BY 1, 2, 3, 4`, f.arg())
	if err != nil {
		return nil, fmt.Errorf("servicedb: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only: rows.Err reports failures
	var out []Endpoint
	for rows.Next() {
		var e Endpoint
		var port, first, last int64
		var lo, hi sql.NullInt64
		if err := rows.Scan(&e.Hostname, &e.IP, &port, &e.Host, &e.Conns, &e.Unresolved,
			&first, &last, &lo, &hi); err != nil {
			return nil, fmt.Errorf("servicedb: %w", err)
		}
		e.Port, e.First, e.Last = uint16(port), uint64(first), uint64(last)
		e.TLS = tlsLabel(lo, hi)
		out = append(out, e)
	}
	return out, rows.Err()
}

// tlsLabel summarizes the TLS hints of an endpoint's connections; MIN
// and MAX skip the unknown ones (NULL).
func tlsLabel(lo, hi sql.NullInt64) string {
	switch {
	case !lo.Valid:
		return ""
	case lo.Int64 == 1:
		return "tls"
	case hi.Int64 == 0:
		return "plain"
	}
	return "mixed"
}

// endpointLists fills each endpoint's clients and server apps, sorted.
func (d *DB) endpointLists(ctx context.Context, f Filter, byKey map[epKey]*Endpoint) error {
	rows, err := d.db.QueryContext(ctx, `SELECT DISTINCT hostname, ip, port, `+endpointHostSQL+`,
		CASE WHEN client_host = '' THEN '`+Untraced+`'
			ELSE COALESCE(NULLIF(client_app, ''), client_comm, '') END,
		COALESCE(NULLIF(server_app, ''), '')
		FROM conn WHERE role <> 'unknown' AND ip <> '' AND `+projectClause+`
		ORDER BY 1, 2, 3, 4, 5, 6`, f.arg())
	if err != nil {
		return fmt.Errorf("servicedb: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only: rows.Err reports failures
	for rows.Next() {
		var k epKey
		var port int64
		var client, server string
		if err := rows.Scan(&k.hostname, &k.ip, &port, &k.host, &client, &server); err != nil {
			return fmt.Errorf("servicedb: %w", err)
		}
		k.port = uint16(port)
		if e := byKey[k]; e != nil {
			e.Clients = addSorted(e.Clients, client)
			e.ServerApps = addSorted(e.ServerApps, server)
		}
	}
	return rows.Err()
}

// requestCounts counts each endpoint's requests by method and path. Per
// connection, each view (the client's carries http_host, the server's
// does not) sums over its sources; the two views count the same
// requests, so the connection's count is the larger view — the one whose
// segments a build kept more of — never the two added up. Connections
// then add up per endpoint (keyed as endpointRows keys it, the scoping
// host of a local IP included). A row stored with a raw path, before the
// templating, is folded by the built-in rules first (pathTemplateSQL,
// paths.go), so it counts under its route's template; a stored template
// is taken as it is.
const requestCounts = `WITH per_view AS (
		SELECT host, conn_id, method, ` + pathTemplateSQL + ` AS path, http_host <> '' AS client,
			SUM(requests) AS n
		FROM http_request GROUP BY 1, 2, 3, 4, 5),
	per_conn AS (
		SELECT host, conn_id, method, path, MAX(n) AS n FROM per_view GROUP BY 1, 2, 3, 4)
	SELECT c.hostname, c.ip, c.port, iormesh_endpoint_host(c.ip, c.host), r.method, r.path,
		SUM(r.n)
		FROM per_conn r JOIN conn c ON c.host = r.host AND c.conn_id = r.conn_id
		WHERE c.role <> 'unknown' AND c.ip <> '' AND
			(?1 = '[]' OR c.project IN (SELECT value FROM json_each(?1))
				OR c.client_project IN (SELECT value FROM json_each(?1))
				OR c.server_project IN (SELECT value FROM json_each(?1)))
		GROUP BY 1, 2, 3, 4, 5, 6 ORDER BY 1, 2, 3, 4, 5, 6`

// endpointRequests fills each endpoint's requests by method and path.
func (d *DB) endpointRequests(ctx context.Context, f Filter, byKey map[epKey]*Endpoint) error {
	rows, err := d.db.QueryContext(ctx, requestCounts, f.arg())
	if err != nil {
		return fmt.Errorf("servicedb: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only: rows.Err reports failures
	for rows.Next() {
		var k epKey
		var port int64
		var r EndpointRequest
		if err := rows.Scan(&k.hostname, &k.ip, &port, &k.host, &r.Method, &r.Path,
			&r.Count); err != nil {
			return fmt.Errorf("servicedb: %w", err)
		}
		k.port = uint16(port)
		if e := byKey[k]; e != nil {
			e.Requests = append(e.Requests, r)
		}
	}
	return rows.Err()
}

// addSorted inserts s into a sorted set, skipping "" and duplicates.
func addSorted(set []string, s string) []string {
	i, found := slices.BinarySearch(set, s)
	if s == "" || found {
		return set
	}
	return slices.Insert(set, i, s)
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
