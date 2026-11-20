package ctl

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"iormesh/internal/schema"
	"iormesh/internal/viewmodel"
)

// The microservices mesh of the conversation tests (docs/visualization.md
// V2): four hosts, gateway → orders → inventory and billing, like M0's
// microservices scenario. orders' worker thread reads its config,
// calls inventory (HTTP/1.1, paired by req_seq), writes the order the
// create-tmp-then-rename way and charges billing over HTTP/2 (no
// req_seq: joined only by the clock bounds). billing's clock runs 30 ms
// ahead of everyone else's; inventory's has no bound to anyone but via
// its own connection.
const billingSkew = 30_000 // µs

// msHost is one host of the mesh with its methods table.
type msHost struct {
	dir     string
	trace   *hostTrace
	methods []schema.Method
}

// microMesh builds the mesh; withClock false leaves out conn C's data
// records, so no bound relates orders and billing.
func microMesh(withClock bool) []msHost {
	gw := &hostTrace{host: "gw", project: "shop", session: "g1"}
	orders := &hostTrace{host: "orders", project: "shop", session: "o1"}
	inv := &hostTrace{host: "inv", project: "shop", session: "i1"}
	bill := &hostTrace{host: "bill", project: "shop", session: "b1"}
	for _, h := range []*hostTrace{gw, orders, inv, bill} {
		h.header()
	}
	// Conn A gw → orders, B orders → inventory, C orders → billing.
	tcpPair(gw, orders, 10, 20, 7, 9, 500, "a", "a", "10.0.0.1:40000", "10.0.0.2:8080", 0x1000, 0x2000)
	tcpPair(orders, inv, 20, 30, 10, 11, 1900, "b", "b", "10.0.0.2:41000", "10.0.0.3:8081", 0x3000, 0x4000)
	tcpPair(orders, bill, 20, 40, 12, 13, 3900, "c", "c", "10.0.0.2:42000", "10.0.0.4:8082", 0x5000, 0x6000)
	if withClock {
		orders.data(20, schema.KindWrite, 12, 4100, "c", 0x5001, 100)
		bill.data(40, schema.KindRead, 13, 4110, "c", 0x5001, 100)
		bill.data(40, schema.KindWrite, 13, 5900, "c", 0x6001, 50)
		orders.data(20, schema.KindRead, 12, 5910, "c", 0x6001, 50)
	}
	javaHTTP(gw, 10, 11, "http-1", 900, 9500, "", "", "GET", "/checkout", 0, 1)
	javaHTTP(gw, 10, 11, "http-1", 950, 9200, "a", "orders", "GET", "/orders/42", 1, 2)
	ordersWork(orders)
	javaHTTP(inv, 30, 31, "inv-1", 2200, 2800, "b", "", "GET", "/stock/7", 1, 1)
	fileOp(inv, 30, 31, schema.KindOpenat, 7, 7, 2300, schema.Args{Path: bptr("/var/inv/stock.db")})
	read := fileOp(inv, 30, 31, schema.KindPread64, 7, 64, 2350, schema.Args{Offset: bptr(int64(4096))})
	read.Bytes = 64
	javaHTTP(bill, 40, 41, "bill-1", 5000, 6000, "c", "", "POST", "/charge", 0, 1)
	for _, h := range []*hostTrace{gw, orders, inv, bill} {
		h.end(20_000, schema.EndStop)
	}
	shift(bill, billingSkew)
	return []msHost{{"gw", gw, frames("com.shop.gw.Checkout#run(Checkout.java:30)",
		"com.shop.gw.OrdersClient#get(OrdersClient.java:12)")},
		{"orders", orders, frames("com.shop.orders.OrderHandler#handle(OrderHandler.java:20)",
			"com.shop.orders.DataStore#save(DataStore.java:42)", "com.shop.orders.Config#load(Config.java:9)",
			"com.shop.orders.Downstream#call(Downstream.java:15)")},
		{"inv", inv, frames("com.shop.inv.StockHandler#handle(StockHandler.java:8)")},
		{"bill", bill, frames("com.shop.bill.ChargeHandler#handle(ChargeHandler.java:5)")}}
}

