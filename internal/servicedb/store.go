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

// upsertRequest folds one request count in. The count is a MAX, not a
// sum: a rebuild re-reads every collected segment, so adding would count
// the same requests again, and a build over fewer segments must not
// shrink what an earlier one saw.
const upsertRequest = `INSERT INTO http_request
	(host, conn_id, method, scheme, http_host, path, requests, first_seen, last_seen)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (host, conn_id, method, scheme, http_host, path) DO UPDATE SET
	requests = MAX(http_request.requests, excluded.requests),
	first_seen = MIN(http_request.first_seen, excluded.first_seen),
	last_seen = MAX(http_request.last_seen, excluded.last_seen)`

// Import folds a snapshot into the database in one transaction: either
// all of it lands or none. Naming rules are never touched. A fused
// connection whose server end had a row of its own (imported while only
// the server's project was built) takes that row over: the server row is
// removed and its window folded into the fused one, so one connection
// never shows as two. An unresolved server half that an earlier import
// already fused is skipped for the same reason.
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

// importOne writes one observation and its requests.
func importOne(ctx context.Context, tx *sql.Tx, o Observation) error {
	if o.State == schema.StateFused {
		if err := supersede(ctx, tx, &o); err != nil {
			return err
		}
	} else if o.Role == RoleServer {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM conn WHERE state = 'fused'
			AND server_host = ? AND server_conn = ? AND NOT (host = ? AND conn_id = ?)`,
			o.Host, o.ConnID, o.Host, o.ConnID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil
		}
	}
	if _, err := tx.ExecContext(ctx, upsertConn, connArgs(o)...); err != nil {
		return err
	}
	for _, r := range o.Requests {
		if _, err := tx.ExecContext(ctx, upsertRequest, o.Host, o.ConnID, r.Method, r.Scheme,
			r.Host, r.Path, int64(r.Count), int64(r.First), int64(r.Last)); err != nil {
			return err
		}
	}
	return nil
}

// supersede removes the row the fused connection's server end had on its
// own, widening o's window by it first so nothing seen is lost.
func supersede(ctx context.Context, tx *sql.Tx, o *Observation) error {
	h, c := o.Server.Host, o.Server.ConnID
	if h == o.Host && c == o.ConnID { // a same-host pair: one row already
		return nil
	}
	var first, last int64
	err := tx.QueryRowContext(ctx, `SELECT first_seen, last_seen FROM conn
		WHERE host = ? AND conn_id = ?`, h, c).Scan(&first, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	o.First, o.Last = min(o.First, uint64(first)), max(o.Last, uint64(last))
	_, err = tx.ExecContext(ctx, `DELETE FROM conn WHERE host = ? AND conn_id = ?`, h, c)
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
		if r.Count == 0 || r.Count > math.MaxInt64 {
			return bad("request %s %s: count %d", r.Method, r.Path, r.Count)
		}
		if err := validWindow(r.First, r.Last); err != nil {
			return bad("request %s %s: %v", r.Method, r.Path, err)
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
