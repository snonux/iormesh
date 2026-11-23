package servicedb

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"iormesh/internal/schema"
)

// connIDs lists the database's conn rows' ids, sorted.
func connIDs(t *testing.T, d *DB) []string {
	t.Helper()
	rows, err := d.db.Query(`SELECT conn_id FROM conn ORDER BY conn_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// forget runs Forget or fails the test.
func forget(t *testing.T, d *DB, sources ...string) Forgotten {
	t.Helper()
	got, err := d.Forget(context.Background(), sources)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// TestForget (task gk4): forgetting a source drops the connections only
// it saw, its request rows and its share of the others' windows; the
// connections other sources still name stay, and so do the naming
// rules. Forgetting it again, or a source never imported, is a no-op.
func TestForget(t *testing.T) {
	d, _ := openTemp(t)
	ctx := context.Background()
	importSnap(t, d, observeMesh(t)) // sources hA and hB
	if _, err := d.AddRule(ctx, Rule{HostGlob: "inventory*", Service: "inventory"}); err != nil {
		t.Fatal(err)
	}
	if got := forget(t, d, "hB", "hB"); got != (Forgotten{Sources: 1, Conns: 2}) {
		t.Errorf("forget hB: %+v, want 1 source and 2 connections (b2, b3)", got)
	}
	if got, want := connIDs(t, d), []string{"a1", "a2", "lo"}; !slices.Equal(got, want) {
		t.Errorf("connections %v, want %v", got, want)
	}
	// a1 keeps hA's view of its requests (2) and hA's window only.
	if n := itemsCount(t, d); n != 2 {
		t.Errorf("a1's /items count %d, want hA's 2", n)
	}
	var first, last uint64
	var hB int
	if err := d.db.QueryRow(`SELECT first_seen, last_seen, (SELECT COUNT(*) FROM http_request
		WHERE source = 'hB') FROM conn WHERE conn_id = 'a1'`).Scan(&first, &last, &hB); err != nil {
		t.Fatal(err)
	}
	if first != wall0+10 || last != wall0+530 || hB != 0 {
		t.Errorf("a1 window [%d, %d], %d hB request rows: want hA's [%d, %d] and none",
			first-wall0, last-wall0, hB, 10, 530)
	}
	rules, err := d.Rules(ctx)
	if err != nil || len(rules) != 1 {
		t.Errorf("rules after forget: %v, %v; want the one rule", rules, err)
	}
	before := dump(t, d)
	if got := forget(t, d, "hB", "nosuch"); got != (Forgotten{}) {
		t.Errorf("forget again: %+v, want nothing", got)
	}
	if dump(t, d) != before {
		t.Errorf("forgetting an unknown source changed the database")
	}
	// A source forgotten while its file stays comes back with the next
	// build that reads it.
	importSnap(t, d, observeMesh(t))
	if got, want := connIDs(t, d), []string{"a1", "a2", "b2", "b3", "lo"}; !slices.Equal(got, want) {
		t.Errorf("after re-import: %v, want %v", got, want)
	}
}

// TestForgetKeepsServerSegment: a server half imported after its
// connection was fused is not a row of its own, but its source is
// recorded on the fused row — forgetting the segments of the fusing
// build keeps a connection a later server segment still shows.
func TestForgetKeepsServerSegment(t *testing.T) {
	d, _ := openTemp(t)
	fused := pick(observeMesh(t), "a1")
	importSnap(t, d, fused)
	srv := fused.Conns[0].Server
	later := Observation{Host: "hB", ConnID: "b1", Role: RoleServer, State: schema.StateUnresolved,
		Reason: "no_match", Project: "inv", Server: srv, TLS: -1, First: wall0 + 900, Last: wall0 + 950,
		Sources: []SourceSpan{{"hB-later", wall0 + 900, wall0 + 950}}}
	importSnap(t, d, Snapshot{Conns: []Observation{later}})
	if got := forget(t, d, "hA", "hB"); got.Conns != 0 {
		t.Errorf("forget the fusing build's sources: %+v, want no connection dropped", got)
	}
	var first, last uint64
	if err := d.db.QueryRow(`SELECT first_seen, last_seen FROM conn WHERE conn_id = 'a1'`).
		Scan(&first, &last); err != nil {
		t.Fatalf("a1 dropped though hB-later names it: %v", err)
	}
	if first != wall0+900 || last != wall0+950 {
		t.Errorf("a1 window [+%d, +%d], want hB-later's [+900, +950]", first-wall0, last-wall0)
	}
	if got := forget(t, d, "hB-later"); got.Conns != 1 {
		t.Errorf("forget the last source: %+v, want a1 dropped", got)
	}
}

// TestMigrateV3Sources: a version 3 database's connections get their
// request rows' sources (version 4), so forgetting those drops them; a
// connection without requests records no source and stays.
func TestMigrateV3Sources(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "iormesh")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, FileName)
	writeFile(t, path, nil, 0o600)
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range append(slices.Clone(migrations[:3]), "PRAGMA user_version = 3",
		`INSERT INTO conn (host, conn_id, role, state, project, first_seen, last_seen)
			VALUES ('h', 'c', 'client', 'unresolved', 'p', 1, 9), ('h', 'quiet', 'client', 'unresolved', 'p', 1, 2)`,
		`INSERT INTO http_request VALUES ('h', 'c', 'p/h/seg1', 'GET', 'http', 'x', '/', 4, 1, 2, 1),
			('h', 'c', 'p/h/seg2', 'GET', 'http', 'x', '/', 1, 5, 6, 1)`) {
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_ = raw.Close()
	d, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if got := forget(t, d, "p/h/seg1"); got != (Forgotten{Sources: 1}) {
		t.Errorf("forget seg1: %+v, want 1 source, no connection", got)
	}
	var first, last uint64
	if err := d.db.QueryRow(`SELECT first_seen, last_seen FROM conn WHERE conn_id = 'c'`).
		Scan(&first, &last); err != nil || first != 5 || last != 6 {
		t.Errorf("c after forgetting seg1: [%d, %d] %v, want seg2's [5, 6]", first, last, err)
	}
	if got := forget(t, d, "p/h/seg2"); got.Conns != 1 {
		t.Errorf("forget seg2: %+v, want c dropped", got)
	}
	if got := connIDs(t, d); !slices.Equal(got, []string{"quiet"}) {
		t.Errorf("connections %v, want the sourceless one kept", got)
	}
}

// TestImportRejectsUnnamedSource (negative): a source span without a
// name, or with its window reversed, rejects the snapshot.
func TestImportRejectsUnnamedSource(t *testing.T) {
	d, _ := openTemp(t)
	for _, s := range []SourceSpan{{"", wall0, wall0}, {"x", wall0 + 1, wall0}} {
		snap := pick(observeMesh(t), "a2")
		snap.Conns[0].Sources = []SourceSpan{s}
		if err := d.Import(context.Background(), snap); err == nil {
			t.Errorf("source %+v accepted", s)
		}
	}
}
