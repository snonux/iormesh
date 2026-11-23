package servicedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"iormesh/internal/schema"
)

// ErrInvalid marks a snapshot that cannot be a merge's output: Import
// rejects the whole snapshot on one, before writing anything.
var ErrInvalid = errors.New("servicedb: invalid observation")

// upsertConn folds one observation into its row. Every merged column is
// idempotent and additive: a fused verdict is never undone, a value the
// new snapshot does not know (empty or NULL) keeps the old one, and the
// window only widens. Importing the same snapshot twice is a no-op.
const upsertConn = `INSERT INTO conn (host, conn_id, role, state, reason, merged_id, project,
	client_host, client_project, client_app, client_comm,
	server_host, server_conn, server_project, server_app, server_comm,
	ip, port, hostname, tls, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host, conn_id) DO UPDATE SET
	role = CASE WHEN excluded.role = 'unknown' THEN conn.role ELSE excluded.role END,
	state = CASE WHEN conn.state = 'fused' THEN 'fused' ELSE excluded.state END,
	reason = CASE WHEN conn.state = 'fused' OR excluded.state = 'fused' THEN ''
		ELSE excluded.reason END,
	merged_id = COALESCE(NULLIF(excluded.merged_id, ''), conn.merged_id),
	project = excluded.project,
	client_host = COALESCE(NULLIF(excluded.client_host, ''), conn.client_host),
	client_project = COALESCE(NULLIF(excluded.client_project, ''), conn.client_project),
	client_app = COALESCE(NULLIF(excluded.client_app, ''), conn.client_app),
	client_comm = COALESCE(NULLIF(excluded.client_comm, ''), conn.client_comm),
	server_host = COALESCE(NULLIF(excluded.server_host, ''), conn.server_host),
	server_conn = COALESCE(NULLIF(excluded.server_conn, ''), conn.server_conn),
	server_project = COALESCE(NULLIF(excluded.server_project, ''), conn.server_project),
	server_app = COALESCE(NULLIF(excluded.server_app, ''), conn.server_app),
	server_comm = COALESCE(NULLIF(excluded.server_comm, ''), conn.server_comm),
	port = CASE WHEN excluded.ip = '' THEN conn.port ELSE excluded.port END,
	ip = COALESCE(NULLIF(excluded.ip, ''), conn.ip),
	hostname = COALESCE(NULLIF(excluded.hostname, ''), conn.hostname),
	tls = COALESCE(excluded.tls, conn.tls),
	first_seen = MIN(conn.first_seen, excluded.first_seen),
	last_seen = MAX(conn.last_seen, excluded.last_seen)`

// upsertRequest folds one source's count in. Counts are kept per source
// (the unit a build reads), so a rebuild over the same sources rewrites
// the same numbers and a build over fewer sources leaves the others'
// counts standing: the reads sum over sources, which stays exact for a
// pooled connection whose requests span several segments, old ones
// deleted since. importOne clears the source's rows on the connection
// first (clearSources, paths.go), so a source read again — grown, or
// under other path templates — replaces its counts; the MAX only decides
// between two rows of one key within a snapshot, and when moveRequests
// merges a server row into a fused one. Every imported path is a
// template (Observe templated it), so the row is marked templated: the
// reads show it as stored (paths.go).
const upsertRequest = `INSERT INTO http_request (host, conn_id, source, method, scheme,
	http_host, path, requests, first_seen, last_seen, templated)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
ON CONFLICT (host, conn_id, source, method, scheme, http_host, path) DO UPDATE SET
	requests = MAX(http_request.requests, excluded.requests),
	first_seen = MIN(http_request.first_seen, excluded.first_seen),
	last_seen = MAX(http_request.last_seen, excluded.last_seen),
	templated = MAX(http_request.templated, excluded.templated)`

// moveRequests re-keys one connection row's requests onto another, by
// upsertRequest's rule, each keeping its templated mark. The SELECT's
// WHERE is also what sqlite needs to parse an upsert on INSERT … SELECT.
const moveRequests = `INSERT INTO http_request (host, conn_id, source, method, scheme,
	http_host, path, requests, first_seen, last_seen, templated)
SELECT ?, ?, source, method, scheme, http_host, path, requests, first_seen, last_seen, templated
	FROM http_request WHERE host = ? AND conn_id = ?
ON CONFLICT (host, conn_id, source, method, scheme, http_host, path) DO UPDATE SET
	requests = MAX(http_request.requests, excluded.requests),
	first_seen = MIN(http_request.first_seen, excluded.first_seen),
	last_seen = MAX(http_request.last_seen, excluded.last_seen),
	templated = MAX(http_request.templated, excluded.templated)`

