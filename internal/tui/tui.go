// Package tui is `iormeshctl tui` (PLAN.md M5; docs/visualization.md
// "The terminal UI", step V4): a Bubble Tea explorer of one build's view
// model for SSH sessions and hosts without a browser. It renders the same
// internal/viewmodel.Model that `show` prints and the site draws — no
// reading of its own, so the three surfaces cannot disagree — and reuses
// the text views' formatting (StepText, WriteSequence, WriteThreads,
// WriteProcess, WriteSummary, WriteHealth) for its rows and details.
//
// Rules it keeps (ior's TUI rules, its AGENTS.md):
//
//   - all-pointer methods on Model, and Init is side-effect free;
//   - never wider or taller than the terminal: every line is cut to the
//     width (ANSI- and wide-rune-aware), the screen is exactly the
//     terminal's height;
//   - terminal-safe: New runs the whole model through viewmodel.Safe
//     once, so every service, thread, path or frame a server chose is
//     printable before any screen sees it;
//   - deterministic: a frame is a pure function of the model, the size
//     and the keys pressed — no clock, no randomness — so tests compare
//     frames.
//
// It is cgo-free (Bubble Tea and Lip Gloss are pure Go), as iormeshctl
// must be (internal/buildgate).
package tui

import (
	tea "charm.land/bubbletea/v2"

	"iormesh/internal/viewmodel"
)

// screen is one of the explorer's screens, in tab order.
type screen int

// The screens mirror the site's (docs/visualization.md "Screens"): the
// mesh as a service table (a graph does not fit a terminal), its edges,
// the conversations, one conversation's sequence, the thread stories,
// the processes, and the run's health.
const (
	scrServices screen = iota
	scrEdges
	scrConversations
	scrSequence
	scrThreads
	scrProcesses
	scrHealth
	numScreens
)

// screenNames label the tabs; screenShort when the full names do not
// fit the width.
var (
	screenNames = [numScreens]string{"services", "edges", "conversations", "sequence", "threads",
		"processes", "health"}
	screenShort = [numScreens]string{"svc", "edg", "conv", "seq", "thr", "proc", "hlth"}
)

// pane is one screen's position: the selected row and the list's first
// visible row, the detail's scroll, and the text filter on its rows.
type pane struct {
	cursor, offset, scroll int
	filter                 string
}

// scope narrows the conversations screen to the ones touching a service,
// or with a request along one edge; the zero scope is all of them.
type scope struct {
	service, from, to string
}

// Model is the explorer's state.
type Model struct {
	vm            viewmodel.Model
	width, height int
	cur           screen
	panes         [numScreens]pane
	// back is the stack ⏎ pushes and esc pops: the walk mesh →
	// conversation → step → thread can be retraced.
	back  []screen
	scope scope
	// conv is the conversation the sequence screen shows (an index into
	// vm.Conversations).
	conv        int
	detailFocus bool
	filtering   bool
	help        bool
	st          styles
}

// New returns the explorer over m, made terminal-safe once here.
func New(m viewmodel.Model) *Model {
	return &Model{vm: viewmodel.Safe(m), st: newStyles()}
}

// Run runs the explorer on the process's terminal until the operator
// quits. The caller checks that stdin and stdout are terminals first:
// Bubble Tea would otherwise draw into a pipe.
func Run(m viewmodel.Model) error {
	_, err := tea.NewProgram(New(m)).Run()
	return err
}

// Init asks for nothing: the window size arrives as a message.
func (m *Model) Init() tea.Cmd { return nil }

// Update applies a key or a resize; everything else is ignored.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		cmd = m.handleKey(msg)
	}
	m.clamp()
	return m, cmd
}

// View draws the frame on the alternate screen, so leaving the explorer
// gives the operator's shell back as it was.
func (m *Model) View() tea.View {
	v := tea.NewView(m.Render())
	v.AltScreen = true
	return v
}
