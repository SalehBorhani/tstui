package session

import (
	"fmt"
	"sync"

	"github.com/root-ali/tstui/internal/layout"
)

// Tab is one tab: one or more sessions laid out as split panes, one of
// which has the focus. Change it only through the Manager.
type Tab struct {
	panes  *layout.Tree[*Session]
	focus  *Session
	zoomed bool
}

// Panes returns the tab's sessions in reading order.
func (t *Tab) Panes() []*Session { return t.panes.Leaves() }

// Focus is the pane that has the keyboard.
func (t *Tab) Focus() *Session { return t.focus }

// Tree is the tab's pane tree, for its geometry. It must not be changed.
func (t *Tab) Tree() *layout.Tree[*Session] { return t.panes }

// Zoomed reports whether only the focused pane is shown.
func (t *Tab) Zoomed() bool { return t.zoomed }

// Visible returns the panes drawn: all of them, or the focused one when
// zoomed.
func (t *Tab) Visible() []*Session {
	if t.zoomed {
		return []*Session{t.focus}
	}
	return t.Panes()
}

// Manager keeps the sessions as an ordered list of tabs with one active
// tab, or none when detached. Each tab holds one or more sessions as
// split panes.
//
// Its methods must be called from one goroutine (the UI goroutine); the
// sessions themselves are safe for concurrent use.
type Manager struct {
	spawner Spawner
	o       Options

	tabs   []*Tab
	active int // index into tabs, -1 when detached or empty
	nextID int

	closing sync.WaitGroup // sessions being closed in the background
}

// NewManager returns a manager whose sessions start with sp and o.
func NewManager(sp Spawner, o Options) *Manager {
	return &Manager{spawner: sp, o: o, active: -1, nextID: 1}
}

// SetSize sets the terminal size new sessions start with.
func (m *Manager) SetSize(cols, rows int) {
	if cols > 0 && rows > 0 {
		m.o.Cols, m.o.Rows = cols, rows
	}
}

// Open focuses the session for spec's server, in whatever tab or pane it
// is, if there is one and extra is false. Otherwise it adds a tab,
// activates it and starts its program. created says whether a session was
// added; a start failure is returned with the tab kept, so its banner can
// explain the error.
func (m *Manager) Open(spec Spec, extra bool) (s *Session, created bool, err error) {
	if !extra {
		for _, t := range m.Sessions() {
			if t.spec.Key() == spec.Key() {
				m.FocusSession(t)
				return t, false, nil
			}
		}
	}
	s = m.newSession(spec, m.o)
	m.tabs = append(m.tabs, &Tab{panes: layout.New(s), focus: s})
	m.Activate(len(m.tabs) - 1)
	return s, true, s.Start()
}

// Split adds a session for spec as a pane next to the active tab's
// focused pane, in direction d, focuses it and starts its program. It
// starts at half the focused pane's size. With no active tab it opens a
// new tab instead.
func (m *Manager) Split(spec Spec, d layout.Dir) (*Session, error) {
	t := m.ActiveTab()
	if t == nil {
		s, _, err := m.Open(spec, true)
		return s, err
	}
	o := m.o
	cols, rows := t.focus.Terminal().Size()
	if d == layout.Horizontal {
		cols = max((cols-1)/2, 1)
	} else {
		rows = max((rows-1)/2, 1)
	}
	o.Cols, o.Rows = cols, rows
	s := m.newSession(spec, o)
	t.panes.Split(t.focus, s, d)
	t.focus, t.zoomed = s, false
	return s, s.Start()
}

func (m *Manager) newSession(spec Spec, o Options) *Session {
	s := New(m.nextID, spec, m.spawner, o)
	m.nextID++
	if n := m.countName(s.name); n > 0 {
		s.name = fmt.Sprintf("%s#%d", s.name, n+1)
	}
	return s
}

func (m *Manager) countName(base string) int {
	n := 0
	for _, t := range m.Sessions() {
		if t.spec.label() == base {
			n++
		}
	}
	return n
}

// Sessions returns every session, tab by tab and pane by pane.
func (m *Manager) Sessions() []*Session {
	var out []*Session
	for _, t := range m.tabs {
		out = append(out, t.Panes()...)
	}
	return out
}

// Len is the number of sessions.
func (m *Manager) Len() int { return len(m.Sessions()) }

// Tabs returns the tabs in order. The slice must not be changed.
func (m *Manager) Tabs() []*Tab { return m.tabs }

