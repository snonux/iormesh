package federate

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/schema"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden from the current output")

func mustAddrPort(s string) netip.AddrPort { return netip.MustParseAddrPort(s) }

// meshSources writes the golden scenario's traces under dir: S2's rig as
// hosts — client hostA dials the VIP 10.0.1.254:8080, the NAT host
// (untraced: no conntrack needed) DNATs and SNATs it to hostB — plus
// every way a half stays unresolved.
func meshSources(t *testing.T, dir string) []Source {
	t.Helper()
	const vip, srv = "10.0.1.254:8080", "10.0.2.2:8080"
	a := newTrace("hostA", "shop")
	b := newTrace("hostB", "billing") // another project: cross-project edges
	// NAT'ed pair plus TIME_WAIT reuse: one client tuple three times, a
	// fresh SNAT port each time, three keys, three fused edges.
	for i, k := range []ISNPair{{0xfffffff0, 0x1000}, {0x0000fff1, 0x2000}, {0x0001fff2, 0x3000}} {
		at := wall0 + uint64(i)*sec
		a.client(100, fmt.Sprintf("a-reuse-%d", i), "10.0.1.1:45678", vip, k, at)
		b.server(200, fmt.Sprintf("b-reuse-%d", i), srv,
			fmt.Sprintf("10.0.2.254:4000%d", i), k, at)
	}
	// Untraced far end: unresolved, reporting the peer M6 would expand to.
	a.client(100, "a-untraced", "10.0.1.1:40100", "203.0.113.9:443", ISNPair{7, 8}, wall0)
	// A pre-M4 trace's record: no tcp_sock fields, so no key.
	b.event(200, schema.KindTCPState, 11, 0, wall0, "b-nokey",
		schema.Args{SockState: ptr("ESTABLISHED")})
	b.event(200, schema.KindAccept4, 11, 11, wall0, "b-nokey", tuple(srv, "10.0.2.254:40050"))
	b.socket(200, "b-nokey")
	// Two clients rebuild one key in the window: ambiguous, all three
	// halves stay unresolved.
	c := newTrace("hostC", "shop")
	amb := ISNPair{0x5555, 0x6666}
	a.client(101, "a-amb", "10.0.1.1:40200", vip, amb, wall0)
	c.client(300, "c-amb", "10.0.3.3:40200", vip, amb, wall0)
	b.server(200, "b-amb", srv, "10.0.2.254:40060", amb, wall0)
	// hostC's unresolved id equals hostA's: requalified on both hosts.
	c.client(300, "a-untraced", "10.0.3.3:40300", "198.51.100.7:5432", ISNPair{9, 10}, wall0)
	// Two projects on one host: their loopback connection was half a
	// connection in each session, and fuses in the merge.
	batch := newTrace("hostA", "batch")
	k := ISNPair{0xabc, 0xdef}
	a.client(102, "a-local", "127.0.0.1:41000", "127.0.0.1:9000", k, wall0)
	batch.server(400, "a-local-srv", "127.0.0.1:9000", "127.0.0.1:41000", k, wall0)
	return []Source{a.write(t, filepath.Join(dir, "a")), b.write(t, filepath.Join(dir, "b")),
		c.write(t, filepath.Join(dir, "c")), batch.write(t, filepath.Join(dir, "a-batch"))}
}

// report renders a Result as text for the golden file: the merged graph,
// then the fused connections, unresolved halves and renames.
func report(t *testing.T, r Result) string {
	t.Helper()
	var b bytes.Buffer
	if err := schema.WriteGraph(&b, r.Graph); err != nil {
		t.Fatal(err)
	}
	for _, c := range r.Conns {
		fmt.Fprintf(&b, "fused %s key=%s %s/%s(%s) -> %s/%s(%s) cross_project=%v\n",
			c.ID, c.Key, c.Client.Host, c.Client.ConnID, c.Client.Project,
			c.Server.Host, c.Server.ConnID, c.Server.Project, c.CrossProject())
	}
	for _, u := range r.Unresolved {
		fmt.Fprintf(&b, "unresolved %s/%s role=%s local=%s peer=%s reason=%s\n",
			u.Half.Host, u.Half.ConnID, u.Half.Role, u.Half.Local, u.Half.Peer, u.Reason)
	}
	var renames []string
	for k, v := range r.Renames {
		renames = append(renames, fmt.Sprintf("rename %s/%s -> %s\n", k.Host, k.ConnID, v))
	}
	slices.Sort(renames)
	b.WriteString(strings.Join(renames, ""))
	return b.String()
}

