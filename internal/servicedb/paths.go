package servicedb

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"modernc.org/sqlite"

	"iormesh/internal/pathtmpl"
)

// Path templates (task xh4, internal/pathtmpl). Observe stores every
// request under its path's template, so http_request holds one row per
// route. Two things keep the counts exact as the templating changes —
// the upgrade from raw paths, or an operator editing a rule:
//
//   - Import replaces a source's rows on a connection instead of merging
//     them by key (clearSources), so a source read again under other
//     keys never counts its requests twice;
//   - the reads fold the stored path by the built-in rules once more
//     (pathTemplateSQL), so rows stored raw before the templating, from
//     sources no build reads again, show as templates too. Folding is
//     idempotent, so a stored template is unchanged by it. The operator's
//     rules are not applied there: they live in the route map, not the
//     database, and rows of an old rule keep the template they got.

// pathTemplateSQL is pathtmpl.Builtin in SQL, computed at read time: no
// column, no migration.
const pathTemplateSQL = "iormesh_path_template(path)"

func init() {
	// Registered with the driver for every connection opened later; a
	// deterministic function, so sqlite may fold repeated calls.
	sqlite.MustRegisterDeterministicScalarFunction("iormesh_path_template", 1,
		func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			p, _ := args[0].(string)
			return pathtmpl.Builtin(p), nil
		})
}

// clearSources deletes the request rows o's sources have on o's
// connection, before o's requests are written: a snapshot counts every
// request a source holds (sources are read whole), so its rows replace
// the source's earlier ones, whatever path keys those were stored under.
// Sources o does not name keep their rows.
func clearSources(ctx context.Context, tx *sql.Tx, o Observation) error {
	done := map[string]bool{}
	for _, r := range o.Requests {
		if done[r.Source] {
			continue
		}
		done[r.Source] = true
		if _, err := tx.ExecContext(ctx, `DELETE FROM http_request
			WHERE host = ? AND conn_id = ? AND source = ?`, o.Host, o.ConnID, r.Source); err != nil {
			return err
		}
	}
	return nil
}
