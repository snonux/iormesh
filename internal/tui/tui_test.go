package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"iormesh/internal/viewmodel"
)

// esc is an escape sequence a compromised server could plant in a name:
// printed raw it would clear the operator's screen.
const esc = "\x1b[2J"

// sizes are the terminal sizes every frame test renders at: the
// smallest drawn, narrow stacked layouts, the side-by-side threshold
// on both sides, a wide one, and below the minimum.
var sizes = [][2]int{{20, 6}, {40, 10}, {80, 24}, {99, 30}, {100, 30}, {160, 48}, {19, 5}, {300, 8}}

// fixture is a small mesh: gateway calls orders, orders reads a file
// and calls a service whose name carries an escape sequence; a thread,
// a path and a frame carry escapes too.
func fixture() viewmodel.Model {
	start := time.Date(2025, 11, 20, 12, 0, 0, 0, time.UTC)
	lanes := fixtureLanes()
	return viewmodel.Model{
		Run: viewmodel.Run{Projects: []string{"shop"}, Hosts: []string{"gw", "orders"}, Sessions: 2,
			Events: 40, First: start, Last: start.Add(time.Minute)},
		Services: []viewmodel.Service{
			{Name: "db" + esc, Endpoints: []string{"10.0.0.9:5432"}, Conns: 1, Unresolved: 1},
			{Name: "gateway"},
			{Name: "orders", Named: true, Endpoints: []string{"orders.example.org:8080"}, Conns: 3, TLS: "plain",
				Requests: 3, TopPaths: []viewmodel.PathStat{{Method: "GET", Path: "/orders/{id}", Count: 3}}},
		},
		Edges: []viewmodel.Edge{
			{From: "gateway", To: "orders", State: viewmodel.StateFused, Conns: 3},
			{From: "orders", To: "db" + esc, State: viewmodel.StateUnresolved, Conns: 1, Unresolved: 1},
		},
		Conversations: fixtureConversations(start, lanes),
		Threads: []viewmodel.ThreadStory{
			{Lifeline: lanes[0], Entries: []viewmodel.ThreadEntry{{Conversation: "0001", Step: 1,
				Act: viewmodel.ActSend, Label: "GET /orders/42", Peer: "orders", Ordering: viewmodel.OrderCausal,
				Ref: "shop/gw/s1/5"}}},
			{Lifeline: lanes[1], Entries: []viewmodel.ThreadEntry{{Conversation: "0001", Step: 2,
				Act: viewmodel.ActFile, Label: "read /etc/orders" + esc + ".conf",
				Ordering: viewmodel.OrderCausal, Ref: "shop/orders/s2/13"}, {Conversation: "0001", Step: 3,
				Act: viewmodel.ActSend, Label: "POST /q", Peer: "db" + esc, Ordering: viewmodel.OrderCausal,
				Ref: "shop/orders/s2/16"}}},
		},
		Processes: []viewmodel.Process{{ID: "p1", Project: "shop", Host: "orders", FQDN: "orders.example.org",
			PID: 20, Incarnation: 1, Incarnations: 2, Comm: "java" + esc, Service: "orders",
			Files: []viewmodel.ProcFile{{Path: "/etc/orders" + esc + ".conf", ReadBytes: 512}}}},
	}
}

// fixtureLanes are the fixture's lifelines.
func fixtureLanes() []viewmodel.Lifeline {
	return []viewmodel.Lifeline{
		{Service: "gateway", Host: "gw.example.org", PID: 10, TID: 11, Thread: "http-1"},
		{Service: "orders", Host: "orders.example.org", PID: 20, TID: 21, Thread: "worker-3" + esc},
		{Service: "db" + esc},
	}
}