// ordersWork is orders' worker thread serving GET /orders/42: config
// read, inventory call, the order written to a temp file and renamed,
// the billing call.
func ordersWork(h *hostTrace) {
	javaHTTP(h, 20, 21, "worker-3", 1000, 9000, "a", "", "GET", "/orders/42", 1, 1)
	cfg := fileOp(h, 20, 21, schema.KindOpenat, 5, 5, 1100, schema.Args{Path: bptr("/etc/orders.conf")})
	cfg.Java.Stack = []uint64{3, 1}
	fileOp(h, 20, 21, schema.KindRead, 5, 512, 1150, schema.Args{}).Bytes = 512
	fileOp(h, 20, 21, schema.KindClose, 5, 0, 1160, schema.Args{})
	javaHTTP(h, 20, 21, "worker-3", 2000, 3000, "b", "inventory", "GET", "/stock/7", 1, 4)
	tmp := fileOp(h, 20, 21, schema.KindOpenat, 6, 6, 3500,
		schema.Args{Path: bptr("/data/order-42.json.tmp"), Flags: bptr(uint32(0o101))})
	tmp.Java.Stack = []uint64{2, 1}
	fileOp(h, 20, 21, schema.KindWrite, 6, 128, 3600, schema.Args{}).Bytes = 128
	fileOp(h, 20, 21, schema.KindClose, 6, 0, 3650, schema.Args{})
	fileOp(h, 20, 21, schema.KindRename, schema.NoFD, 0, 3700,
		schema.Args{Path: bptr("/data/order-42.json.tmp"), NewPath: bptr("/data/order-42.json")}).Java.Stack =
		[]uint64{2, 1}
	javaHTTP(h, 20, 21, "worker-3", 4000, 7000, "c", "billing", "POST", "/charge", 0, 4)
	// Another thread's file work during the request is not this
	// request's story.
	fileOp(h, 20, 22, schema.KindOpenat, 8, 8, 1200, schema.Args{Path: bptr("/tmp/other")})
}

// tcpPair adds one TCP connection from cli's pid cp (fd cfd) to srv's
// pid sp (fd sfd) at µs at: connect, accept and ESTABLISHED records whose
// tcp_sock fields rebuild the ISN pair (kc, ks).
func tcpPair(cli, srv *hostTrace, cp, sp uint32, cfd, sfd int32, at uint64, cconn, sconn, caddr, saddr string,
	kc, ks uint32) {
	cli.tcp(cp, cfd, at, cconn, caddr, saddr, "SYN_SENT", 0, 0, 0, 0)
	cli.add(cp, schema.KindConnect, cfd, -115, at, cconn, tupleArgs(caddr, saddr))
	cli.tcp(cp, cfd, at+10, cconn, caddr, saddr, "ESTABLISHED", kc+1, ks+1, 1, 0)
	srv.add(sp, schema.KindAccept4, sfd, int64(sfd), at+5, sconn, tupleArgs(saddr, caddr))
	srv.tcp(sp, sfd, at+5, sconn, saddr, caddr, "ESTABLISHED", ks+1, kc+1, 0, 0)
}

// javaHTTP adds one http event on thread tid ("thread") spanning
// [start, end] µs; reqHost "" makes it the server's end. Every request
// is a synchronous library's, held by its thread (http.held).
func javaHTTP(h *hostTrace, pid, tid uint32, thread string, start, end uint64, conn, reqHost, method,
	path string, reqSeq uint32, top uint64) {
	ev := h.add(pid, schema.KindHTTP, schema.NoFD, 0, start, conn, schema.Args{})
	ev.TID, ev.LatencyNS = tid, (end-start)*uint64(time.Microsecond)
	ev.Attribution, ev.Java = schema.AttributionJava, &schema.Java{Thread: thread, Stack: []uint64{top}}
	version := "HTTP/1.1"
	if reqSeq == 0 && conn != "" {
		version = "HTTP/2"
	}
	ev.HTTP = &schema.HTTP{Method: method, Scheme: "http", Host: reqHost, Path: path, Status: 200,
		ReqSeq: reqSeq, Version: version, Held: true}
}

// fileOp adds one Java-attributed file event on thread tid.
func fileOp(h *hostTrace, pid, tid uint32, kind string, fd int32, ret int64, at uint64,
	args schema.Args) *schema.Event {
	ev := h.add(pid, kind, fd, ret, at, "", args)
	thread := map[uint32]string{21: "worker-3", 22: "other", 31: "inv-1"}[tid]
	ev.TID, ev.Attribution, ev.Java = tid, schema.AttributionJava, &schema.Java{Thread: thread, Stack: []uint64{1}}
	if fd >= 0 {
		ev.FDGen = 1
	}
	return ev
}

// shift moves every event of h by d µs: a host whose clock runs ahead.
func shift(h *hostTrace, d uint64) {
	for i := range h.events {
		h.events[i].WallNS += d * uint64(time.Microsecond)
	}
}

// frames is a methods table, ids 1… in order.
func frames(fs ...string) []schema.Method {
	var out []schema.Method
	for i, f := range fs {
		m, err := schema.ParseFrame(f)
		if err != nil {
			panic(err)
		}
		m.SchemaVersion, m.MethodID = schema.Version, uint64(i+1)
		out = append(out, m)
	}
	return out
}

