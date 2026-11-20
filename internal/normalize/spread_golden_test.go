package normalize

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// TestSpreadGoldenAgrees keeps the microservices spread's merged golden
// (expect-spread.json, task 1f4) in step with the loopback expect.json:
// the same events in the same order, except that the spread's accepts
// and connects name their address and literal port and its client http
// events the address they dialed; and the same edges with each process
// on its own host, plus one runs_on edge per process. An event or edge
// changed in one golden alone fails here.
func TestSpreadGoldenAgrees(t *testing.T) {
	dir := filepath.Join("..", "..", "workload", "scenarios", "microservices")
	loop := loadGolden(t, filepath.Join(dir, "expect.json"))
	spread := loadGolden(t, filepath.Join(dir, "expect-spread.json"))
	if len(loop.Events) != len(spread.Events) {
		t.Fatalf("expect.json declares %d events, expect-spread.json %d", len(loop.Events),
			len(spread.Events))
	}
	for i := range loop.Events {
		a, b := placeless(t, loop.Events[i]), placeless(t, spread.Events[i])
		if a != b {
			t.Errorf("event %d differs beyond its address:\n loopback %s\n spread   %s", i+1, a, b)
		}
		if e := spread.Events[i]; e.HTTP != nil && e.HTTP.Host == "127.0.0.1" {
			t.Errorf("event %d: the spread dials loopback", i+1)
		}
	}
	assertSpreadEdges(t, loop.Edges, spread.Edges)
}

// assertSpreadEdges: the spread's edges other than runs_on are the
// loopback's with every file node moved to its process's host, and each
// of the four processes runs_on a host.
func assertSpreadEdges(t *testing.T, loop, spread []ExpectedEdge) {
	t.Helper()
	var placed []ExpectedEdge
	for _, e := range spread {
		if e.Type != "runs_on" {
			e.To = renameHostAny(e.To)
			placed = append(placed, e)
		}
	}
	if len(placed) != len(loop) || len(spread)-len(placed) != 4 {
		t.Errorf("spread declares %d edges and %d runs_on, want expect.json's %d and 4",
			len(placed), len(spread)-len(placed), len(loop))
	}
	for _, e := range loop {
		if !containsEdge(placed, e) {
			t.Errorf("expect-spread.json lacks %+v", e)
		}
	}
}

// placeless is e as JSON without what the spread changes: a socket
// event's address args and an http event's dialed host.
func placeless(t *testing.T, e ExpectedEvent) string {
	t.Helper()
	if e.Event == "accept" || e.Event == "connect" {
		e.Args = ExpectedArgs{}
	}
	if e.HTTP != nil {
		h := *e.HTTP
		h.Host = ""
		e.HTTP = &h
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// renameHostAny rewrites any host symbol of a file node to $h1, the one
// host of the loopback golden.
func renameHostAny(id string) string {
	if rest, ok := strings.CutPrefix(id, "file:$h"); ok {
		if _, path, ok := strings.Cut(rest, ":"); ok {
			return "file:$h1:" + path
		}
	}
	return id
}
