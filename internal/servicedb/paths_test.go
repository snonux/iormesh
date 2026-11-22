package servicedb

import (
	"context"
	"testing"

	"iormesh/internal/pathtmpl"
)

// withPaths is the mesh fixture with a1's two client requests and b1's
// two server requests on /items/<n> instead of /items.
func withPaths(t *testing.T, rules *pathtmpl.Rules) Snapshot {
	t.Helper()
	sources, r := mesh()
	a, b := sources[0].(memSource), sources[1].(memSource)
	a.events[2].HTTP.Path, a.events[3].HTTP.Path = "/items/1", "/items/2"
	b.events[1].HTTP.Path, b.events[2].HTTP.Path = "/items/3", "/items/4?x=***"
	s, err := Observe(context.Background(), sources, r, rules)
	if err != nil {
		t.Fatal(err)
	}
	return pick(s, "a1")
}

// TestObserveFoldsPaths: the database's path key is the template — ids
// fold to {id}, an operator rule wins over that — so the per-path rows
// stay one per route, not one per id.
func TestObserveFoldsPaths(t *testing.T) {
	for _, c := range []struct {
		rules []string
		want  []string // per request row: path
	}{
		{nil, []string{"/items/{id}", "/items/{id}", "/items/{id}?x=***"}},
		{[]string{"/items/{item}"}, []string{"/items/{item}", "/items/{item}", "/items/{item}?x=***"}},
	} {
		rules, err := pathtmpl.Compile(c.rules)
		if err != nil {
			t.Fatal(err)
		}
		reqs := withPaths(t, rules).Conns[0].Requests
		var got []string
		for _, r := range reqs {
			got = append(got, r.Path)
		}
		if len(got) != len(c.want) {
			t.Fatalf("rules %v: paths %q, want %q", c.rules, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("rules %v: paths %q, want %q", c.rules, got, c.want)
				break
			}
		}
		if reqs[0].Count != 2 {
			t.Errorf("rules %v: client view counts %d, want both requests in one row", c.rules, reqs[0].Count)
		}
	}
}

// pathCount is the endpoint listing's count of GET path at
// inventory.svc:8443, and whether any other path is listed there.
func pathCount(t *testing.T, d *DB, path string) (n int64, others []string) {
	t.Helper()
	eps, err := d.Endpoints(context.Background(), Filter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range eps {
		if e.Hostname != "inventory.svc" {
			continue
		}
		for _, r := range e.Requests {
			if r.Path == path {
				n += r.Count
			} else {
				others = append(others, r.Path)
			}
		}
	}
	return n, others
}

// rawReq is n GET requests of one source on path, the client's view.
func rawReq(source, path string, n uint64) Request {
	r := req(source, "inventory.svc", n)
	r.Path = path
	return r
}

// TestImportReplacesSourceRows: a source imported again under other path
// keys (raw before the templating, or under an edited operator rule)
// replaces its rows on that connection rather than adding to them, so
// the same requests never count twice; other sources' rows stand.
func TestImportReplacesSourceRows(t *testing.T) {
	d, _ := openTemp(t)
	importSnap(t, d, a1With(t, rawReq("seg1", "/items/1", 1), rawReq("seg1", "/items/2", 1),
		rawReq("seg0", "/items/9", 4)))
	importSnap(t, d, a1With(t, rawReq("seg1", "/items/{id}", 2)))
	if n, others := pathCount(t, d, "/items/{id}"); n != 6 || len(others) != 0 {
		t.Errorf("seg1 re-keyed: %d requests on /items/{id} (others %q), want seg1's 2 + seg0's 4", n, others)
	}
	importSnap(t, d, a1With(t, rawReq("seg1", "/items/{item}", 2)))
	if n, _ := pathCount(t, d, "/items/{item}"); n != 2 {
		t.Errorf("seg1 under a new rule: %d requests on /items/{item}, want 2", n)
	}
	if n, _ := pathCount(t, d, "/items/{id}"); n != 4 {
		t.Errorf("seg1 under a new rule: %d left on /items/{id}, want seg0's 4 only", n)
	}
}

// TestLegacyRowsFoldOnRead: rows a build stored before the templating
// (raw paths, from sources no build reads again) are folded by the
// built-in rules when read, so the listing never shows the raw ids.
func TestLegacyRowsFoldOnRead(t *testing.T) {
	d, _ := openTemp(t)
	importSnap(t, d, a1With(t, rawReq("old", "/items/1", 1), rawReq("old", "/items/2", 2),
		rawReq("old", "/api/v2/users", 1)))
	if n, others := pathCount(t, d, "/items/{id}"); n != 3 || len(others) != 1 || others[0] != "/api/v2/users" {
		t.Errorf("legacy rows: %d on /items/{id}, others %q; want 3 and /api/v2/users verbatim", n, others)
	}
}
