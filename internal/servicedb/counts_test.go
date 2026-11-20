package servicedb

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"iormesh/internal/schema"
)

// a1With is the fixture's fused a1 connection carrying only reqs.
func a1With(t *testing.T, reqs ...Request) Snapshot {
	t.Helper()
	s := pick(observeMesh(t), "a1")
	s.Conns[0].Requests = reqs
	return s
}

// req is n GET /items requests of one source and view (host set: the
// client's view).
func req(source, host string, n uint64) Request {
	return Request{Source: source, Method: "GET", Scheme: "https", Host: host, Path: "/items",
		Count: n, First: wall0 + 20, Last: wall0 + 30}
}

// itemsCount is the endpoint listing's count of GET /items at
// inventory.svc:8443.
func itemsCount(t *testing.T, d *DB) int64 {
	t.Helper()
	eps, err := d.Endpoints(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.Hostname == "inventory.svc" {
			for _, r := range e.Requests {
				if r.Path == "/items" {
					return r.Count
				}
			}
		}
	}
	return 0
}

// TestRequestsSpanSegments: a pooled connection's requests spread over
// two segments add up, and stay added up after a build that no longer
// has the first segment (its count was 2 + 3, not max(2, 3)).
func TestRequestsSpanSegments(t *testing.T) {
	d, _ := openTemp(t)
	importSnap(t, d, a1With(t, req("seg1", "inventory.svc", 2), req("seg2", "inventory.svc", 3)))
	if got := itemsCount(t, d); got != 5 {
		t.Fatalf("both segments: %d requests, want 5", got)
	}
	importSnap(t, d, a1With(t, req("seg2", "inventory.svc", 3))) // seg1 deleted
	importSnap(t, d, a1With(t, req("seg2", "inventory.svc", 3), req("seg3", "inventory.svc", 1)))
	if got := itemsCount(t, d); got != 6 {
		t.Errorf("after seg1 went and seg3 came: %d requests, want 6", got)
	}
}

// TestRequestsViewsNotSummed: the client's and the server's view of the
// same requests, seen by different builds, count once — the larger view
// wins, never their sum.
func TestRequestsViewsNotSummed(t *testing.T) {
	d, _ := openTemp(t)
	importSnap(t, d, a1With(t, req("hA-seg1", "inventory.svc", 2)))
	importSnap(t, d, a1With(t, req("hB-seg1", "", 2)))
	if got := itemsCount(t, d); got != 2 {
		t.Fatalf("client view then server view of 2 requests: %d, want 2", got)
	}
	importSnap(t, d, a1With(t, req("hB-seg2", "", 3)))
	if got := itemsCount(t, d); got != 5 {
		t.Errorf("server view grew to 5 while the client's stayed at 2: %d, want 5", got)
	}
}

// TestMigrateV1 opens a version-1 database: its request rows survive
// the move to per-source counts, under source ”.
func TestMigrateV1(t *testing.T) {
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
	for _, q := range []string{migrations[0], "PRAGMA user_version = 1",
		`INSERT INTO conn (host, conn_id, role, state, project, first_seen, last_seen)
			VALUES ('h', 'c', 'client', 'unresolved', 'p', 1, 2)`,
		`INSERT INTO http_request VALUES ('h', 'c', 'GET', 'http', 'x', '/', 4, 1, 2)`} {
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
	var v, n int
	var source string
	if err := d.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if err := d.db.QueryRow("SELECT source, requests FROM http_request").Scan(&source, &n); err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) || source != "" || n != 4 {
		t.Errorf("version %d, row (%q, %d): want %d and ('', 4)", v, source, n, len(migrations))
	}
}

// TestOpenExistingCreatesNothing (negative): a reading open of a missing
// database fails with ErrNoDatabase and leaves no file or directory.
func TestOpenExistingCreatesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "iormesh")
	_, err := OpenExisting(context.Background(), filepath.Join(dir, FileName))
	if !errors.Is(err, ErrNoDatabase) {
		t.Fatalf("err %v, want ErrNoDatabase", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("directory created: %v", err)
	}
}

// TestOpenURIPathIsAFile: a "file:" or ":memory:" path is made absolute
// first, so the driver opens a file of that name, never a URI or an
// in-memory database.
func TestOpenURIPathIsAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	for _, name := range []string{"file:x.db", ":memory:"} {
		d, err := Open(context.Background(), name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		_ = d.Close()
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("%s: no such file in the working directory: %v", name, err)
		}
	}
}

// TestTakeOverMovesRequests: the server row's requests and TLS hint move
// to the fused row instead of going with the server row.
func TestTakeOverMovesRequests(t *testing.T) {
	d, _ := openTemp(t)
	fused := pick(observeMesh(t), "a1").Conns[0]
	serverOnly := Observation{Host: "hB", ConnID: "b1", Role: RoleServer,
		State: schema.StateUnresolved, Reason: "no_match", Project: "inv", Server: fused.Server,
		Endpoint: fused.Endpoint, TLS: 1, First: wall0 + 1, Last: wall0 + 2,
		Requests: []Request{req("hB-old", "", 7)}}
	importSnap(t, d, Snapshot{Conns: []Observation{serverOnly}})
	fused.Requests, fused.TLS = nil, -1
	importSnap(t, d, Snapshot{Conns: []Observation{fused}})
	var n, tls int
	if err := d.db.QueryRow(`SELECT r.requests, c.tls FROM http_request r JOIN conn c
		USING (host, conn_id) WHERE c.conn_id = 'a1'`).Scan(&n, &tls); err != nil {
		t.Fatal(err)
	}
	if n != 7 || tls != 1 {
		t.Errorf("fused row has %d requests, tls %d: want the server row's 7 and 1", n, tls)
	}
}
