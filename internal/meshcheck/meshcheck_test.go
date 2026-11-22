package meshcheck

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"iormesh/internal/federate"
	"iormesh/internal/normalize"
	"iormesh/internal/schema"
)

// fakeT records what a check reported; Fatalf stops the check by
// panicking with errFatal, which run recovers.
type fakeT struct {
	errs  []string
	fatal string
}

type fatalStop struct{}

func (f *fakeT) Helper() {}
func (f *fakeT) Errorf(format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *fakeT) Fatalf(format string, args ...any) {
	f.fatal = fmt.Sprintf(format, args...)
	panic(fatalStop{})
}

// run calls check with a fresh fakeT and returns it.
func run(check func(T)) *fakeT {
	f := &fakeT{}
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(fatalStop); !ok {
					panic(r)
				}
			}
		}()
		check(f)
	}()
	return f
}

// The pod run's addresses (task uh4): one pod per service.
var (
	ordersEnd    = netip.MustParseAddrPort("10.244.0.12:443")
	inventoryEnd = netip.MustParseAddrPort("10.244.0.11:80")
	billingEnd   = netip.MustParseAddrPort("10.244.0.10:443")
	gatewayAddr  = netip.MustParseAddr("10.244.0.13")
	ordersAddr   = ordersEnd.Addr()
)

// podHops are the pod run's four hops, two of them at inventory's end.
var podHops = []Hop{
	{ordersEnd, "gateway→orders (TLS)"},
	{inventoryEnd, "orders→inventory"},
	{billingEnd, "orders→billing (TLS)"},
	{inventoryEnd, "gateway→inventory"},
}

// conn is a fused connection from client to server in project ns, the
// client's address IPv4-mapped as a dual-stack JVM socket reports it.
func conn(id string, client netip.Addr, port uint16, server netip.AddrPort) federate.Conn {
	local := netip.AddrPortFrom(netip.AddrFrom16(client.As16()), port)
	return federate.Conn{ID: id,
		Client: federate.Half{Project: "ns", Local: local, Peer: server},
		Server: federate.Half{Project: "ns", Local: server, Peer: netip.AddrPortFrom(client, port)}}
}

// podResult is a clean merge of the pod run.
func podResult() federate.Result {
	var res federate.Result
	res.Conns = []federate.Conn{
		conn("c1", gatewayAddr, 40001, ordersEnd),
		conn("c2", ordersAddr, 40002, inventoryEnd),
		conn("c3", ordersAddr, 40003, billingEnd),
		conn("c4", gatewayAddr, 40004, inventoryEnd),
	}
	return res
}

// TestFusedSharedServerEnd: two hops at one server end (the gateway and
// orders both call inventory, each from its own pod) want two fused
// connections there; one is not enough.
func TestFusedSharedServerEnd(t *testing.T) {
	if f := run(func(t T) { Fused(t, podResult(), podHops, "ns") }); len(f.errs) != 0 {
		t.Fatalf("clean pod merge flagged: %v", f.errs)
	}
	res := podResult()
	res.Conns = slices.Delete(res.Conns, 3, 4)
	f := run(func(t T) { Fused(t, res, podHops, "ns") })
	if !hasErr(f, "1 fused connections at 10.244.0.11:80, want 2") {
		t.Errorf("a missing gateway→inventory hop not flagged: %v", f.errs)
	}
}

// TestFusedFlags: an unresolved end, a connection no hop declares, one in
// another project, one across projects when none is wanted, and ends that
// saw different 4-tuples each fail.
func TestFusedFlags(t *testing.T) {
	for name, tc := range map[string]struct {
		mutate  func(*federate.Result)
		project string
		want    string
	}{
		"unresolved": {func(r *federate.Result) {
			r.Unresolved = []federate.Unresolved{{Half: federate.Half{Role: "client", ConnID: "x", Host: "h"},
				Reason: "no_key"}}
		}, "ns", "unresolved client end x on h: no_key"},
		"undeclared": {func(r *federate.Result) {
			r.Conns[2].Server.Local = netip.MustParseAddrPort("10.244.0.10:8443")
		}, "ns", "is no declared hop"},
		"other project": {func(r *federate.Result) { r.Conns[0].Server.Project = "edge" }, "ns",
			"fused across projects ns and edge, want ns"},
		"within one project": {func(*federate.Result) {}, "", "fused within project ns"},
		"different 4-tuples": {func(r *federate.Result) {
			r.Conns[1].Server.Peer = netip.MustParseAddrPort("10.244.0.1:40002")
		}, "ns", "the ends saw different 4-tuples"},
	} {
		res := podResult()
		tc.mutate(&res)
		f := run(func(t T) { Fused(t, res, podHops, tc.project) })
		if !hasErr(f, tc.want) {
			t.Errorf("%s: want %q, got %v", name, tc.want, f.errs)
		}
	}
}

// hasErr reports whether some reported error contains want.
func hasErr(f *fakeT, want string) bool {
	return slices.ContainsFunc(f.errs, func(e string) bool { return strings.Contains(e, want) })
}

