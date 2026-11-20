package servicedb

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

// dump renders both data tables row by row, so two states compare as
// text.
func dump(t *testing.T, d *DB) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT host, conn_id, role, state, reason, merged_id, project, client_host, client_app,
			server_host, server_conn, server_app, ip, port, hostname, COALESCE(tls, -1),
			first_seen, last_seen FROM conn ORDER BY host, conn_id`,
		`SELECT host, conn_id, method, scheme, http_host, path, requests, first_seen, last_seen
			FROM http_request ORDER BY 1, 2, 3, 4, 5, 6`,
	} {
		rows, err := d.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			_, _ = fmt.Fprintln(&b, vals...)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		_ = rows.Close()
	}
	return b.String()
}

// importSnap imports s or fails the test.
func importSnap(t *testing.T, d *DB, s Snapshot) {
	t.Helper()
	if err := d.Import(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

// pick returns the snapshot's observations anchored at the given conns.
func pick(s Snapshot, conns ...string) Snapshot {
	var out Snapshot
	for _, o := range s.Conns {
		if slices.Contains(conns, o.ConnID) {
			out.Conns = append(out.Conns, o)
		}
	}
	return out
}

// TestImportIdempotent: rebuilding the same mesh updates rows instead of
// duplicating them (PLAN.md M4) — the second import changes no byte.
func TestImportIdempotent(t *testing.T) {
	d, _ := openTemp(t)
	s := observeMesh(t)
	importSnap(t, d, s)
	once := dump(t, d)
	importSnap(t, d, s)
	if twice := dump(t, d); twice != once {
		t.Errorf("re-import changed the database:\n%s\nvs\n%s", once, twice)
	}
	if n := strings.Count(once, "\n"); n != len(s.Conns)+2 { // two request rows
		t.Errorf("%d rows, want %d:\n%s", n, len(s.Conns)+2, once)
	}
}

// TestImportAdditive: a build over fewer segments forgets nothing, and a
// later one widens the window and raises counts without summing them.
func TestImportAdditive(t *testing.T) {
	d, _ := openTemp(t)
	s := observeMesh(t)
	importSnap(t, d, s)
	full := dump(t, d)
	importSnap(t, d, pick(s, "a2")) // the operator deleted older segments
	if got := dump(t, d); got != full {
		t.Errorf("a partial rebuild changed rows:\n%s\nvs\n%s", full, got)
	}
	later := pick(s, "a1")
	later.Conns[0].Last += 1000
	later.Conns[0].Requests[0].Count = 3
	importSnap(t, d, later)
	var last, reqs int64
	if err := d.db.QueryRow(`SELECT c.last_seen, r.requests FROM conn c JOIN http_request r
		USING (host, conn_id) WHERE c.conn_id = 'a1'`).Scan(&last, &reqs); err != nil {
		t.Fatal(err)
	}
	if uint64(last) != s.Conns[0].Last+1000 || reqs != 3 {
		t.Errorf("last %d requests %d, want %d and 3", last, reqs, s.Conns[0].Last+1000)
	}
}

// TestImportUnresolvedKept: unresolved halves stay unresolved with their
// reason, and a fused verdict is never undone by a later build that saw
// only one end.
func TestImportUnresolvedKept(t *testing.T) {
	d, _ := openTemp(t)
	s := observeMesh(t)
	importSnap(t, d, s)
	for conn, want := range map[string]string{"a2": "unresolved no_match", "b2": "unresolved no_match",
		"b3": "unresolved no_role", "a1": "fused ", "lo": "fused "} {
		var state, reason string
		if err := d.db.QueryRow(`SELECT state, reason FROM conn WHERE conn_id = ?`, conn).
			Scan(&state, &reason); err != nil {
			t.Fatalf("%s: %v", conn, err)
		}
		if got := state + " " + reason; got != want {
			t.Errorf("%s: %q, want %q", conn, got, want)
		}
	}
	// Project shop rebuilt alone: its end of a1 is unresolved now.
	alone := pick(s, "a1")
	alone.Conns[0].State, alone.Conns[0].Reason, alone.Conns[0].Server = schema.StateUnresolved, "no_match", End{}
	alone.Conns[0].MergedID = ""
	before := dump(t, d)
	importSnap(t, d, alone)
	if after := dump(t, d); after != before {
		t.Errorf("an unresolved rebuild undid a fused row:\n%s\nvs\n%s", before, after)
	}
}

// TestImportFusedTakesOverServerRow: a connection first seen from its
// server end alone (only inv built) becomes one fused row once both
// projects are built together, and the server-only build no longer
// adds it back.
func TestImportFusedTakesOverServerRow(t *testing.T) {
	d, _ := openTemp(t)
	s := observeMesh(t)
	fused := pick(s, "a1").Conns[0]
	serverOnly := Observation{Host: "hB", ConnID: "b1", Role: RoleServer,
		State: schema.StateUnresolved, Reason: "no_match", Project: "inv", Server: fused.Server,
		Endpoint: fused.Endpoint, TLS: -1, First: wall0 + 1, Last: wall0 + 2,
		Requests: []Request{{Method: "GET", Scheme: "https", Path: "/items", Count: 2,
			First: wall0 + 1, Last: wall0 + 2}}}
	importSnap(t, d, Snapshot{Conns: []Observation{serverOnly}})
	importSnap(t, d, pick(s, "a1"))
	importSnap(t, d, Snapshot{Conns: []Observation{serverOnly}})
	var n int
	var first int64
	if err := d.db.QueryRow(`SELECT COUNT(*), MIN(first_seen) FROM conn`).Scan(&n, &first); err != nil {
		t.Fatal(err)
	}
	if n != 1 || uint64(first) != wall0+1 {
		t.Errorf("%d rows, first %d: want one fused row with the server row's window", n, first)
	}
	var orphans int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM http_request WHERE conn_id = 'b1'`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d request rows of the superseded server row remain", orphans)
	}
}

// TestImportRejectsInvalid (negative): an observation no merge makes
// fails the whole snapshot, and nothing of it is written — not even the
// valid observation before it.
func TestImportRejectsInvalid(t *testing.T) {
	good := pick(observeMesh(t), "a2").Conns[0]
	for name, mutate := range map[string]func(*Observation){
		"no host":         func(o *Observation) { o.Host = "" },
		"no project":      func(o *Observation) { o.Project = "" },
		"bad role":        func(o *Observation) { o.Role = "peer" },
		"bad state":       func(o *Observation) { o.State = "maybe" },
		"no reason":       func(o *Observation) { o.Reason = "" },
		"fused, one end":  func(o *Observation) { o.State, o.MergedID = schema.StateFused, "m" },
		"window reversed": func(o *Observation) { o.First = o.Last + 1 },
		"window overflow": func(o *Observation) { o.Last = math.MaxUint64 },
		"bad tls":         func(o *Observation) { o.TLS = 2 },
		"zero requests": func(o *Observation) {
			o.Requests = []Request{{Method: "GET", First: o.First, Last: o.Last}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d, _ := openTemp(t)
			bad := good
			bad.ConnID = "zz"
			mutate(&bad)
			err := d.Import(context.Background(), Snapshot{Conns: []Observation{good, bad}})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v, want ErrInvalid", err)
			}
			if got := dump(t, d); got != "" {
				t.Errorf("rejected snapshot wrote rows:\n%s", got)
			}
		})
	}
}