// TabCount is the number of tabs.
func (m *Manager) TabCount() int { return len(m.tabs) }

// ActiveTab returns the active tab, or nil.
func (m *Manager) ActiveTab() *Tab {
	if m.active < 0 {
		return nil
	}
	return m.tabs[m.active]
}

// Active returns the active tab's focused session, or nil.
func (m *Manager) Active() *Session {
	if t := m.ActiveTab(); t != nil {
		return t.focus
	}
	return nil
}

// ActiveIndex is the active tab's index, or -1.
func (m *Manager) ActiveIndex() int { return m.active }

// Activate makes tab i active and clears the activity marker of the
// panes it shows. An index out of range changes nothing and returns
// false.
func (m *Manager) Activate(i int) bool {
	if i < 0 || i >= len(m.tabs) {
		return false
	}
	m.active = i
	for _, s := range m.tabs[i].Visible() {
		s.ClearActivity()
	}
	return true
}

// TabOf returns the index of the tab that holds s, or -1.
func (m *Manager) TabOf(s *Session) int {
	for i, t := range m.tabs {
		if t.panes.Contains(s) {
			return i
		}
	}
	return -1
}

// Index returns the position of s in Sessions, or -1.
func (m *Manager) Index(s *Session) int {
	for i, t := range m.Sessions() {
		if t == s {
			return i
		}
	}
	return -1
}

// FocusSession activates the tab that holds s and gives s the focus. A
// zoomed tab stays zoomed only when s already had the focus.
func (m *Manager) FocusSession(s *Session) bool {
	i := m.TabOf(s)
	if i < 0 {
		return false
	}
	t := m.tabs[i]
	if t.focus != s {
		t.focus, t.zoomed = s, false
	}
	m.Activate(i)
	return true
}

// NextPane moves the active tab's focus to its next pane, wrapping.
func (m *Manager) NextPane() bool {
	t := m.ActiveTab()
	if t == nil {
		return false
	}
	panes := t.Panes()
	if len(panes) < 2 {
		return false
	}
	for i, s := range panes {
		if s == t.focus {
			return m.FocusSession(panes[(i+1)%len(panes)])
		}
	}
	return false
}

// ToggleZoom shows only the active tab's focused pane, or all of them
// again. It returns false when there is nothing to zoom.
func (m *Manager) ToggleZoom() bool {
	t := m.ActiveTab()
	if t == nil || (t.panes.Len() < 2 && !t.zoomed) {
		return false
	}
	t.zoomed = !t.zoomed
	m.Activate(m.active)
	return true
}

// Detach leaves every session running with no tab active.
func (m *Manager) Detach() { m.active = -1 }

// Close removes s's pane and closes it in the background. Its tab's focus
// moves to the next pane, or the previous one when it was the last. A tab
// left with no pane is removed: the tab after it becomes active, or the
// one before when it was the last; a detached manager stays detached.
func (m *Manager) Close(s *Session) {
	i := m.TabOf(s)
	if i < 0 {
		return
	}
	t := m.tabs[i]
	panes := t.Panes()
	t.panes.Remove(s)
	if t.panes.Len() > 0 {
		if t.focus == s {
			j := indexOfSession(panes, s)
			rest := t.Panes()
			t.focus = rest[min(j, len(rest)-1)]
			t.zoomed = false
		}
		if t.panes.Len() == 1 {
			t.zoomed = false
		}
	} else {
		m.tabs = append(m.tabs[:i], m.tabs[i+1:]...)
		switch {
		case len(m.tabs) == 0:
			m.active = -1
		case m.active > i:
			m.active--
		case m.active == i:
			m.Activate(min(i, len(m.tabs)-1))
		}
	}
	m.closing.Add(1)
	go func() {
		defer m.closing.Done()
		s.Close()
	}()
}

func indexOfSession(ss []*Session, s *Session) int {
	for i, t := range ss {
		if t == s {
			return i
		}
	}
	return -1
}

// Running is the number of sessions with a live process.
func (m *Manager) Running() int {
	n := 0
	for _, t := range m.Sessions() {
		if st, _, _ := t.State(); st.Running() {
			n++
		}
	}
	return n
}

// CloseAll closes every session, in parallel, and waits for them and for
// those Close left running in the background.
func (m *Manager) CloseAll() {
	for _, t := range m.Sessions() {
		m.closing.Add(1)
		go func() {
			defer m.closing.Done()
			t.Close()
		}()
	}
	m.tabs, m.active = nil, -1
	m.closing.Wait()
}
