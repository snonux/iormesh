package servicedb

import (
	"cmp"
	"context"
	"database/sql"
	"fmt"
	"maps"
	"slices"
)

// Forgetting a source (task gk4). The database is additive (doc.go):
// a build over fewer segments keeps what the missing ones counted. That
// is right for a segment a build merely did not read, and wrong for one
// the operator deleted: `iormeshctl prune -apply` calls Forget with the
// segments it is about to delete, so a full build afterwards shows only
// what the store still holds.
//
// What a forgotten source leaves is decided per connection by the
// sources still recorded on it (table conn_source, schema version 4):
//
//   - its request rows go, so request counts drop by what it counted;
//   - a connection no other source names goes, with its requests (ON
//     DELETE CASCADE): no service, edge or count of it remains;
//   - any other connection stays, its window narrowed to the span its
//     remaining sources give. The verdict, ends, hostname and TLS hint
//     it learned stay as they are: they were true of the connection.
//
// Naming rules are human input and never touched. A connection stored
// before version 4 with no request rows has no source recorded and is
// kept; so is anything of the version 1 source '' (no segment name).
// A source forgotten while its file stays is no loss: the next build
// that reads it imports it again.

// Forgotten is what Forget dropped.
type Forgotten struct {
	// Sources counts the given sources the database held anything of.
	Sources int
	// Conns counts the connections dropped, seen only by those sources.
	Conns int
}

// Forget drops what sources (federate.Source names: a segment's
// <project>/<host>/<file>) recorded, in one transaction: either all of
// them are forgotten or none.
func (d *DB) Forget(ctx context.Context, sources []string) (Forgotten, error) {
	var out Forgotten
	err := d.inTx(ctx, func(tx *sql.Tx) error {
		out = Forgotten{}
		touched := map[connKey]bool{}
		for _, s := range slices.Compact(slices.Sorted(slices.Values(sources))) {
			known, err := forgetSource(ctx, tx, s, touched)
			if err != nil {
				return fmt.Errorf("servicedb: forget %s: %w", s, err)
			}
			if known {
				out.Sources++
			}
		}
		keys := slices.SortedFunc(maps.Keys(touched), func(a, b connKey) int {
			return cmp.Or(cmp.Compare(a.host, b.host), cmp.Compare(a.conn, b.conn))
		})
		for _, k := range keys {
			dropped, err := settleConn(ctx, tx, k)
			if err != nil {
				return fmt.Errorf("servicedb: forget: %s/%s: %w", k.host, k.conn, err)
			}
			if dropped {
				out.Conns++
			}
		}
		return nil
	})
	return out, err
}

// connKey is a conn row's key.
type connKey struct{ host, conn string }

// forgetSource deletes source's request and source rows, adding the
// connections it named to touched. It reports whether the database held
// any row of the source.
func forgetSource(ctx context.Context, tx *sql.Tx, source string, touched map[connKey]bool) (bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT host, conn_id FROM conn_source WHERE source = ?`, source)
	if err != nil {
		return false, err
	}
	known := false
	for rows.Next() {
		var k connKey
		if err := rows.Scan(&k.host, &k.conn); err != nil {
			_ = rows.Close() // the scan error is the one to report
			return false, err
		}
		touched[k], known = true, true
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM http_request WHERE source = ?`, source)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	known = known || n > 0
	_, err = tx.ExecContext(ctx, `DELETE FROM conn_source WHERE source = ?`, source)
	return known, err
}

// settleConn drops k's row when no source names it any more, else sets
// its window to its remaining sources' span. It reports a drop.
func settleConn(ctx context.Context, tx *sql.Tx, k connKey) (bool, error) {
	var n int
	var first, last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), MIN(first_seen), MAX(last_seen)
		FROM conn_source WHERE host = ? AND conn_id = ?`, k.host, k.conn).Scan(&n, &first, &last); err != nil {
		return false, err
	}
	if n == 0 {
		_, err := tx.ExecContext(ctx, `DELETE FROM conn WHERE host = ? AND conn_id = ?`, k.host, k.conn)
		return true, err
	}
	_, err := tx.ExecContext(ctx, `UPDATE conn SET first_seen = ?, last_seen = ?
		WHERE host = ? AND conn_id = ?`, first.Int64, last.Int64, k.host, k.conn)
	return false, err
}