// fixtureConversations are the checkout through orders and an older
// health check.
func fixtureConversations(start time.Time, lanes []viewmodel.Lifeline) []viewmodel.Conversation {
	steps := []viewmodel.Step{
		{Kind: viewmodel.StepRequest, Service: "gateway", Thread: "http-1", To: "orders",
			Label: "GET /orders/42", Ordering: viewmodel.OrderTime, Hop: viewmodel.HopUntraced,
			Stack: []string{}, Ref: "shop/gw/s1/5", Lane: 0, ToLane: 1},
		{Kind: viewmodel.StepFile, Service: "orders", Thread: "worker-3" + esc,
			Label: "read /etc/orders" + esc + ".conf", Ordering: viewmodel.OrderCausal, Bytes: 512,
			Stack: []string{"com.shop.Config#load(Config.java:9)", "com.shop.Main#run" + esc + "(Main.java:3)"},
			Ref:   "shop/orders/s2/13", Lane: 1, ToLane: -1},
		{Kind: viewmodel.StepRequest, Service: "orders", Thread: "worker-3" + esc, To: "db" + esc,
			Label: "POST /q", Ordering: viewmodel.OrderCausal, Hop: viewmodel.HopUnpaired,
			Stack: []string{"com.shop.Db#call(Db.java:15)"}, Ref: "shop/orders/s2/16", Lane: 1, ToLane: 2},
		{Kind: viewmodel.StepResponse, Service: "orders", Thread: "worker-3" + esc, To: "gateway",
			Label: "200", Ordering: viewmodel.OrderCausal, Latency: 9 * time.Millisecond,
			Stack: []string{}, Ref: "shop/orders/s2/20", Lane: 1, ToLane: 0},
	}
	conv := viewmodel.Conversation{ID: "0001", Start: start, Entry: "gateway", Label: "GET /orders/42",
		Hops: 2, Services: []string{"gateway", "orders", "db" + esc}, Lifelines: lanes, Steps: steps}
	other := viewmodel.Conversation{ID: "0002", Start: start.Add(-time.Second), Entry: "gateway",
		Label: "GET /health", Hops: 1, Services: []string{"gateway"}, Lifelines: lanes[:1],
		Steps: []viewmodel.Step{{Kind: viewmodel.StepFile, Service: "gateway", Thread: "http-1",
			Label: "stat /tmp", Ordering: viewmodel.OrderCausal, Stack: []string{}, Ref: "shop/gw/s1/2",
			Lane: 0, ToLane: -1}}}
	return []viewmodel.Conversation{conv, other}
}

