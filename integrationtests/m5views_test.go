package integrationtests

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"iormesh/internal/schema"
	"iormesh/internal/tui"
	"iormesh/internal/viewmodel"
)

// The checkout's sequence as declared (workload/scenarios/microservices
// and its spread): the gateway's main thread calls orders, whose serving
// thread calls inventory, then billing, each of which writes its record
// as create-tmp, write, rename before it answers; then orders writes its
// own and answers the gateway. Every step causal: each service's client
// is synchronous (HttpClient#send), so each serving thread held its
// request alone. Steps on the scenario's files only — the JDK's own file
// work on those threads (name service, time zones) is interleaved too,
// but depends on the machine.
var checkoutStory = []string{
	"causal java@hosta[gateway/main] -> orders@hostb[orders/HTTP-Dispatcher]: POST /orders",
	"causal orders@hostb[orders/HTTP-Dispatcher] -> inventory@hosta[inventory/HTTP-Dispatcher]: POST /inventory/reserve",
	"causal inventory@hosta[inventory/HTTP-Dispatcher]: create $tmp/inventory.d/reservation-42.json.tmp",
	"causal inventory@hosta[inventory/HTTP-Dispatcher]: write $tmp/inventory.d/reservation-42.json.tmp",
	"causal inventory@hosta[inventory/HTTP-Dispatcher]: rename $tmp/inventory.d/reservation-42.json.tmp -> $tmp/inventory.d/reservation-42.json",
	"causal inventory@hosta[inventory/HTTP-Dispatcher] -> orders@hostb[orders/HTTP-Dispatcher]: 200",
	"causal orders@hostb[orders/HTTP-Dispatcher] -> billing@hostc[billing/HTTP-Dispatcher]: POST /billing/charge",
	"causal billing@hostc[billing/HTTP-Dispatcher]: create $tmp/billing.d/invoice-42.json.tmp",
	"causal billing@hostc[billing/HTTP-Dispatcher]: write $tmp/billing.d/invoice-42.json.tmp",
	"causal billing@hostc[billing/HTTP-Dispatcher]: rename $tmp/billing.d/invoice-42.json.tmp -> $tmp/billing.d/invoice-42.json",
	"causal billing@hostc[billing/HTTP-Dispatcher] -> orders@hostb[orders/HTTP-Dispatcher]: 200",
	"causal orders@hostb[orders/HTTP-Dispatcher]: create $tmp/orders.d/order-42.json.tmp",
	"causal orders@hostb[orders/HTTP-Dispatcher]: write $tmp/orders.d/order-42.json.tmp",
	"causal orders@hostb[orders/HTTP-Dispatcher]: rename $tmp/orders.d/order-42.json.tmp -> $tmp/orders.d/order-42.json",
	"causal orders@hostb[orders/HTTP-Dispatcher] -> java@hosta[gateway/main]: 201",
}

// checkoutStack names, per step, a frame its Java stack must hold: the
// method that caused it.
var checkoutStack = map[string]string{
	"POST /orders":                "iormesh.workload.scenarios.ServiceNode#checkout(",
	"POST /inventory/reserve":     "iormesh.workload.scenarios.ServiceNode#handle(",
	"reservation-42.json.tmp ->":  "iormesh.workload.scenarios.DataStore#save(",
	"invoice-42.json.tmp ->":      "iormesh.workload.scenarios.DataStore#save(",
	"orders.d/order-42.json.tmp ": "iormesh.workload.scenarios.DataStore#save(",
}

// story is c's steps as checkoutStory writes them: hops, and the file
// steps on the harness's temporary directory.
func (b *viewsBuild) story(c viewmodel.Conversation) []string {
	var out []string
	for _, s := range c.Steps {
		if s.Kind == viewmodel.StepFile && !strings.Contains(s.Label, b.tmp) {
			continue
		}
		line := s.Ordering + " " + b.lane(c, s.Lane)
		if s.Kind != viewmodel.StepFile {
			line += " -> " + b.lane(c, s.ToLane)
		}
		out = append(out, line+": "+strings.ReplaceAll(s.Label, b.tmp, "$tmp"))
	}
	return out
}

