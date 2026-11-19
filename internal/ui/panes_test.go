package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/layout"
	"github.com/root-ali/tstui/internal/session"
)

func (h *sessionHarness) prefix(keys ...any) {
	h.t.Helper()
	h.ctrl(tcell.KeyCtrlB)
	for _, k := range keys {
		switch k := k.(type) {
		case tcell.Key:
			h.key(k)
		case string:
			h.typeText(k)
		default:
			h.t.Fatalf("bad key %v", k)
		}
	}
}

// activeHost is the focused pane's host, read on the UI goroutine.
func (h *sessionHarness) activeHost() string {
	if s := h.app.sessions.Active(); s != nil {
		return s.Spec().Host
	}
	return ""
}

func (h *sessionHarness) waitSize(i, cols, rows int) {
	h.t.Helper()
	h.eventually("pty size", func() bool {
		c, r := h.sp.Proc(i).Size()
		return c == cols && r == rows
	})
}

// Splitting through the picker, moving between panes, typing into the
// focused one only, and closing a pane.
func TestAppSplitPanes(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter) // web-01
	h.waitSpawned(1)
	// 140x40: the right column is 102 wide, 32 high under the tab bar.
	h.waitSize(0, 100, 30)

	h.prefix("|")
	h.eventually("picker", func() bool { return h.app.dialogName == "split" })
	h.waitScreen("<Split right>")
	h.typeText("db")
	h.key(tcell.KeyEnter)
	h.waitSpawned(2)
	if got := h.sp.Spec(1).Host; got != "db-03" {
		t.Fatalf("split opened %q", got)
	}
	h.eventually("db-03 focused in tab 1", func() bool {
		return h.app.sessions.TabCount() == 1 && h.app.sessions.Len() == 2 && h.activeHost() == "db-03" &&
			h.app.focus == focusTerm && h.app.dialog == nil
	})
	h.waitScreen("1:db-03 +1")
	h.waitSize(0, 49, 30)
	h.waitSize(1, 49, 30)

	go h.sp.Proc(0).Output("left-pane\r\n")
	go h.sp.Proc(1).Output("right-pane\r\n")
	h.waitScreen("left-pane")
	h.waitScreen("right-pane")

	h.typeText("x")
	h.waitInput(1, "x")
	h.prefix(tcell.KeyLeft)
	h.eventually("left pane", func() bool { return h.activeHost() == "web-01" })
	h.typeText("y")
	h.waitInput(0, "y")
	if strings.Contains(string(h.sp.Proc(0).Input()), "x") || strings.Contains(string(h.sp.Proc(1).Input()), "y") {
		t.Fatal("keys reached an unfocused pane")
	}
	h.prefix(tcell.KeyLeft) // nothing further left
	h.prefix("o")
	h.eventually("o: next pane", func() bool { return h.activeHost() == "db-03" })

	h.prefix("c")
	h.eventually("pane closed", func() bool {
		return h.app.sessions.Len() == 1 && h.app.sessions.TabCount() == 1 && h.activeHost() == "web-01" && h.app.focus == focusTerm
	})
	h.waitSize(0, 100, 30)
	if !h.sp.Proc(1).Exited() {
		t.Fatal("closed pane's process still runs")
	}

	h.prefix("-")
	h.eventually("picker", func() bool { return h.app.dialogName == "split" })
	h.key(tcell.KeyEscape)
	h.eventually("picker closed", func() bool { return h.app.dialog == nil && h.app.focus == focusTerm })
	if h.sp.Count() != 2 {
		t.Fatal("cancelled picker opened a session")
	}
}

// Fullscreen shows nothing but the terminal; it ends with prefix f, prefix
// tab, or the last session.
func TestAppFullscreen(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.waitScreen("Proxy:")

	h.prefix("f")
	h.eventually("fullscreen", func() bool { return h.app.fullscreen })
	h.waitSize(0, 140, 40)
	screen := h.text()
	for _, gone := range []string{"Proxy:", "1:web-01", "Servers", "<enter>"} {
		if strings.Contains(screen, gone) {
			t.Errorf("fullscreen still shows %q:\n%s", gone, screen)
		}
	}
	h.ctrl(tcell.KeyCtrlB)
	h.waitScreen("f exit fullscreen")
	h.typeText("f")
	h.eventually("restored", func() bool { return !h.app.fullscreen && h.app.focus == focusTerm })
	h.waitScreen("Proxy:")
	h.waitSize(0, 100, 30)

	// prefix tab leaves fullscreen for the list; F on the list comes back.
	h.prefix("f")
	h.eventually("fullscreen", func() bool { return h.app.fullscreen })
	h.prefix(tcell.KeyTab)
	h.eventually("list", func() bool { return !h.app.fullscreen && h.app.focus == focusList })
	h.typeText("F")
	h.eventually("F: fullscreen terminal", func() bool { return h.app.fullscreen && h.app.focus == focusTerm })

	// The palette works from the terminal and shows its footer.
	h.prefix(":")
	h.eventually("palette", func() bool { return h.app.paletteOpen })
	h.typeText("fs")
	h.key(tcell.KeyEnter)
	h.eventually(":fs leaves fullscreen", func() bool { return !h.app.fullscreen && !h.app.paletteOpen })

	h.prefix("f")
	h.eventually("fullscreen", func() bool { return h.app.fullscreen })
	h.prefix("c")
	h.eventually("last session closed: fullscreen off", func() bool {
		return !h.app.fullscreen && h.app.sessions.Len() == 0 && h.app.focus == focusList
	})
	h.waitScreen("Proxy:")
}

