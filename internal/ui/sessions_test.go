package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/session"
	"github.com/root-ali/tstui/internal/tsh"
)

// sessionHarness is the app with a logged-in proxy, two servers and a
// fake session spawner.
type sessionHarness struct {
	*appHarness
	sp *session.Fake
}

func newSessionHarness(t *testing.T, logins []string, o Options) *sessionHarness {
	t.Helper()
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	st := validStatus(profileUser)
	st.Logins = logins
	fake.SetStatus(profileProxy, st)
	sp := &session.Fake{}
	o.Spawner = sp
	h := &sessionHarness{appHarness: newAppHarness(t, fake, o), sp: sp}
	h.eventually("server list", func() bool { return h.app.serversLoaded })
	// The size set before Run does not survive tview's start; tview reads
	// the screen size on every draw, so set it now.
	h.screen.SetSize(140, 40)
	h.app.tv.QueueUpdateDraw(func() {})
	return h
}

func (h *sessionHarness) waitSpawned(n int) {
	h.t.Helper()
	h.eventually("session spawned", func() bool { return h.sp.Count() >= n })
}

func (h *sessionHarness) waitScreen(want string) {
	h.t.Helper()
	h.eventually("screen shows "+want, func() bool { return strings.Contains(h.textUnlocked(), want) })
}

