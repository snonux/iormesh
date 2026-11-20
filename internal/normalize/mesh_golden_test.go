package normalize

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// meshScenarios are the multi-host scenarios (task kb4): each has one
// golden per host — what that host's tracer records, its end of the
// connection unresolved — and the merged expect.json, both processes with
// the connection fused. By convention the merged golden's $p1 is the
// client and $p2 the server.
var meshScenarios = []string{"tcp-two-host", "tcp-nat"}

// TestMeshGoldensAgree keeps each multi-host scenario's three goldens in
// step: the merged golden declares exactly each host's events for its
// process, in order (fd and thread symbols aside, which the merged golden
// numbers across both processes), and each host's edges with only the
// connection's state changed from unresolved to fused — the merge adds
// the fusion and nothing else. A host golden edited alone fails here.
func TestMeshGoldensAgree(t *testing.T) {
	for _, sc := range meshScenarios {
		dir := filepath.Join("..", "..", "workload", "scenarios", sc)
		merged := loadGolden(t, filepath.Join(dir, "expect.json"))
		for proc, role := range map[string]string{"$p1": "client", "$p2": "server"} {
			host := loadGolden(t, filepath.Join(dir, "expect-"+role+".json"))
			if host.Scenario != sc || merged.Scenario != sc {
				t.Fatalf("%s: goldens name scenarios %q and %q", sc, host.Scenario, merged.Scenario)
			}
			assertSameEvents(t, sc+" "+role, host.Events, eventsOf(merged.Events, proc))
			assertMergedEdges(t, sc+" "+role, host.Edges, merged.Edges, proc, "$h"+proc[2:])
		}
	}
}

func loadGolden(t *testing.T, path string) Expectation {
	t.Helper()
	x, err := LoadExpectation(path)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// eventsOf is the declared events of process proc.
func eventsOf(events []ExpectedEvent, proc string) []ExpectedEvent {
	var out []ExpectedEvent
	for _, e := range events {
		if e.Process == proc {
			out = append(out, e)
		}
	}
	return out
}

// assertSameEvents compares two declared event lists without their
// process, thread and fd symbols.
func assertSameEvents(t *testing.T, what string, want, got []ExpectedEvent) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("%s: host golden declares %d events, merged %d", what, len(want), len(got))
	}
	for i := range want {
		if a, b := bareEvent(t, want[i]), bareEvent(t, got[i]); a != b {
			t.Errorf("%s: event %d differs:\n host   %s\n merged %s", what, i+1, a, b)
		}
	}
}

// bareEvent is e as JSON without the symbols the merged golden renumbers.
func bareEvent(t *testing.T, e ExpectedEvent) string {
	t.Helper()
	e.Process, e.Thread, e.FD = "", "", nil
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertMergedEdges: every host edge (on host $h1, process $p1) is in
// the merged golden for proc on mergedHost, a connected edge fused there.
func assertMergedEdges(t *testing.T, what string, host, merged []ExpectedEdge, proc,
	mergedHost string) {
	t.Helper()
	if len(host) == 0 {
		t.Fatalf("%s: host golden declares no edges", what)
	}
	for _, e := range host {
		if e.Type == "connected" {
			if e.State != "unresolved" {
				t.Errorf("%s: a host alone declares its connection %q, want unresolved", what, e.State)
			}
			e.State = "fused"
		}
		e.From = proc
		e.To = renameHost(e.To, mergedHost)
		if !containsEdge(merged, e) {
			t.Errorf("%s: merged golden lacks %+v", what, e)
		}
	}
}

// renameHost rewrites the host symbol $h1 of a host golden's node id to
// the merged golden's.
func renameHost(id, host string) string {
	for _, prefix := range []string{"host:", "file:"} {
		if len(id) >= len(prefix)+3 && id[:len(prefix)+3] == prefix+"$h1" {
			return prefix + host + id[len(prefix)+3:]
		}
	}
	return id
}

func containsEdge(edges []ExpectedEdge, e ExpectedEdge) bool {
	for _, x := range edges {
		if x == e {
			return true
		}
	}
	return false
}

// TestBareEventKeepsRefs: the sync test's comparison sees the port and
// connection refs — a symbol and a literal, or two different literals,
// never compare equal — and round-trips them as declared.
func TestBareEventKeepsRefs(t *testing.T) {
	x, err := ParseExpectation([]byte(`{"scenario": "s", "schema_version": 1, "events": [
	  {"thread": "$t1", "event": "connect", "args": {"remote_port": "$port1"}, "conn_id": "$c1"},
	  {"thread": "$t1", "event": "connect", "args": {"remote_port": 80}, "conn_id": "$c1"},
	  {"thread": "$t1", "event": "connect", "args": {"remote_port": 880}, "conn_id": "$c1"}],
	  "edges": [], "must_not_appear": []}`))
	if err != nil {
		t.Fatal(err)
	}
	a, b, c := bareEvent(t, x.Events[0]), bareEvent(t, x.Events[1]), bareEvent(t, x.Events[2])
	if a == b || b == c {
		t.Fatalf("refs lost: %s / %s / %s", a, b, c)
	}
	want := `{"thread":"","event":"connect","conn_id":"$c1","args":{"remote_port":80}}`
	if b != want {
		t.Fatalf("got %s, want %s", b, want)
	}
}