// newMicroFixture writes the mesh into a fresh store and builds it.
func newMicroFixture(t *testing.T, withClock bool) *buildFixture {
	t.Helper()
	f := newBuildFixture(t)
	if err := os.RemoveAll(f.store); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.store, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, h := range microMesh(withClock) {
		writeJavaSegment(t, f.store, h.dir, h.trace, h.methods)
	}
	f.mustRun(t, "build")
	return f
}

// shopOutput reads one of the shop build's output files.
func (f *buildFixture) shopOutput(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.builds, "shop", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestConversationsGolden builds the microservices mesh: the gateway's
// checkout is one conversation from the gateway through orders to
// inventory and billing, with orders' file work interleaved in its
// thread's order; the inventory hop is paired and causal, the billing
// hop (HTTP/2) joined by the clock bounds and time-ordered. The text
// views, the conversation JSON and the SVG exports match their goldens,
// and a rebuild writes the same bytes.
func TestConversationsGolden(t *testing.T) {
	f := newMicroFixture(t, true)
	golden(t, "conv_list.golden", f.mustRun(t, "show", "conversations"))
	seq := f.mustRun(t, "show", "sequence", "0001")
	golden(t, "conv_sequence.golden", seq)
	golden(t, "conv_sequence_json.golden", f.mustRun(t, "show", "sequence", "-json"))
	svg, graph := f.shopOutput(t, "sequence/0001.svg"), f.shopOutput(t, graphSVGName)
	golden(t, "conv_sequence_svg.golden", svg)
	golden(t, "conv_graph_svg.golden", graph)
	model := f.shopOutput(t, modelFileName)
	f.mustRun(t, "build")
	if f.shopOutput(t, modelFileName) != model || f.shopOutput(t, "sequence/0001.svg") != svg ||
		f.shopOutput(t, graphSVGName) != graph {
		t.Fatal("a rebuild changed the model or the SVG exports")
	}
	c := firstConversation(t, f)
	wantHops := map[string]string{"GET /orders/42": viewmodel.HopPaired,
		"GET /stock/7": viewmodel.HopPaired, "POST /charge": viewmodel.HopClock}
	for _, s := range c.Steps {
		if want, ok := wantHops[s.Label]; ok && s.Kind == viewmodel.StepRequest && s.Hop != want {
			t.Errorf("%s: hop %s, want %s", s.Label, s.Hop, want)
		}
	}
}

// firstConversation decodes `show sequence -json`.
func firstConversation(t *testing.T, f *buildFixture) viewmodel.Conversation {
	t.Helper()
	var c viewmodel.Conversation
	if err := json.Unmarshal([]byte(f.mustRun(t, "show", "sequence", "-json")), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestConversationsUnprovableClock: without data records on the billing
// connection no bound relates orders' clock to billing's, so the HTTP/2
// hop cannot be joined — the client's step ends at billing unpaired and
// billing's request is a conversation of its own, named after its
// traced caller. Nothing is put in a sequence the clocks cannot prove.
func TestConversationsUnprovableClock(t *testing.T) {
	f := newMicroFixture(t, false)
	out := f.mustRun(t, "show", "conversations")
	if !strings.Contains(out, "POST /charge") || strings.Count(out, "\n") != 3 {
		t.Fatalf("want the checkout and billing's own conversation:\n%s", out)
	}
	for _, s := range firstConversation(t, f).Steps {
		if s.Label == "POST /charge" && (s.Hop != viewmodel.HopUnpaired || s.Ordering == viewmodel.OrderCausal) {
			t.Errorf("billing hop %+v, want unpaired and not causal", s)
		}
	}
	seq := f.mustRun(t, "show", "sequence", "0002")
	if !strings.Contains(seq, "unpaired") {
		t.Errorf("billing's own conversation:\n%s", seq)
	}
}

// TestQueryRef: a step's ref selects exactly its raw event.
func TestQueryRef(t *testing.T) {
	f := newMicroFixture(t, true)
	var ref string
	for _, s := range firstConversation(t, f).Steps {
		if strings.HasPrefix(s.Label, "rename ") {
			ref = s.Ref
		}
	}
	evs, _ := queryLines(t, f.mustRun(t, "query", "-ref", ref))
	if len(evs) != 1 || evs[0].Event != schema.KindRename || *evs[0].Args.NewPath != "/data/order-42.json" {
		t.Fatalf("query -ref %s: %+v", ref, evs)
	}
	if code := f.run("query", "-ref", "no-seq"); code != exitUsage {
		t.Errorf("a malformed ref: exit %d, want %d", code, exitUsage)
	}
	if code := f.run("show", "sequence", "9999"); code == exitOK {
		t.Error("show sequence of an unknown id succeeded")
	}
	if !bytes.Contains(f.stderr.Bytes(), []byte("no such conversation")) {
		t.Errorf("stderr: %s", f.stderr)
	}
}