// assertCheckoutSequence: the checkout's steps are the declared story —
// every hop on its causing thread, each serving thread's file work in
// its place — every hop paired, every step causal (and so the summary's
// time-ordered and unordered counts zero), and each step's Java stack
// holds the method that caused it.
func assertCheckoutSequence(t *testing.T, b *viewsBuild, c viewmodel.Conversation) {
	t.Helper()
	if got := b.story(c); !slices.Equal(got, checkoutStory) {
		t.Errorf("checkout %s:\n got %q\nwant %q", c.ID, got, checkoutStory)
	}
	if c.MoreSteps != 0 || c.Hops != 3 {
		t.Errorf("checkout %s: %d hops, %d steps cut; want 3 and none", c.ID, c.Hops, c.MoreSteps)
	}
	for i, s := range c.Steps {
		if s.Ordering != viewmodel.OrderCausal || s.Kind == viewmodel.StepRequest && s.Hop != viewmodel.HopPaired {
			t.Errorf("step %d %q: %s, hop %q; the scenario is synchronous and every hop paired",
				i+1, s.Label, s.Ordering, s.Hop)
		}
	}
	for part, frame := range checkoutStack {
		s := c.Steps[stepIndex(t, c, part)]
		if !slices.ContainsFunc(s.Stack, func(f string) bool { return strings.HasPrefix(f, frame) }) {
			t.Errorf("step %q: stack %q lacks %s", s.Label, s.Stack, frame)
		}
	}
}

// show runs `iormeshctl show` over the build.
func (b *viewsBuild) show(t *testing.T, args ...string) string {
	t.Helper()
	return b.run(t, append(append([]string{"show"}, args...), "-store", b.store, "-builds", b.builds)...)
}

// wantMatches fails for every pattern out does not match.
func wantMatches(t *testing.T, what, out string, patterns ...string) {
	t.Helper()
	for _, p := range patterns {
		if !regexp.MustCompile(p).MatchString(out) {
			t.Errorf("%s does not match %s:\n%s", what, p, out)
		}
	}
}

// wantInOrder fails unless out holds every one of lines, in order.
func wantInOrder(t *testing.T, what, out string, lines ...string) {
	t.Helper()
	rest := out
	for _, l := range lines {
		i := strings.Index(rest, l)
		if i < 0 {
			t.Errorf("%s lacks %q (in this order: %q):\n%s", what, l, lines, out)
			return
		}
		rest = rest[i+len(l):]
	}
}

// assertShowWalk walks the mesh in plain text: the named services and
// their edges, the checkout among the conversations, its sequence with
// the rename step's top frame and ref, that step's whole Java stack
// (-json) and raw event (query -ref), and the serving thread's story.
func assertShowWalk(t *testing.T, b *viewsBuild, c viewmodel.Conversation) {
	wantMatches(t, "show services", b.show(t, "services"), `(?m)^orders\s+yes\s+1\s+0\s+tls\s`,
		`(?m)^inventory\s+yes\s+2\s+0\s+plain\s`, `(?m)^billing\s+yes\s+1\s+0\s+tls\s`)
	wantMatches(t, "show edges", b.show(t, "edges"), `(?m)^java\s+orders\s+fused\s+1\s+0\s`,
		`(?m)^java\s+billing\s+fused\s+1\s+0\s`)
	wantMatches(t, "show conversations", b.show(t, "conversations"),
		`(?m)^`+c.ID+`\s.*\sjava\s+POST /orders\s+3\s+\d+\s+java orders inventory billing$`)
	rename := c.Steps[stepIndex(t, c, "rename", "order-42.json.tmp")]
	wantInOrder(t, "show sequence", b.show(t, "sequence", c.ID),
		"java/main -> orders/HTTP-Dispatcher: POST /orders",
		"orders/HTTP-Dispatcher -> inventory/HTTP-Dispatcher: POST /inventory/reserve",
		"orders/HTTP-Dispatcher -> billing/HTTP-Dispatcher: POST /billing/charge",
		"orders/HTTP-Dispatcher: "+rename.Label, "at "+rename.Stack[0], "ref "+rename.Ref,
		"orders/HTTP-Dispatcher -> java/main: 201")
	var js viewmodel.Conversation
	if err := json.Unmarshal([]byte(b.show(t, "sequence", c.ID, "-json")), &js); err != nil {
		t.Fatalf("show sequence -json: %v", err)
	}
	if got := js.Steps[stepIndex(t, js, "rename", "order-42.json.tmp")].Stack; !reflect.DeepEqual(got, rename.Stack) {
		t.Errorf("show sequence -json: the rename's stack %q, the model's %q", got, rename.Stack)
	}
	assertRawEvent(t, b, rename.Ref)
	wantInOrder(t, "show thread", b.show(t, "thread", "orders/HTTP-Dispatcher"),
		c.ID+"#1  received POST /orders from java", rename.Label)
	assertProcessesShow(t, b)
}