func (h *sessionHarness) waitInput(i int, want string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(string(h.sp.Proc(i).Input()), want) {
		if time.Now().After(deadline) {
			h.t.Fatalf("input %q lacks %q", h.sp.Proc(i).Input(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *sessionHarness) ctrl(k tcell.Key) { h.screen.InjectKey(k, 0, tcell.ModCtrl) }

func (h *sessionHarness) alt(r rune) { h.screen.InjectKey(tcell.KeyRune, r, tcell.ModAlt) }

func (h *sessionHarness) focused() focusArea {
	var f focusArea
	h.ui(func() { f = h.app.focus })
	return f
}

// Enter opens a session with the only login, the terminal gets the keys
// (Ctrl+C included) and shows the output; the prefix moves focus back.
func TestAppSessionOpenAndType(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	if spec := h.sp.Spec(0); spec.Host != "web-01" || spec.Login != "ali" || spec.Proxy != profileProxy {
		t.Fatalf("spec %+v", spec)
	}
	if h.focused() != focusTerm {
		t.Fatal("terminal not focused after connect")
	}
	go h.sp.Proc(0).Output("\x1b[32mali@web-01\x1b[0m:~$ ")
	h.waitScreen("ali@web-01:~$")
	h.waitScreen("1:web-01")
	h.waitScreen("Sessions: 1")

	h.typeText("ls")
	h.key(tcell.KeyEnter)
	h.ctrl(tcell.KeyCtrlC) // goes to the program, does not quit
	h.ctrl(tcell.KeyCtrlB) // prefix …
	h.ctrl(tcell.KeyCtrlB) // … twice sends one literal Ctrl+B
	h.waitInput(0, "ls\r\x03\x02")

	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.eventually("focus on the list", func() bool { return h.app.focus == focusList })
	h.key(tcell.KeyTab)
	h.eventually("tab focuses the terminal again", func() bool { return h.app.focus == focusTerm })
}

// Several logins: the picker asks, and remembers the choice per server.
func TestAppLoginPicker(t *testing.T) {
	h := newSessionHarness(t, []string{"ali", "root", "deploy"}, Options{})
	h.key(tcell.KeyEnter)
	h.eventually("login picker", func() bool { return h.app.dialog != nil })
	h.waitScreen("<Login as> web-01")
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	if got := h.sp.Spec(0).Login; got != "root" {
		t.Fatalf("login %q", got)
	}
	h.ui(func() {
		if got := h.app.o.Logins.Get(profileProxy, "web-01"); got != "root" {
			t.Errorf("remembered %q", got)
		}
	})

	h.ctrl(tcell.KeyCtrlB)
	h.typeText("c") // close the tab
	h.eventually("tab closed", func() bool { return h.app.sessions.Len() == 0 && h.app.focus == focusList })
	h.key(tcell.KeyEnter)
	h.eventually("picker again", func() bool { return h.app.dialog != nil })
	h.waitScreen("<Login as>")
	h.key(tcell.KeyEnter)
	h.waitSpawned(2)
	if got := h.sp.Spec(1).Login; got != "root" {
		t.Fatalf("last choice not preselected: %q", got)
	}
}

// A click moves focus as well as the keyboard does.
func TestAppMouseFocus(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	var lx, ly, tx, ty int
	h.ui(func() {
		x, y, _, _ := h.app.list.table.GetInnerRect()
		lx, ly = x+2, y+2
		x, y, _, _ = h.app.activeView().GetInnerRect()
		tx, ty = x+2, y+2
	})
	h.screen.InjectMouse(lx, ly, tcell.Button1, tcell.ModNone)
	h.screen.InjectMouse(lx, ly, tcell.ButtonNone, tcell.ModNone)
	h.eventually("click on the list focuses it", func() bool { return h.app.focus == focusList })
	h.typeText("q") // a list key now: the quit dialog, not input for the shell
	h.eventually("quit dialog", func() bool { return h.app.dialog != nil })
	h.key(tcell.KeyEscape)
	h.screen.InjectMouse(tx, ty, tcell.Button1, tcell.ModNone)
	h.screen.InjectMouse(tx, ty, tcell.ButtonNone, tcell.ModNone)
	h.eventually("click on the terminal focuses it", func() bool { return h.app.focus == focusTerm })
	if strings.Contains(string(h.sp.Proc(0).Input()), "q") {
		t.Error("q reached the shell")
	}
}

func TestAppNoLogins(t *testing.T) {
	h := newSessionHarness(t, nil, Options{})
	h.key(tcell.KeyEnter)
	h.waitScreen("Your roles allow no SSH logins")
	if h.sp.Count() != 0 {
		t.Fatal("session opened without a login")
	}
}

// The tab state machine through the keyboard.
func TestAppTabs(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter) // web-01
	h.waitSpawned(1)
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter) // db-03
	h.waitSpawned(2)
	h.waitScreen("2:db-03")
	h.eventually("db-03 active", func() bool { return h.app.sessions.ActiveIndex() == 1 })

	h.alt('1')
	h.eventually("alt-1", func() bool { return h.app.sessions.ActiveIndex() == 0 })
	h.ctrl(tcell.KeyCtrlB)
	h.typeText("2")
	h.eventually("prefix 2", func() bool { return h.app.sessions.ActiveIndex() == 1 })

	// Background output marks the tab.
	go h.sp.Proc(0).Output("news\r\n")
	h.waitScreen("1:web-01 ●*")

	// Enter on a server that has a session focuses it; n opens another.
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyUp)
	h.key(tcell.KeyEnter)
	h.eventually("web-01 focused, no new session", func() bool {
		return h.app.sessions.ActiveIndex() == 0 && h.app.focus == focusTerm && h.sp.Count() == 2
	})
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.typeText("n")
	h.waitSpawned(3)
	h.waitScreen("3:web-01#2")

	h.ctrl(tcell.KeyCtrlB)
	h.typeText("d")
	h.eventually("detached", func() bool { return h.app.sessions.Active() == nil && h.app.focus == focusList })
	if h.sp.Proc(0).Exited() || h.sp.Proc(1).Exited() {
		t.Fatal("detach ended a session")
	}
}

// A session ends: banner, r reconnects, c closes and hides the pane.
func TestAppDisconnectBanner(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.sp.Proc(0).Exit(0)
	h.waitScreen("Session closed (exit 0). r reconnect, c close")
	h.waitScreen("1:web-01 ○")
	h.typeText("r")
	h.waitSpawned(2)
	h.waitScreen("reconnecting")
	h.sp.Proc(1).Exit(130)
	h.waitScreen("Session closed (exit 130)")
	h.typeText("c")
	h.eventually("closed", func() bool { return h.app.sessions.Len() == 0 && h.app.focus == focusList && !h.app.termShown })
}

func TestAppSpawnFailureBanner(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.ui(func() { h.sp.Err = tsh.ErrNotFound })
	h.key(tcell.KeyEnter)
	h.waitScreen("✘ the session failed (tsh binary not found)")
	h.waitScreen("1:web-01 !")
}

func TestAppAutoReconnect(t *testing.T) {
	cfg := config.Default()
	cfg.Terminal.AutoReconnect = true
	h := newSessionHarness(t, []string{"ali"}, Options{Config: cfg})
	h.ui(func() { h.app.reconnectBase = 200 * time.Millisecond })
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.sp.Proc(0).Output("up\r\n")
	h.eventually("connected", func() bool { st, _, _ := h.app.sessions.Active().State(); return st == session.Connected })
	h.sp.Proc(0).Exit(255)
	h.waitScreen("Connection lost (exit 255). Reconnecting in")
	h.waitSpawned(2)

	// A clean exit is not retried.
	h.sp.Proc(1).Output("up\r\n")
	h.sp.Proc(1).Exit(0)
	h.waitScreen("Session closed (exit 0)")
	time.Sleep(400 * time.Millisecond)
	if h.sp.Count() != 2 {
		t.Fatal("clean exit was retried")
	}
}

func TestAppQuitConfirmation(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.typeText("q")
	h.waitScreen("1 session is still open.")
	h.key(tcell.KeyEscape)
	h.eventually("dialog gone", func() bool { return h.app.dialog == nil })
	select {
	case <-h.runErr:
		t.Fatal("app quit after cancel")
	default:
	}
	h.typeText("q")
	h.waitScreen("Quit tstui and close them?")
	h.key(tcell.KeyEnter) // Quit is the first button
	select {
	case err := <-h.runErr:
		h.runErr <- err
	case <-time.After(5 * time.Second):
		t.Fatal("app did not quit")
	}
	if !h.sp.Proc(0).Exited() {
		t.Error("session left running after quit")
	}
}

// The certificate expires with a session open: nothing is killed, no
// dialog pops over the terminal, the header warns; Enter on a server then
// asks for a login and opens the session after it.
func TestAppCertExpiryKeepsSessions(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{WatchInterval: 50 * time.Millisecond})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	st := expiredStatus(profileUser)
	st.Logins = []string{"ali"}
	h.fake.SetStatus(profileProxy, st)
	h.eventually("header warns, sessions kept", func() bool {
		return strings.Contains(h.app.header.left.GetText(true), "expired · sessions kept")
	})
	h.ui(func() {
		if h.modalOpen() {
			t.Error("login dialog opened over the terminal")
		}
	})
	if h.sp.Proc(0).Exited() {
		t.Fatal("expiry killed the session")
	}
	h.ctrl(tcell.KeyCtrlB)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyDown)
	h.key(tcell.KeyEnter) // db-03 while expired
	h.eventually("login dialog", h.modalOpen)
	h.typeText("pw")
	h.key(tcell.KeyEnter) // harness Login makes the status valid again
	h.waitSpawned(2)
	if got := h.sp.Spec(1).Host; got != "db-03" {
		t.Fatalf("after login opened %q", got)
	}
}