// upsertSource records that a source saw a connection, and when (task
// gk4): the window only widens, so a source read again — the same, or
// grown — and a request's source written after the connection's own
// span both land idempotently. DB.Forget reads these rows to drop a
// pruned segment's connections.
const upsertSource = `INSERT INTO conn_source (host, conn_id, source, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (host, conn_id, source) DO UPDATE SET
	first_seen = MIN(conn_source.first_seen, excluded.first_seen),
	last_seen = MAX(conn_source.last_seen, excluded.last_seen)`

// moveSources re-keys one connection row's sources onto another, by
// upsertSource's rule (the WHERE as in moveRequests).
const moveSources = `INSERT INTO conn_source (host, conn_id, source, first_seen, last_seen)
SELECT ?, ?, source, first_seen, last_seen FROM conn_source WHERE host = ? AND conn_id = ?
ON CONFLICT (host, conn_id, source) DO UPDATE SET
	first_seen = MIN(conn_source.first_seen, excluded.first_seen),
	last_seen = MAX(conn_source.last_seen, excluded.last_seen)`

// Import folds a snapshot into the database in one transaction: either
// all of it lands or none. Naming rules are never touched. A fused
// connection whose server end had a row of its own (imported while only
// the server's project was built) takes that row over: its window, TLS
// hint and requests move to the fused row and the server row goes, so
// one connection never shows as two. An unresolved server half that an
// earlier import already fused is skipped for the same reason.
func (d *DB) Import(ctx context.Context, s Snapshot) error {
	for i := range s.Conns {
		if err := validate(&s.Conns[i]); err != nil {
			return err
		}
	}
	return d.inTx(ctx, func(tx *sql.Tx) error {
		for i := range s.Conns {
			if err := importOne(ctx, tx, s.Conns[i]); err != nil {
				return fmt.Errorf("servicedb: import %s/%s: %w", s.Conns[i].Host, s.Conns[i].ConnID, err)
			}
		}
		return nil
	})
}

// importOne writes one observation, its sources and its requests, which
// replace the rows their sources had on the connection.
func importOne(ctx context.Context, tx *sql.Tx, o Observation) error {
	if o.State != schema.StateFused && o.Role == RoleServer {
		host, conn, err := fusedElsewhere(ctx, tx, o)
		if err != nil {
			return err
		}
		if host != "" {
			// The fused row holds the connection; it still learns that
			// o's sources name it, so forgetting the client's segments
			// does not drop a connection a kept server segment shows.
			o.Host, o.ConnID, o.Requests = host, conn, nil
			return writeSources(ctx, tx, o)
		}
	}
	server, err := serverRow(ctx, tx, &o)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, upsertConn, connArgs(o)...); err != nil {
		return err
	}
	if server {
		if err := supersede(ctx, tx, o); err != nil {
			return err
		}
	}
	if err := clearSources(ctx, tx, o); err != nil {
		return err
	}
	for _, r := range o.Requests {
		if _, err := tx.ExecContext(ctx, upsertRequest, o.Host, o.ConnID, r.Source, r.Method,
			r.Scheme, r.Host, r.Path, int64(r.Count), int64(r.First), int64(r.Last)); err != nil {
			return err
		}
	}
	return writeSources(ctx, tx, o)
}

// writeSources records o's sources on its row: its own spans and every
// request's source, so a request row never outlives the record of the
// source it came from when that source is forgotten.
func writeSources(ctx context.Context, tx *sql.Tx, o Observation) error {
	put := func(name string, first, last uint64) error {
		_, err := tx.ExecContext(ctx, upsertSource, o.Host, o.ConnID, name, int64(first), int64(last))
		return err
	}
	for _, s := range o.Sources {
		if err := put(s.Name, s.First, s.Last); err != nil {
			return err
		}
	}
	for _, r := range o.Requests {
		if err := put(r.Source, r.First, r.Last); err != nil {
			return err
		}
	}
	return nil
}

// fusedElsewhere finds the row an earlier import fused the connection
// whose server half o is into: that row already holds it. It returns
// that row's key, or "" when there is none.
func fusedElsewhere(ctx context.Context, tx *sql.Tx, o Observation) (host, conn string, err error) {
	err = tx.QueryRowContext(ctx, `SELECT host, conn_id FROM conn WHERE state = 'fused'
		AND server_host = ? AND server_conn = ? AND NOT (host = ? AND conn_id = ?)
		ORDER BY host, conn_id LIMIT 1`,
		o.Host, o.ConnID, o.Host, o.ConnID).Scan(&host, &conn)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	return host, conn, err
}

