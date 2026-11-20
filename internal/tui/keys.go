package tui

import (
	"slices"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"iormesh/internal/viewmodel"
)

// Keys (docs/visualization.md "The terminal UI"): ↑↓/jk move, pgup/pgdn
// page, g/G or home/end jump, ⏎ opens, esc goes back, tab moves the
// focus between the list and the detail (which then scrolls), / filters
// the rows, 1–7 pick a screen (s the sequence, h health), ? help, q
// quits.

// bigJump is "to the end": clamp cuts it to the last row or line.
const bigJump = 1 << 30

// handleKey applies one key press.
func (m *Model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		return tea.Quit
	}
	if m.filtering {
		m.filterKey(key, k.Text)
		return nil
	}
	if m.help { // any key closes the help; q still quits
		m.help = false
		if key == "q" {
			return tea.Quit
		}
		return nil
	}
	switch key {
	case "q":
		return tea.Quit
	case "?":
		m.help = true
	case "tab":
		m.detailFocus = !m.detailFocus
	case "enter":
		m.open()
	case "esc", "backspace":
		m.goBack()
	case "/":
		m.filtering = m.cur != scrHealth
	case "s":
		m.sequenceKey()
	case "h":
		m.switchTo(scrHealth)
	case "1", "2", "3", "4", "5", "6", "7":
		m.switchTo(screen(key[0] - '1'))
	default:
		m.move(key)
	}
	return nil
}

// filterKey edits the current screen's filter: typed text appends, ⌫
// deletes a rune, ⏎ keeps the filter, esc drops it.
func (m *Model) filterKey(key, text string) {
	p := &m.panes[m.cur]
	switch key {
	case "enter":
		m.filtering = false
	case "esc":
		m.filtering, p.filter = false, ""
	case "backspace":
		_, n := utf8.DecodeLastRuneInString(p.filter)
		p.filter = p.filter[:len(p.filter)-n]
	default:
		p.filter += text // empty for a special key
	}
	p.cursor, p.offset, p.scroll = 0, 0, 0
}

// move moves the cursor, or scrolls the detail when it has the focus
// (health is all detail).
func (m *Model) move(key string) {
	p := &m.panes[m.cur]
	target := &p.cursor
	if m.detailFocus || m.cur == scrHealth {
		target = &p.scroll
	}
	page := max(1, m.bodyHeight()/2)
	before := p.cursor
	switch key {
	case "up", "k":
		*target--
	case "down", "j":
		*target++
	case "pgup":
		*target -= page
	case "pgdown", "space":
		*target += page
	case "home", "g":
		*target = 0
	case "end", "G":
		*target = bigJump
	}
	if p.cursor != before {
		p.scroll = 0 // a new row's detail starts at its top
	}
}

// sequenceKey: on the conversations screen s opens the selected one, as
// ⏎ does; elsewhere it shows the sequence screen.
func (m *Model) sequenceKey() {
	if m.cur == scrConversations {
		m.open()
		return
	}
	m.switchTo(scrSequence)
}

// switchTo jumps to screen s from the tab keys: a fresh start, so the
// back stack and the conversations' scope are dropped.
func (m *Model) switchTo(s screen) {
	m.cur, m.back, m.scope, m.detailFocus = s, nil, scope{}, false
}

// push enters screen s from the current one (⏎), at its top.
func (m *Model) push(s screen) {
	m.back = append(m.back, m.cur)
	m.cur, m.detailFocus = s, false
	m.panes[s] = pane{}
}

// goBack returns to the screen ⏎ came from. Leaving the conversations
// for the screen that scoped them drops the scope; with nothing to go
// back to, esc drops the scope in place.
func (m *Model) goBack() {
	if m.detailFocus {
		m.detailFocus = false
		return
	}
	if len(m.back) == 0 {
		if m.cur == scrConversations && m.scope != (scope{}) {
			m.scope, m.panes[scrConversations] = scope{}, pane{}
		}
		return
	}
	m.cur, m.back = m.back[len(m.back)-1], m.back[:len(m.back)-1]
	if m.cur != scrConversations && m.cur != scrSequence {
		m.scope = scope{}
	}
}

// open is ⏎: one step further down the walk mesh → conversation → step →
// thread (and a thread back to a conversation).
func (m *Model) open() {
	i := m.selected(m.cur)
	if i < 0 {
		return
	}
	switch m.cur {
	case scrServices:
		m.push(scrConversations)
		m.scope = scope{service: m.vm.Services[i].Name}
	case scrEdges:
		m.push(scrConversations)
		m.scope = scope{from: m.vm.Edges[i].From, to: m.vm.Edges[i].To}
	case scrConversations:
		m.conv = i
		m.push(scrSequence)
	case scrSequence:
		m.openThread(i)
	case scrThreads:
		m.openStory(i)
	}
}

// openThread opens the story of step i's own thread.
func (m *Model) openThread(i int) {
	c, _ := m.conversation()
	s := c.Steps[i]
	if s.Lane < 0 || s.Lane >= len(c.Lifelines) {
		return
	}
	lane := c.Lifelines[s.Lane]
	t := slices.IndexFunc(m.vm.Threads, func(t viewmodel.ThreadStory) bool { return t.Lifeline == lane })
	if t < 0 {
		return
	}
	m.push(scrThreads)
	m.panes[scrThreads].cursor = t
}

// openStory opens the conversation of story i's newest entry, at that
// step.
func (m *Model) openStory(i int) {
	es := m.vm.Threads[i].Entries
	if len(es) == 0 {
		return
	}
	e := es[len(es)-1]
	c := slices.IndexFunc(m.vm.Conversations, func(c viewmodel.Conversation) bool { return c.ID == e.Conversation })
	if c < 0 {
		return
	}
	m.conv = c
	m.push(scrSequence)
	m.panes[scrSequence].cursor = e.Step - 1
}