func TestAppPasteIntoTerminal(t *testing.T) {
	h := newSessionHarness(t, []string{"ali"}, Options{})
	h.key(tcell.KeyEnter)
	h.waitSpawned(1)
	h.sp.Proc(0).Output("\x1b[?2004h")
	h.eventually("bracketed paste on", func() bool { return h.app.sessions.Active().Terminal().Modes().BracketedPaste })
	_ = h.screen.PostEvent(tcell.NewEventPaste(true))
	h.typeText("uptime")
	h.key(tcell.KeyEnter)
	_ = h.screen.PostEvent(tcell.NewEventPaste(false))
	h.waitInput(0, "\x1b[200~uptime\r\x1b[201~")
}

func TestAppDemoShell(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	sp := &session.Fake{}
	h := newAppHarness(t, fake, Options{Spawner: sp, DemoShell: true})
	h.eventually("demo tab", func() bool { return sp.Count() == 1 })
	h.eventually("demo tab on screen", func() bool { return strings.Contains(h.textUnlocked(), "1:demo") })
	// Enter on a server opens another local shell, no login asked.
	h.eventually("servers", func() bool { return h.app.serversLoaded })
	h.screen.InjectKey(tcell.KeyCtrlB, 0, tcell.ModCtrl)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.eventually("second shell", func() bool { return sp.Count() == 2 })
}