// TestMergeGolden merges the synthetic mesh and compares the whole
// result with testdata/mesh.golden (go test -update rewrites it).
func TestMergeGolden(t *testing.T) {
	r, err := Merge(context.Background(), meshSources(t, t.TempDir()), Options{})
	if err != nil {
		t.Fatal(err)
	}
	got := report(t, r)
	golden := filepath.Join("testdata", "mesh.golden")
	if *update {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("merge differs from %s (go test -update to accept):\n%s", golden, got)
	}
}

// TestMergeDeterministic: the order sources come in never changes the
// output bytes.
func TestMergeDeterministic(t *testing.T) {
	srcs := meshSources(t, t.TempDir())
	r1, err := Merge(context.Background(), srcs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(srcs)
	r2, err := Merge(context.Background(), srcs, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if report(t, r1) != report(t, r2) {
		t.Fatal("source order changed the merge")
	}
}

// TestMergeLoopbackPairUntouched: a connection the host's correlator
// fused itself (both ends in one trace) is no half and keeps its id and
// state.
func TestMergeLoopbackPairUntouched(t *testing.T) {
	tr := newTrace("hostA", "shop")
	k := ISNPair{1, 2}
	tr.client(100, "lo", "127.0.0.1:41000", "127.0.0.1:9000", k, wall0)
	tr.server(101, "lo", "127.0.0.1:9000", "127.0.0.1:41000", k, wall0)
	for i := range tr.edges {
		if tr.edges[i].Type == schema.EdgeConnected {
			tr.edges[i].State = schema.StateFused
		}
	}
	r, err := Merge(context.Background(), []Source{tr.write(t, t.TempDir())}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Conns)+len(r.Unresolved)+len(r.Renames) != 0 {
		t.Fatalf("loopback pair touched: %+v", r.Fusion)
	}
	for _, e := range r.Graph.Edges {
		if e.Type == schema.EdgeConnected && (e.To != "conn:lo" || e.State != schema.StateFused) {
			t.Errorf("edge %+v", e)
		}
	}
}

// TestMergeRejects covers inputs the merge refuses instead of guessing:
// one source holding two hosts' events, a corrupt event line, a missing
// graph, and a canceled context.
func TestMergeRejects(t *testing.T) {
	two := newTrace("hostA", "shop")
	two.client(100, "x", "10.0.0.1:1", "10.0.0.2:2", ISNPair{1, 2}, wall0)
	two.host = "hostB"
	two.client(100, "y", "10.0.0.1:3", "10.0.0.2:2", ISNPair{3, 4}, wall0)
	twoDir := two.write(t, filepath.Join(t.TempDir(), "two"))

	corrupt := newTrace("hostA", "shop").write(t, filepath.Join(t.TempDir(), "corrupt"))
	if err := os.WriteFile(filepath.Join(corrupt.Dir, eventsFile), []byte("{\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		src  Source
		ctx  func() context.Context
		want string
	}{
		"two hosts":     {twoDir, context.Background, "events of two hosts"},
		"corrupt event": {corrupt, context.Background, "line 1"},
		"no graph":      {NDJSONDir{Dir: t.TempDir()}, context.Background, graphFile},
		"canceled": {twoDir, func() context.Context {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx
		}, context.Canceled.Error()},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Merge(c.ctx(), []Source{c.src}, Options{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want one naming %q", err, c.want)
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
}