// newSized is the explorer over the fixture at w×h.
func newSized(w, h int) *Model {
	m := New(fixture())
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

// press sends keys; a key of more than one rune is a special key's name
// ("enter", "esc", "down", "tab", ...).
func press(m *Model, keys ...string) {
	for _, k := range keys {
		m.Update(keyMsg(k))
	}
}

func keyMsg(k string) tea.KeyPressMsg {
	special := map[string]rune{"enter": tea.KeyEnter, "esc": tea.KeyEscape, "down": tea.KeyDown,
		"up": tea.KeyUp, "tab": tea.KeyTab, "backspace": tea.KeyBackspace, "pgdown": tea.KeyPgDown,
		"end": tea.KeyEnd}
	if r, ok := special[k]; ok {
		return tea.KeyPressMsg{Code: r}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

// plain is the frame without the explorer's styles.
func plain(m *Model) string { return ansi.Strip(m.Render()) }

// TestFramesFit: on every screen, at every size, with the cursor on
// every row and the help open, a frame is exactly the terminal's height
// and every line exactly its width — never wider (ior's rule).
func TestFramesFit(t *testing.T) {
	for _, sz := range sizes {
		for s := range numScreens {
			m := newSized(sz[0], sz[1])
			press(m, string(rune('1'+s)))
			for range 6 {
				checkFrame(t, m, sz[0], sz[1])
				press(m, "down")
			}
			press(m, "tab", "end")
			checkFrame(t, m, sz[0], sz[1])
			press(m, "?")
			checkFrame(t, m, sz[0], sz[1])
		}
	}
}

func checkFrame(t *testing.T, m *Model, w, h int) {
	t.Helper()
	lines := strings.Split(m.Render(), "\n")
	if w < minWidth || h < minHeight {
		if len(lines) != 1 || ansi.StringWidth(lines[0]) != w {
			t.Fatalf("%dx%d: want one line saying it is too small, got %q", w, h, lines)
		}
		return
	}
	if len(lines) != h {
		t.Fatalf("%dx%d screen %d: %d lines", w, h, m.cur, len(lines))
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Fatalf("%dx%d screen %d line %d: width %d: %q", w, h, m.cur, i, got, ansi.Strip(l))
		}
	}
}

// TestNoEscapeReachesTheTerminal: a name, thread, path or frame holding
// an escape sequence is printed Go-quoted on every screen, never raw;
// the only escape sequences in a frame are the explorer's own styles.
func TestNoEscapeReachesTheTerminal(t *testing.T) {
	seen := false
	for s := range numScreens {
		m := newSized(160, 48)
		press(m, string(rune('1'+s)))
		for range 5 {
			raw := m.Render()
			if strings.Contains(raw, esc) {
				t.Fatalf("screen %d: raw escape in the frame:\n%q", s, raw)
			}
			for _, r := range ansi.Strip(raw) {
				if r < 0x20 && r != '\n' || r == 0x7f || r >= 0x80 && r < 0xa0 {
					t.Fatalf("screen %d: control %U in the frame", s, r)
				}
			}
			seen = seen || strings.Contains(raw, `\x1b[2J`)
			press(m, "down")
		}
	}
	if !seen {
		t.Fatal("the quoted escape never showed: the fixture's names were not rendered")
	}
}

// TestWalk walks mesh → conversation → step → Java stack → thread and
// back: ⏎ on a service scopes the conversations, ⏎ opens one's
// sequence, a step's detail holds its whole stack and the query of its
// raw event, ⏎ on it opens its thread's story, ⏎ there the newest
// conversation, and esc retraces it all.
func TestWalk(t *testing.T) {
	m := newSized(160, 48)
	press(m, "down", "down", "enter") // services: db, gateway, orders
	if m.cur != scrConversations || m.scope.service != "orders" {
		t.Fatalf("⏎ on orders: screen %d scope %+v", m.cur, m.scope)
	}
	if f := plain(m); !strings.Contains(f, "conversations touching orders") || strings.Contains(f, "GET /health") {
		t.Fatalf("scoped list:\n%s", f)
	}
	press(m, "enter", "down")
	f := plain(m)
	for _, want := range []string{"STEP 0001", "at com.shop.Config#load(Config.java:9)",
		`at "com.shop.Main#run\x1b[2J(Main.java:3)"`, "iormeshctl query -ref shop/orders/s2/13", "ordering   causal"} {
		if !strings.Contains(f, want) {
			t.Errorf("step detail lacks %q:\n%s", want, f)
		}
	}
	press(m, "enter")
	if m.cur != scrThreads || m.selected(scrThreads) != 1 {
		t.Fatalf("⏎ on the step: screen %d thread %d", m.cur, m.selected(scrThreads))
	}
	press(m, "enter")
	if m.cur != scrSequence || m.panes[scrSequence].cursor != 2 {
		t.Fatalf("⏎ on the story: screen %d step %d", m.cur, m.panes[scrSequence].cursor)
	}
	press(m, "esc", "esc", "esc", "esc")
	if m.cur != scrServices || m.scope != (scope{}) || len(m.back) != 0 {
		t.Fatalf("esc did not retrace: screen %d scope %+v back %v", m.cur, m.scope, m.back)
	}
}

// TestEdgeScopeAndFilter: ⏎ on an edge lists the conversations with a
// request along it; / filters a list's rows and esc clears the filter.
func TestEdgeScopeAndFilter(t *testing.T) {
	m := newSized(100, 24)
	press(m, "2", "enter")
	if f := plain(m); !strings.Contains(f, "conversations gateway -> orders") || strings.Contains(f, "GET /health") {
		t.Fatalf("edge scope:\n%s", f)
	}
	press(m, "1", "/", "o", "r", "d")
	if f := plain(m); !strings.Contains(f, "/ord_") || len(m.listing(scrServices).rows) != 1 {
		t.Fatalf("filter:\n%s", f)
	}
	press(m, "enter")
	if m.filtering || m.panes[scrServices].filter != "ord" || m.selected(scrServices) != 2 {
		t.Fatalf("⏎ keeps the filter: %+v", m.panes[scrServices])
	}
	press(m, "/", "esc")
	if m.panes[scrServices].filter != "" || len(m.listing(scrServices).rows) != 3 {
		t.Fatalf("esc clears the filter: %+v", m.panes[scrServices])
	}
}

// TestDeterministic: the same keys at the same size give the same
// frames, byte for byte.
func TestDeterministic(t *testing.T) {
	keys := []string{"3", "enter", "down", "tab", "down", "4", "5", "6", "7", "1", "enter"}
	a, b := newSized(120, 40), newSized(120, 40)
	for _, k := range keys {
		press(a, k)
		press(b, k)
		if a.Render() != b.Render() {
			t.Fatalf("after %q the frames differ", k)
		}
	}
}

// TestEmptyModel: a build without conversations or services draws and
// says why the screens are empty.
func TestEmptyModel(t *testing.T) {
	m := New(viewmodel.Model{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	for s := range numScreens {
		press(m, string(rune('1'+s)), "enter", "down", "s")
		checkFrame(t, m, 80, 20)
	}
	press(m, "3")
	if !strings.Contains(plain(m), "no conversations") {
		t.Fatalf("empty conversations:\n%s", plain(m))
	}
}

// TestProgram runs the explorer in a real Bubble Tea program (teatest):
// it draws, walks to a step's stack, and q quits.
func TestProgram(t *testing.T) {
	tm := teatest.NewTestModel(t, New(fixture()), teatest.WithInitialTermSize(120, 30))
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("services")) },
		teatest.WithDuration(5*time.Second))
	for _, k := range []string{"4", "down"} {
		tm.Send(keyMsg(k))
	}
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool { return bytes.Contains(b, []byte("query -ref")) },
		teatest.WithDuration(5*time.Second))
	tm.Send(keyMsg("q"))
	final, ok := tm.FinalModel(t, teatest.WithFinalTimeout(5*time.Second)).(*Model)
	if !ok || final.cur != scrSequence || final.panes[scrSequence].cursor != 1 {
		t.Fatalf("final model: %+v", final)
	}
	_, _ = io.Copy(io.Discard, tm.FinalOutput(t))
}