// golden reads one of the scenario's goldens.
func golden(t *testing.T, file string) normalize.Expectation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "workload", "scenarios", Scenario, file))
	if err != nil {
		t.Fatal(err)
	}
	x, err := normalize.ParseExpectation(data)
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	return x
}

// TestPodGoldenIsSpreadWithGatewayAlone pins expect-pods.json (task uh4)
// to expect-spread.json: the same events, addresses and hops, and the
// same edges but the gateway's ($p4) — on a host of its own, $h4, in the
// pod run, beside inventory on $h2 in the rig's.
func TestPodGoldenIsSpreadWithGatewayAlone(t *testing.T) {
	spread, pods := golden(t, "expect-spread.json"), golden(t, "expect-pods.json")
	if !reflect.DeepEqual(spread.Events, pods.Events) || !slices.Equal(spread.MustNotAppear, pods.MustNotAppear) {
		t.Error("expect-pods.json's events differ from expect-spread.json's")
	}
	if len(spread.Edges) != len(pods.Edges) {
		t.Fatalf("%d edges, the spread has %d", len(pods.Edges), len(spread.Edges))
	}
	moved := 0
	for i, e := range spread.Edges {
		if e.From == "$p4" && strings.Contains(e.To, "$h2") {
			e.To = strings.Replace(e.To, "$h2", "$h4", 1)
			moved++
		}
		if pods.Edges[i] != e {
			t.Errorf("edge %d = %+v, want %+v", i, pods.Edges[i], e)
		}
	}
	if moved != 4 {
		t.Errorf("moved %d of the gateway's edges, want its runs_on and three files", moved)
	}
}

// TestMutationsChangeBothGoldens: every mutation changes each
// one-service-per-host golden (a no-op would be caught by nothing) and
// leaves the original as it was.
func TestMutationsChangeBothGoldens(t *testing.T) {
	for _, file := range []string{"expect-spread.json", "expect-pods.json"} {
		want := golden(t, file)
		orig := golden(t, file)
		for name, mutate := range Mutations() {
			wrong := want
			wrong.Events = slices.Clone(want.Events)
			wrong.Edges = slices.Clone(want.Edges)
			mutate(&wrong)
			if reflect.DeepEqual(wrong, want) {
				t.Errorf("%s: %s left the golden as it was", file, name)
			}
			if !reflect.DeepEqual(want, orig) {
				t.Fatalf("%s: %s changed the original golden", file, name)
			}
		}
	}
}

// TestRequestsPaired: a request whose ends disagree on the path fails, a
// trace without a numbered request is fatal.
func TestRequestsPaired(t *testing.T) {
	ev := func(path string) schema.Event {
		return schema.Event{Event: schema.KindHTTP, ConnID: "c1", LatencyNS: 1,
			HTTP: &schema.HTTP{Method: "POST", Path: path, ReqSeq: 1}}
	}
	if f := run(func(t T) { RequestsPaired(t, []schema.Event{ev("/orders"), ev("/orders")}) }); len(f.errs) != 0 {
		t.Errorf("paired ends flagged: %v", f.errs)
	}
	if f := run(func(t T) { RequestsPaired(t, []schema.Event{ev("/orders"), ev("/billing")}) }); len(f.errs) != 1 {
		t.Errorf("disagreeing ends: %v", f.errs)
	}
	if f := run(func(t T) { RequestsPaired(t, nil) }); f.fatal == "" {
		t.Error("no numbered request was not fatal")
	}
	if f := run(func(t T) { HTTPTimed(t, []schema.Event{ev("/orders")}, false) }); len(f.errs) != 0 {
		t.Errorf("a timed request flagged: %v", f.errs)
	}
	untimed := ev("/orders")
	untimed.LatencyNS = 0
	if f := run(func(t T) { HTTPTimed(t, []schema.Event{untimed}, false) }); len(f.errs) != 1 {
		t.Errorf("an untimed request: %v", f.errs)
	}
}

// TestMergeMethods: two hosts' tables with one id each keep both methods
// apart, each stack moves with its host, and the input stacks stay.
func TestMergeMethods(t *testing.T) {
	stackA, stackB := []uint64{1}, []uint64{1}
	tr := normalize.Trace{Events: []schema.Event{
		{HostID: "pod-a", Java: &schema.Java{Stack: stackA}},
		{HostID: "pod-b", Java: &schema.Java{Stack: stackB}},
		{HostID: "pod-b"},
	}}
	MergeMethods(&tr, []HostMethods{
		{"pod-a", map[uint64]schema.Method{1: {MethodID: 1, Class: "A", Method: "a"}}},
		{"pod-b", map[uint64]schema.Method{1: {MethodID: 1, Class: "B", Method: "b"}}},
	})
	a, b := tr.Events[0].Java.Stack[0], tr.Events[1].Java.Stack[0]
	if a == b || tr.Methods[a].Class != "A" || tr.Methods[b].Class != "B" {
		t.Errorf("stacks %d, %d over methods %v", a, b, tr.Methods)
	}
	if stackA[0] != 1 || stackB[0] != 1 {
		t.Error("the hosts' own stacks changed")
	}
}