// assertRawEvent: `query -ref` prints the step's raw event — the rename
// with its Java stack's method ids — and nothing else.
func assertRawEvent(t *testing.T, b *viewsBuild, ref string) {
	t.Helper()
	out := b.run(t, "query", "-store", b.store, "-db", b.db, "-ref", ref)
	var ev schema.Event
	if err := json.Unmarshal([]byte(out), &ev); err != nil {
		t.Fatalf("query -ref %s: %v\n%s", ref, err, out)
	}
	if ev.Event != "rename" || ev.Args.NewPath == nil || !strings.HasSuffix(*ev.Args.NewPath, "/order-42.json") ||
		ev.Java == nil || len(ev.Java.Stack) == 0 {
		t.Errorf("query -ref %s: not the rename with its stack:\n%s", ref, out)
	}
}

// assertProcessesShow: one screen answers what orders touches and what
// touches it back — its config read at startup and its record's
// create-tmp/rename, its server socket with the gateway as its peer.
func assertProcessesShow(t *testing.T, b *viewsBuild) {
	t.Helper()
	out := b.show(t, "processes")
	_, block, _ := strings.Cut(out, "process    orders/hostb/java[")
	block, _, _ = strings.Cut(block, "\n\n")
	gateway := pidOf(t, b, "gateway")
	wantMatches(t, "show processes, orders' block", block, `(?m)^\s+serves\s+orders$`,
		`(?m)^\s+file\s+\S+/orders\.d/orders\.conf: [1-9]\d* B read, [1-9]\d* B written`,
		`(?m)^\s+file\s+\S+/orders\.d/order-42\.json\.tmp: .*rename -> \S+/orders\.d/order-42\.json$`,
		fmt.Sprintf(`(?m)^\s+socket\s+\S+ fd \d+ server %s:%s -> .*service orders, peer edge/hosta/java\[%d\]`,
			regexp.QuoteMeta(rigServerAddr), spreadTLSPort, gateway))
}

// pidOf is the PID of the workload of role.
func pidOf(t *testing.T, b *viewsBuild, role string) uint32 {
	t.Helper()
	for pid, r := range b.roles {
		if r == role {
			return pid
		}
	}
	t.Fatalf("no %s workload", role)
	return 0
}

// assertTUIWalk is ob4's terminal walk on the spread's model: filter the
// services to orders, open its conversations, the checkout's sequence,
// down to the rename of its record — the step's detail names the method
// in its Java stack and the query of its raw event — and on to the
// serving thread's story. The model is what tui reads: model.json
// through show's loader (task ob4's TestTUIWalksTheMicroservicesMesh
// holds tui to that file).
func assertTUIWalk(t *testing.T, b *viewsBuild, c viewmodel.Conversation) {
	m := tui.New(b.model)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	tuiKeys(m, "/", "o", "r", "d", "e", "r", "s", "enter", "enter") // orders' conversations
	tuiKeys(m, "enter")                                             // the checkout's sequence
	i := stepIndex(t, c, "rename", "order-42.json.tmp")
	for range i {
		tuiKeys(m, "down")
	}
	fr := tuiFrame(m)
	for _, want := range []string{"iormesh.workload.scenarios.DataStore#save(DataStore.java:",
		"iormeshctl query -ref " + c.Steps[i].Ref, "order-42.json"} {
		if !strings.Contains(fr, want) {
			t.Errorf("the rename step's detail lacks %q:\n%s", want, fr)
		}
	}
	tuiKeys(m, "enter") // the step's thread
	if fr := tuiFrame(m); !strings.Contains(fr, "received POST /orders from java") {
		t.Errorf("orders/HTTP-Dispatcher's story:\n%s", fr)
	}
}

// tuiFrame is the explorer's frame without its styles.
func tuiFrame(m *tui.Model) string {
	return ansi.Strip(m.Render())
}

// tuiKeys presses keys; a word is a special key ("enter", "down").
func tuiKeys(m *tui.Model, ks ...string) {
	special := map[string]rune{"enter": tea.KeyEnter, "down": tea.KeyDown, "esc": tea.KeyEscape}
	for _, k := range ks {
		if r, ok := special[k]; ok {
			m.Update(tea.KeyPressMsg{Code: r})
			continue
		}
		m.Update(tea.KeyPressMsg{Code: []rune(k)[0], Text: k})
	}
}