// serverRow looks for the row a fused o's server end had on its own and
// folds its window and TLS hint into o, so the takeover loses neither.
// It reports whether there is such a row to supersede.
func serverRow(ctx context.Context, tx *sql.Tx, o *Observation) (bool, error) {
	h, c := o.Server.Host, o.Server.ConnID
	if o.State != schema.StateFused || (h == o.Host && c == o.ConnID) { // same-host: one row
		return false, nil
	}
	var first, last int64
	var tls sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT first_seen, last_seen, tls FROM conn
		WHERE host = ? AND conn_id = ?`, h, c).Scan(&first, &last, &tls)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	o.First, o.Last = min(o.First, uint64(first)), max(o.Last, uint64(last))
	if o.TLS < 0 && tls.Valid {
		o.TLS = int(tls.Int64)
	}
	return true, nil
}

// supersede moves the server row's requests and sources onto o's row,
// then removes the server row (their old keys go with it, ON DELETE
// CASCADE).
func supersede(ctx context.Context, tx *sql.Tx, o Observation) error {
	h, c := o.Server.Host, o.Server.ConnID
	for _, q := range []string{moveRequests, moveSources} {
		if _, err := tx.ExecContext(ctx, q, o.Host, o.ConnID, h, c); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM conn WHERE host = ? AND conn_id = ?`, h, c)
	return err
}

// connArgs are upsertConn's parameters for o.
func connArgs(o Observation) []any {
	var ip string
	var port int64
	if o.Endpoint.IsValid() {
		ip, port = o.Endpoint.Addr().String(), int64(o.Endpoint.Port())
	}
	var tls any // NULL: no request said
	if o.TLS >= 0 {
		tls = int64(o.TLS)
	}
	return []any{o.Host, o.ConnID, o.Role, o.State, o.Reason, o.MergedID, o.Project,
		o.Client.Host, o.Client.Project, o.Client.App, o.Client.Comm,
		o.Server.Host, o.Server.ConnID, o.Server.Project, o.Server.App, o.Server.Comm,
		ip, port, o.Hostname, tls, int64(o.First), int64(o.Last)}
}

// validate rejects an observation no merge produces: a database built
// from corrupt input would mislead every later read, and its rows would
// never go away (the database is additive).
func validate(o *Observation) error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s/%s: %s", ErrInvalid, o.Host, o.ConnID, fmt.Sprintf(format, args...))
	}
	switch {
	case o.Host == "" || o.ConnID == "":
		return bad("no anchor host or conn id")
	case o.Project == "":
		return bad("no project")
	case o.Role != RoleClient && o.Role != RoleServer && o.Role != RoleUnknown:
		return bad("role %q", o.Role)
	case o.State == schema.StateFused && (o.MergedID == "" || o.Client.Host == "" || o.Server.Host == ""):
		return bad("fused without a merged id or both ends")
	case o.State == schema.StateUnresolved && o.Reason == "":
		return bad("unresolved without a reason")
	case o.State != schema.StateFused && o.State != schema.StateUnresolved:
		return bad("state %q", o.State)
	case o.TLS < -1 || o.TLS > 1:
		return bad("tls hint %d", o.TLS)
	}
	if err := validWindow(o.First, o.Last); err != nil {
		return bad("%v", err)
	}
	for _, r := range o.Requests {
		if r.Source == "" {
			return bad("request %s %s: no source", r.Method, r.Path)
		}
		if r.Count == 0 || r.Count > math.MaxInt64 {
			return bad("request %s %s: count %d", r.Method, r.Path, r.Count)
		}
		if err := validWindow(r.First, r.Last); err != nil {
			return bad("request %s %s: %v", r.Method, r.Path, err)
		}
	}
	for _, s := range o.Sources {
		if s.Name == "" {
			return bad("a source without a name")
		}
		if err := validWindow(s.First, s.Last); err != nil {
			return bad("source %s: %v", s.Name, err)
		}
	}
	return nil
}

// validWindow requires first <= last, both within sqlite's int64.
func validWindow(first, last uint64) error {
	if first > last || last > math.MaxInt64 {
		return fmt.Errorf("window [%d, %d]", first, last)
	}
	return nil
}
