package servicedb

import (
	"cmp"
	"context"
	"fmt"
	"slices"
)

// Interaction is one who-talks-to-whom edge (PLAN.md M4: "orders →
// inventory"): a client, by service name when it serves one, else by
// app; and an endpoint, by its Label.
type Interaction struct {
	From, To    string
	Conns       int64
	Unresolved  int64
	First, Last uint64 // wall ns
}

// Interactions lists the edges between clients and endpoints, sorted by
// (From, To). A client app is shown as the service it serves when the
// rules name every endpoint it accepted connections on the same — fused
// ones and unresolved ones alike, since its own trace saw it accept
// either way (the listing's server apps); otherwise as its app id (or
// process name), so a guess never merges two clients.
func (d *DB) Interactions(ctx context.Context, f Filter) ([]Interaction, error) {
	eps, err := d.Endpoints(ctx, f)
	if err != nil {
		return nil, err
	}
	labels := map[epKey]string{}
	for _, e := range eps {
		labels[keyOf(e)] = e.Label()
	}
	served := AppServices(eps)
	agg := map[[2]string]*Interaction{}
	err = d.clientRows(ctx, f, func(client string, k epKey, in Interaction) {
		if s := served[client]; s != "" {
			client = s
		}
		key := [2]string{client, labels[k]}
		cur := agg[key]
		if cur == nil {
			in.From, in.To = key[0], key[1]
			agg[key] = &in
			return
		}
		cur.Conns += in.Conns
		cur.Unresolved += in.Unresolved
		cur.First, cur.Last = min(cur.First, in.First), max(cur.Last, in.Last)
	})
	if err != nil {
		return nil, err
	}
	out := make([]Interaction, 0, len(agg))
	for _, in := range agg {
		out = append(out, *in)
	}
	slices.SortFunc(out, func(a, b Interaction) int {
		return cmp.Or(cmp.Compare(a.From, b.From), cmp.Compare(a.To, b.To))
	})
	return out, nil
}

// AppServices maps a server app to the one service its endpoints are
// named; an app serving several names, or an unnamed endpoint, maps to
// none. The view model names conversation lifelines by it too, so a
// sequence diagram and the edges agree.
func AppServices(eps []Endpoint) map[string]string {
	names := map[string]map[string]bool{}
	for _, e := range eps {
		for _, app := range e.ServerApps {
			if names[app] == nil {
				names[app] = map[string]bool{}
			}
			names[app][e.Service] = true
		}
	}
	out := map[string]string{}
	for app, set := range names {
		if len(set) == 1 && !set[""] {
			for s := range set {
				out[app] = s
			}
		}
	}
	return out
}

// clientRows aggregates the connections per (client, endpoint) — the
// endpoint keyed as endpointRows keys it, a local IP's scoping host
// included — and calls fn for each, in order.
func (d *DB) clientRows(ctx context.Context, f Filter, fn func(string, epKey, Interaction)) error {
	rows, err := d.db.QueryContext(ctx, `SELECT
		CASE WHEN client_host = '' THEN '`+Untraced+`'
			ELSE COALESCE(NULLIF(client_app, ''), client_comm, '') END AS client,
		hostname, ip, port, `+endpointHostSQL+`,
		COUNT(*), SUM(state = 'unresolved'), MIN(first_seen), MAX(last_seen)
		FROM conn WHERE role <> 'unknown' AND ip <> '' AND `+projectClause+`
		GROUP BY 1, 2, 3, 4, 5 ORDER BY 1, 2, 3, 4, 5`, f.args()...)
	if err != nil {
		return fmt.Errorf("servicedb: %w", err)
	}
	defer func() { _ = rows.Close() }() // read-only: rows.Err reports failures
	for rows.Next() {
		var client string
		var k epKey
		var port, first, last int64
		var in Interaction
		if err := rows.Scan(&client, &k.hostname, &k.ip, &port, &k.host, &in.Conns, &in.Unresolved,
			&first, &last); err != nil {
			return fmt.Errorf("servicedb: %w", err)
		}
		k.port, in.First, in.Last = uint16(port), uint64(first), uint64(last)
		fn(client, k, in)
	}
	return rows.Err()
}