// Splits in fullscreen are separated by a divider, not borders.
func TestAppFullscreenSplit(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.prefix("f")
	h.eventually("fullscreen", func() bool { return h.app.fullscreen })
	h.prefix("|")
	h.eventually("picker", func() bool { return h.app.dialogName == "split" })
	h.typeText("db-03")
	h.key(tcell.KeyEnter)
	h.waitSpawned(2)
	h.waitSize(0, 70, 40)
	h.waitSize(1, 69, 40)
	h.eventually("divider", func() bool {
		lines := strings.Split(h.textUnlocked(), "\n")
		return len(lines) > 5 && []rune(lines[5])[70] == '│'
	})
}

// From the list, | and - split the selected server in directly; zoom
// shows one pane; Enter on an open server focuses its pane; a click
// focuses a pane.
func TestAppSplitFromListZoomAndClick(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter) // web-01
	h.waitSpawned(1)
	h.prefix(tcell.KeyTab)
	h.key(tcell.KeyDown)
	h.typeText("-") // db-03 below
	h.waitSpawned(2)
	h.eventually("split below", func() bool {
		return h.app.sessions.TabCount() == 1 && h.activeHost() == "db-03" && h.app.focus == focusTerm
	})
	h.waitSize(0, 100, 14)
	h.waitSize(1, 100, 14)

	h.prefix("z")
	h.waitScreen("1:db-03 +1 Z")
	h.waitSize(1, 100, 30)
	h.prefix(tcell.KeyUp) // leaves the zoom for the pane above
	h.eventually("up", func() bool { return h.activeHost() == "web-01" && !h.app.sessions.ActiveTab().Zoomed() })

	// Click the bottom pane.
	var bx, by int
	h.ui(func() {
		for _, s := range h.app.sessions.Sessions() {
			if s.Spec().Host == "db-03" {
				x, y, _, _ := h.app.viewFor(s).GetInnerRect()
				bx, by = x+2, y+2
			}
		}
	})
	h.screen.InjectMouse(bx, by, tcell.Button1, tcell.ModNone)
	h.screen.InjectMouse(bx, by, tcell.ButtonNone, tcell.ModNone)
	h.eventually("click focuses the pane", func() bool { return h.activeHost() == "db-03" })

	// Enter on web-01 in the list focuses its pane, no new session.
	h.prefix(tcell.KeyTab)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnter)
	h.eventually("reuse", func() bool { return h.activeHost() == "web-01" && h.app.focus == focusTerm })
	if h.sp.Count() != 2 {
		t.Fatalf("spawned %d, want 2", h.sp.Count())
	}
}

// :vsplit host and :zoom from the palette; the session picker lists panes.
func TestAppSplitCommands(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.prefix(":")
	h.eventually("palette", func() bool { return h.app.paletteOpen })
	h.typeText("vsplit web-01")
	h.key(tcell.KeyEnter)
	h.waitSpawned(2)
	h.eventually("second pane", func() bool { return h.app.sessions.Len() == 2 && h.app.sessions.TabCount() == 1 })
	if s := h.sp.Spec(1); s.Host != "web-01" {
		t.Fatalf("vsplit spec %+v", s)
	}
	h.prefix(":")
	h.eventually("palette", func() bool { return h.app.paletteOpen })
	h.typeText("zoom")
	h.key(tcell.KeyEnter)
	h.eventually("zoomed", func() bool { return h.app.sessions.ActiveTab().Zoomed() })

	h.prefix(":")
	h.eventually("palette", func() bool { return h.app.paletteOpen })
	h.typeText("sessions")
	h.key(tcell.KeyEnter)
	h.waitScreen("1.2: web-01#2")
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnter)
	h.eventually("picked pane 1.1", func() bool {
		a := h.app.sessions.Active()
		return a != nil && a.Name() == "web-01" && !h.app.sessions.ActiveTab().Zoomed()
	})

	// A pane too small to split says so.
	var ok bool
	h.ui(func() {
		s := h.app.sessions.Active()
		h.app.panes.placed = []layout.Placed[*session.Session]{{Value: s, Rect: layout.Rect{W: 30, H: 10}}}
		ok = h.app.canSplit(layout.Horizontal)
	})
	if ok {
		t.Fatal("a 30-column pane can be split side by side")
	}
	h.waitScreen("Pane too small to split.")
}
