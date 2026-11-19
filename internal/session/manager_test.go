package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/root-ali/tstui/internal/layout"
)

func spec(host string) Spec { return Spec{Proxy: "p:443", Host: host, Login: "ali"} }

func names(m *Manager) string {
	var out []string
	for i, s := range m.Sessions() {
		mark := " "
		if i == m.ActiveIndex() {
			mark = "*"
		}
		out = append(out, mark+s.Name())
	}
	return strings.Join(out, " ")
}

// The tab state machine, step by step.
func TestManagerTabs(t *testing.T) {
	sp := &Fake{}
	m := NewManager(sp, Options{Cols: 80, Rows: 24})
	t.Cleanup(m.CloseAll)

	steps := []struct {
		name string
		do   func()
		want string
	}{
		{"empty", func() {}, ""},
		{"open web", func() { m.Open(spec("web-01"), false) }, "*web-01"},
		{"open db", func() { m.Open(spec("db-03"), false) }, " web-01 *db-03"},
		{"open web again focuses it", func() { m.Open(spec("web-01"), false) }, "*web-01  db-03"},
		{"shift-enter opens an extra tab", func() { m.Open(spec("web-01"), true) }, " web-01  db-03 *web-01#2"},
		{"activate 2", func() { m.Activate(1) }, " web-01 *db-03  web-01#2"},
		{"activate out of range", func() { m.Activate(7) }, " web-01 *db-03  web-01#2"},
		{"close active selects the next", func() { m.Close(m.Active()) }, " web-01 *web-01#2"},
		{"close last selects the previous", func() { m.Close(m.Active()) }, "*web-01"},
		{"detach", func() { m.Detach() }, " web-01"},
		{"open while detached", func() { m.Open(spec("cache-01"), false) }, " web-01 *cache-01"},
		{"close inactive keeps active", func() { m.Close(m.Sessions()[0]) }, "*cache-01"},
		{"close all tabs", func() { m.Close(m.Active()) }, ""},
	}
	for _, st := range steps {
		st.do()
		if got := names(m); got != st.want {
			t.Fatalf("%s: tabs = %q, want %q", st.name, got, st.want)
		}
	}
	if m.Active() != nil || m.ActiveIndex() != -1 {
		t.Error("empty manager has an active tab")
	}
	if n := sp.Count(); n != 4 {
		t.Errorf("spawned %d processes, want 4", n)
	}
}

func TestManagerActivityAndState(t *testing.T) {
	sp := &Fake{}
	m := NewManager(sp, Options{Cols: 80, Rows: 24})
	t.Cleanup(m.CloseAll)
	web, _, _ := m.Open(spec("web-01"), false)
	db, _, _ := m.Open(spec("db-03"), false) // active

	if st, _, _ := web.State(); st != Connecting {
		t.Fatalf("new session state %v", st)
	}
	sp.Proc(0).Output("background output\r\n")
	waitFor(t, "web connected", func() bool { st, _, _ := web.State(); return st == Connected })
	if !web.Activity() {
		t.Fatal("background tab has no activity marker")
	}
	m.Activate(m.TabOf(web))
	if web.Activity() {
		t.Fatal("activating a tab did not clear its marker")
	}
	if m.Running() != 2 {
		t.Fatalf("running = %d", m.Running())
	}

	sp.Proc(1).Exit(0)
	waitFor(t, "db disconnected", func() bool { st, _, _ := db.State(); return st == Disconnected })
	if m.Running() != 1 {
		t.Fatalf("running = %d after one exit", m.Running())
	}
	if err := db.Restart(); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := db.State(); st != Connecting {
		t.Fatalf("restarted state %v", st)
	}
	if m.Running() != 2 {
		t.Fatalf("running = %d after restart", m.Running())
	}
}

func TestManagerInputAndResize(t *testing.T) {
	sp := &Fake{}
	m := NewManager(sp, Options{Cols: 80, Rows: 24})
	t.Cleanup(m.CloseAll)
	s, _, _ := m.Open(spec("web-01"), false)
	s.Send([]byte("ls\r"))
	s.Resize(120, 40)
	p := sp.Proc(0)
	waitFor(t, "input delivered", func() bool { return string(p.Input()) == "ls\r" })
	if cols, rows := p.Size(); rows != 40 || cols != 120 {
		t.Errorf("pty size %dx%d", cols, rows)
	}
}

// A program's terminal queries are answered through the input queue.
func TestManagerTerminalReplies(t *testing.T) {
	sp := &Fake{}
	m := NewManager(sp, Options{Cols: 80, Rows: 24})
	t.Cleanup(m.CloseAll)
	m.Open(spec("web-01"), false)
	p := sp.Proc(0)
	p.Output("\x1b[6n")
	waitFor(t, "cursor position report", func() bool { return string(p.Input()) == "\x1b[1;1R" })
}

func TestManagerCloseAllWithRealProcesses(t *testing.T) {
	m := NewManager(&shSpawner{script: "sleep 1000"}, Options{Cols: 80, Rows: 24})
	for i := range 10 {
		if _, _, err := m.Open(spec("host-"+string(rune('a'+i))), false); err != nil {
			t.Fatal(err)
		}
	}
	if m.Running() != 10 {
		t.Fatalf("running %d", m.Running())
	}
	m.CloseAll()
	if m.Len() != 0 {
		t.Fatal("tabs left")
	}
}

func TestLoginMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "tstui", "logins.json")
	mem, err := LoadLogins(path)
	if err != nil {
		t.Fatal(err)
	}
	if mem.Get("p", "web") != "" {
		t.Fatal("empty memory returned a login")
	}
	if err := mem.Set("p", "web", "root"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	again, err := LoadLogins(path)
	if err != nil {
		t.Fatal(err)
	}
	if again.Get("p", "web") != "root" || again.Get("other", "web") != "" {
		t.Fatalf("reloaded %v", again.logins)
	}

	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := LoadLogins(path)
	if err == nil || broken == nil || broken.Get("p", "web") != "" {
		t.Fatalf("broken file: %v %v", broken, err)
	}

	mem, _ = LoadLogins("")
	if err := mem.Set("p", "h", "a"); err != nil || mem.Get("p", "h") != "a" {
		t.Fatal("in-memory only store failed")
	}
}

func TestDefaultLoginsPath(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if p, _ := DefaultLoginsPath(env(map[string]string{"XDG_STATE_HOME": "/x"})); p != "/x/tstui/logins.json" {
		t.Errorf("xdg: %s", p)
	}
	if p, _ := DefaultLoginsPath(env(map[string]string{"HOME": "/h", "XDG_STATE_HOME": "rel"})); p != "/h/.local/state/tstui/logins.json" {
		t.Errorf("home: %s", p)
	}
}

// panes prints the tabs as [a *b] [c]: the active tab's focus has a *,
// a zoomed tab a Z.
func panes(m *Manager) string {
	var out []string
	for i, t := range m.Tabs() {
		var ps []string
		for _, s := range t.Panes() {
			mark := ""
			if i == m.ActiveIndex() && s == t.Focus() {
				mark = "*"
			}
			ps = append(ps, mark+s.Name())
		}
		z := ""
		if t.Zoomed() {
			z = "Z"
		}
		out = append(out, z+"["+strings.Join(ps, " ")+"]")
	}
	return strings.Join(out, " ")
}

// Tabs with split panes, step by step.
func TestManagerSplits(t *testing.T) {
	sp := &Fake{}
	m := NewManager(sp, Options{Cols: 81, Rows: 25})
	t.Cleanup(m.CloseAll)
	var web, db *Session

	steps := []struct {
		name string
		do   func()
		want string
	}{
		{"split with no tab opens one", func() { web, _ = m.Split(spec("web-01"), layout.Horizontal) }, "[*web-01]"},
		{"split side by side", func() { db, _ = m.Split(spec("db-03"), layout.Horizontal) }, "[web-01 *db-03]"},
		{"split the same host again", func() { m.Split(spec("web-01"), layout.Vertical) }, "[web-01 db-03 *web-01#2]"},
		{"next pane wraps", func() { m.NextPane() }, "[*web-01 db-03 web-01#2]"},
		{"zoom", func() { m.ToggleZoom() }, "Z[*web-01 db-03 web-01#2]"},
		{"new tab", func() { m.Open(spec("cache-01"), false) }, "Z[web-01 db-03 web-01#2] [*cache-01]"},
		{"reuse finds a pane and unzooms", func() { m.Open(spec("db-03"), false) }, "[web-01 *db-03 web-01#2] [cache-01]"},
		{"focus a pane in another tab", func() { m.FocusSession(m.Tabs()[1].Panes()[0]) }, "[web-01 db-03 web-01#2] [*cache-01]"},
		{"zoom a single pane does nothing", func() { m.ToggleZoom() }, "[web-01 db-03 web-01#2] [*cache-01]"},
		{"back to tab 1 keeps its focus", func() { m.Activate(0) }, "[web-01 *db-03 web-01#2] [cache-01]"},
		{"close the focus moves to the next", func() { m.Close(db) }, "[web-01 *web-01#2] [cache-01]"},
		{"close the last pane moves to the previous", func() { m.Close(m.Active()) }, "[*web-01] [cache-01]"},
		{"close an unfocused pane", func() { m.Split(spec("db-03"), layout.Vertical); m.Close(web) }, "[*db-03] [cache-01]"},
		{"closing the tab's only pane removes it", func() { m.Close(m.Active()) }, "[*cache-01]"},
	}
	for _, st := range steps {
		st.do()
		if got := panes(m); got != st.want {
			t.Fatalf("%s: tabs = %q, want %q", st.name, got, st.want)
		}
	}
	if m.Len() != 1 || m.TabCount() != 1 {
		t.Fatalf("len %d, tabs %d", m.Len(), m.TabCount())
	}
	// A split pane starts at half the focused pane's size.
	if cols, rows := sp.Proc(1).Size(); cols != 40 || rows != 25 {
		t.Errorf("side-by-side split started at %dx%d, want 40x25", cols, rows)
	}
	if cols, rows := sp.Proc(2).Size(); cols != 40 || rows != 12 {
		t.Errorf("stacked split started at %dx%d, want 40x12", cols, rows)
	}
}
