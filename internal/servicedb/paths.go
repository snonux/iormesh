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
//   - the reads fold, by the built-in rules, the rows stored raw before
//     the templating (templated = 0, schema version 3), from sources no
//     build reads again, so they show as templates too. A row stored as
//     a template is shown as stored: folding it again would turn an
//     operator's literal digits (/status/404, /api/2024/{x}) into {id}.
//     The operator's rules are not applied at read time: they live in
//     the route map, not the database, and rows of an old rule keep the
//     template they got.

// pathTemplateSQL is a request row's path as the reads show it: a
// template as stored, a raw path of an older build folded by
// pathtmpl.Builtin.
const pathTemplateSQL = "CASE WHEN templated = 1 THEN path ELSE iormesh_path_template(path) END"

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
