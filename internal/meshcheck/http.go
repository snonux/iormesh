package meshcheck

import (
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// RequestsPaired: the client's and the server's http events pair one to
// one by (conn_id, req_seq) with the same method, path and status — the
// cross-wire pairing the merge relies on (PLAN.md "Request pairing across
// the wire"). Every http test of integrationtests checks it too.
func RequestsPaired(t T, events []schema.Event) {
	t.Helper()
	type key struct {
		conn string
		seq  uint32
	}
	ends := map[key][]*schema.HTTP{}
	for _, e := range events {
		if e.Event == schema.KindHTTP && e.HTTP.ReqSeq > 0 {
			k := key{e.ConnID, e.HTTP.ReqSeq}
			ends[k] = append(ends[k], e.HTTP)
		}
	}
	if len(ends) == 0 {
		t.Fatalf("no numbered http event")
	}
	for k, hs := range ends {
		if len(hs) != 2 || hs[0].Method != hs[1].Method || hs[0].Path != hs[1].Path ||
			hs[0].Status != hs[1].Status || !sizesAgree(hs[0].ReqBytes, hs[1].ReqBytes) ||
			!sizesAgree(hs[0].RespBytes, hs[1].RespBytes) {
			t.Errorf("request %+v: ends %+v, want a client and a server that agree", k, hs)
		}
	}
}

// sizesAgree: two ends' body sizes of one request are equal where both
// knew them (task wd4); an end that did not know (absent) agrees with any.
func sizesAgree(a, b *uint64) bool {
	return a == nil || b == nil || *a == *b
}

// HTTPTimed: every http event of the JDK's synchronous libraries spans
// its request (task wd4) — the client's send or response read and the
// server's dispatch are entry-stamped on the thread that records the
// event, so each has a start and a latency. With asyncClient the client
// events are sendAsync's, which have none (their stamp follows the app's
// callbacks), and are skipped.
func HTTPTimed(t T, events []schema.Event, asyncClient bool) {
	t.Helper()
	for _, e := range events {
		skip := asyncClient && e.HTTP != nil && e.HTTP.Host != ""
		if e.Event == schema.KindHTTP && e.LatencyNS == 0 && !skip {
			t.Errorf("http event seq %d (%s %s) has no latency", e.Seq, e.HTTP.Method, e.HTTP.Path)
		}
	}
}

// HTTPOf replaces declared event i's http object with a copy and returns
// it: a mutation's expectation copies the events, not what they point
// to.
func HTTPOf(x *normalize.Expectation, i int) *normalize.ExpectedHTTP {
	h := *x.Events[i].HTTP
	x.Events[i].HTTP = &h
	return &h
}

// ClientHTTPIndex is the index of the declared client-side http event
// for path: the one that names the host it dialed. A golden without one
// is a broken golden, so it panics.
func ClientHTTPIndex(x *normalize.Expectation, path string) int {
	for i, e := range x.Events {
		if e.HTTP != nil && e.HTTP.Path == path && e.HTTP.Host != "" {
			return i
		}
	}
	panic("no declared client http event for " + path)
}
