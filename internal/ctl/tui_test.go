package ctl

import (
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"iormesh/internal/tui"
	"iormesh/internal/viewmodel"
)

// TestTUIRefusesWithoutTerminal: without a terminal tui exits 1 with a
// message pointing at show, before it reads anything.
func TestTUIRefusesWithoutTerminal(t *testing.T) {
	f := newBuildFixture(t)
	f.e.terminal = func() bool { return false }
	f.e.tui = func(viewmodel.Model) error {
		t.Fatal("the explorer ran without a terminal")
		return nil
	}
	if code := f.run("tui"); code != exitError || !strings.Contains(f.stderr.String(), "iormeshctl show") {
		t.Fatalf("exit %d, stderr %q", code, f.stderr)
	}
	if code := f.run("tui", "extra"); code != exitUsage {
		t.Fatalf("tui with an argument: exit %d", code)
	}
}

// TestTUIWalksTheMicroservicesMesh is M5's acceptance walk in the
// terminal: tui opens the build's model.json — the model show reads —
// and the microservices scenario is walked from the mesh (orders, the
// service) to its conversation, to the rename step, down to that step's
// Java stack and the query of its raw event. The frames (100×30, the
// sequence at 160×30) are goldens.
func TestTUIWalksTheMicroservicesMesh(t *testing.T) {
	f := newMicroFixture(t, true)
	var got viewmodel.Model
	f.e.terminal = func() bool { return true }
	f.e.tui = func(m viewmodel.Model) error { got = m; return nil }
	f.mustRun(t, "tui")
	want, err := loadModel(f.e, buildFlags{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tui did not open the build's model (err %v)", err)
	}
	m := tui.New(got)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	golden(t, "tui_services.golden", frame(m))
	keys(m, "/", "o", "r", "d", "enter", "enter")       // filter to orders:8080, open its conversations
	keys(m, "enter")                                    // the checkout's sequence
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 30}) // room for the step's stack unwrapped
	for range 10 {                                      // step 11: the rename
		keys(m, "down")
	}
	seq := frame(m)
	golden(t, "tui_sequence.golden", seq)
	for _, want := range []string{"rename /data/order-42.json.tmp -> /data/order-42.json",
		"at com.shop.orders.DataStore#save(DataStore.java:42)", "iormeshctl query -ref shop/orders/"} {
		if !strings.Contains(seq, want) {
			t.Errorf("the rename step's detail lacks %q", want)
		}
	}
	keys(m, "enter") // the step's thread
	if fr := frame(m); !strings.Contains(fr, "story        received GET /orders/42 from java") {
		t.Errorf("worker-3's story:\n%s", fr)
	}
}

// frame is the explorer's frame without its styles, each line's padding
// trimmed so the golden has no trailing blanks.
func frame(m *tui.Model) string {
	lines := strings.Split(ansi.Strip(m.Render()), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

// keys presses keys; a word is a special key ("enter", "down").
func keys(m *tui.Model, ks ...string) {
	special := map[string]rune{"enter": tea.KeyEnter, "down": tea.KeyDown, "esc": tea.KeyEscape}
	for _, k := range ks {
		if r, ok := special[k]; ok {
			m.Update(tea.KeyPressMsg{Code: r})
			continue
		}
		m.Update(tea.KeyPressMsg{Code: []rune(k)[0], Text: k})
	}
}
