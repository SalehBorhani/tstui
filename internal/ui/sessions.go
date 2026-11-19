package ui

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/layout"
	"github.com/root-ali/tstui/internal/session"
	"github.com/root-ali/tstui/internal/tsh"
	"github.com/root-ali/tstui/internal/vterm"
)

// focusArea is the pane that has the keyboard.
type focusArea int

const (
	focusList focusArea = iota
	focusTerm
)

// prefixKey is the tmux-style key that, pressed in the terminal, makes the
// next key a tstui command (config keybindings.focus, "Ctrl-B" by default).
type prefixKey struct {
	ctrl  bool
	r     rune      // lower case
	key   tcell.Key // KeyCtrlA..KeyCtrlZ when ctrl
	label string
}

// parsePrefix reads "Ctrl-B" or "Alt-X"; anything else gives Ctrl-B.
// config.Validate has already checked the format.
func parsePrefix(spec string) prefixKey {
	mod, key, ok := strings.Cut(spec, "-")
	if !ok || len(key) != 1 || !unicode.IsLetter(rune(key[0])) {
		return prefixKey{ctrl: true, r: 'b', key: tcell.KeyCtrlB, label: "ctrl-b"}
	}
	r := unicode.ToLower(rune(key[0]))
	ctrl := strings.EqualFold(mod, "ctrl")
	if ctrl && (r < 'a' || r > 'z') {
		return prefixKey{ctrl: true, r: 'b', key: tcell.KeyCtrlB, label: "ctrl-b"}
	}
	p := prefixKey{ctrl: ctrl, r: r, label: strings.ToLower(mod) + "-" + string(r)}
	for k := tcell.KeyCtrlA; k <= tcell.KeyCtrlZ; k++ {
		if rune(k-tcell.KeyCtrlA)+'a' == r {
			p.key = k
		}
	}
	return p
}

func (p prefixKey) match(ev *tcell.EventKey) bool {
	if p.ctrl {
		return ev.Key() == p.key
	}
	return ev.Key() == tcell.KeyRune && ev.Modifiers()&tcell.ModAlt != 0 && unicode.ToLower(ev.Rune()) == p.r
}

// event is the key itself, to send it to the program literally.
func (p prefixKey) event() *tcell.EventKey {
	if p.ctrl {
		return tcell.NewEventKey(p.key, p.r, tcell.ModCtrl)
	}
	return tcell.NewEventKey(tcell.KeyRune, p.r, tcell.ModAlt)
}

// altDigit returns n for Alt+1..9.
func altDigit(ev *tcell.EventKey) (int, bool) {
	if ev.Key() == tcell.KeyRune && ev.Modifiers()&tcell.ModAlt != 0 && ev.Rune() >= '1' && ev.Rune() <= '9' {
		return int(ev.Rune() - '0'), true
	}
	return 0, false
}

// Auto-reconnect: delays double from reconnectBase up to reconnectMax, at
// most reconnectTries times in a row. A session that stayed connected for
// reconnectStable starts counting again.
const (
	reconnectBase   = 2 * time.Second
	reconnectMax    = 30 * time.Second
	reconnectTries  = 5
	reconnectStable = 30 * time.Second
)

// sessionUI is the per-session state the UI keeps.
type sessionUI struct {
	last      session.State
	tries     int
	timer     *time.Timer
	reconnect time.Time // when the scheduled reconnect fires
}

// tabBar is the line above the terminal: [1:web-01 ●] [2:db-03 ○*] …
type tabBar struct {
	theme Theme
	view  *tview.TextView
}

func newTabBar(theme Theme) *tabBar {
	v := tview.NewTextView().SetDynamicColors(true).SetRegions(true).SetWrap(false)
	return &tabBar{theme: theme, view: v}
}

// tabDot is the state marker: ● connected, ◌ connecting, ○ disconnected,
// ! error.
func (b *tabBar) tabDot(st session.State) string {
	switch st {
	case session.Connected:
		return fmt.Sprintf("[%s]●", tag(b.theme.OK))
	case session.Connecting:
		return fmt.Sprintf("[%s]◌", tag(b.theme.Warn))
	case session.Failed:
		return fmt.Sprintf("[%s]!", tag(b.theme.Err))
	}
	return fmt.Sprintf("[%s]○", tag(b.theme.DialogPlaceholder))
}

func (b *tabBar) render(m *session.Manager) {
	var sb strings.Builder
	for i, t := range m.Tabs() {
		s := t.Focus()
		name := tview.Escape(s.Name())
		panes := t.Panes()
		if len(panes) > 1 {
			name += fmt.Sprintf(" +%d", len(panes)-1)
		}
		if t.Zoomed() {
			name += " Z"
		}
		color := b.theme.InfoValue
		activity := ""
		if i != m.ActiveIndex() && tabActivity(t) {
			color = tcell.ColorYellow
			activity = fmt.Sprintf("[%s::b]*", tag(tcell.ColorYellow))
		}
		fmt.Fprintf(&sb, `["t%d"][%s::b]%d:[%s::-]%s %s%s[-::-][""] `, i, tag(b.theme.Title), i+1, tag(color), name, b.tabDot(tabState(t)), activity)
	}
	if m.Len() == 0 {
		fmt.Fprintf(&sb, "[%s]no sessions · <enter> on a server opens one", tag(b.theme.DialogPlaceholder))
	}
	b.view.SetText(sb.String())
	if i := m.ActiveIndex(); i >= 0 {
		b.view.Highlight(fmt.Sprintf("t%d", i)).ScrollToHighlight()
	} else {
		b.view.Highlight()
	}
}

// tabActivity reports whether any of t's panes has unseen output.
func tabActivity(t *session.Tab) bool {
	for _, s := range t.Panes() {
		if s.Activity() {
			return true
		}
	}
	return false
}

// tabState is the worst state among t's panes: failed, then
// disconnected, then connecting, then connected.
func tabState(t *session.Tab) session.State {
	rank := map[session.State]int{session.Failed: 3, session.Disconnected: 2, session.Connecting: 1, session.Connected: 0}
	worst := session.Connected
	for _, s := range t.Panes() {
		if st, _, _ := s.State(); rank[st] > rank[worst] {
			worst = st
		}
	}
	return worst
}

// listWidth is the server list's width when the terminal pane is shown.
const listWidth = 38

// --- connecting ---

// connect opens a session to the selected server: mode modeReuse focuses
// the one it already has, modeNewTab (shift+Enter or n) always opens one.
func (a *App) connect(mode openMode) {
	if srv, ok := a.list.selected(); ok {
		a.connectTo(srv, mode)
	}
}

func (a *App) connectTo(srv tsh.Server, mode openMode) {
	proxy := a.info.Proxy
	spec := session.Spec{Proxy: proxy, Cluster: srv.Cluster, Host: srv.Hostname}
	if mode == modeReuse {
		for _, s := range a.sessions.Sessions() {
			if s.Spec().Key() == spec.Key() {
				a.focusPane(s)
				return
			}
		}
	}
	if a.o.DemoShell {
		spec.Login = "demo"
		a.openSession(spec, mode)
		return
	}
	if a.info.State == auth.CertExpired || a.info.State == auth.CertLoggedOut || time.Until(a.info.ValidUntil) <= 0 {
		// Log in first, then come back here.
		a.pendingConnect = func() { a.connectTo(srv, mode) }
		a.showLogin("Log in to open a session to " + srv.Hostname + ".")
		return
	}
	logins := a.status.Logins
	switch len(logins) {
	case 0:
		a.flash("Your roles allow no SSH logins on this proxy.", a.theme.Err)
	case 1:
		spec.Login = logins[0]
		a.openSession(spec, mode)
	default:
		last := a.o.Logins.Get(proxy, srv.Hostname)
		a.pickLogin(srv.Hostname, logins, last, func(login string) {
			if err := a.o.Logins.Set(proxy, srv.Hostname, login); err != nil {
				a.logger.Warn("remember login", "err", err)
			}
			spec.Login = login
			a.openSession(spec, mode)
		})
	}
}

func (a *App) openSession(spec session.Spec, mode openMode) {
	var s *session.Session
	var err error
	created := true
	if mode.split() {
		s, err = a.sessions.Split(spec, mode.dir())
	} else {
		s, created, err = a.sessions.Open(spec, mode == modeNewTab)
	}
	if created {
		a.sessUI[s] = &sessionUI{}
	}
	if err != nil {
		a.logger.Info("session failed to start", "host", spec.Host, "err", err)
	}
	a.sessionsChanged()
	a.setFocus(focusTerm)
}

// pickLogin asks which login to use, with last preselected.
func (a *App) pickLogin(host string, logins []string, last string, pick func(string)) {
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true).
		SetMainTextColor(a.theme.DialogField).
		SetSelectedTextColor(a.theme.TableCursorFg).
		SetSelectedBackgroundColor(a.theme.TableCursorBg)
	list.SetBackgroundColor(a.theme.Bg)
	for i, l := range logins {
		list.AddItem(tview.Escape(l), "", 0, nil)
		if l == last {
			list.SetCurrentItem(i)
		}
	}
	closePicker := func() {
		a.removePage("picker")
		a.dialog = nil
		a.setFocus(a.focus)
	}
	list.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		closePicker()
		pick(logins[i])
	})
	list.SetDoneFunc(closePicker)
	frame := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(list, 0, 1, true)
	frame.SetBorder(true).SetBorderColor(a.theme.BorderFocus).SetTitleColor(a.theme.Title).
		SetTitle(" <Login as> "+tview.Escape(host)+" ").SetBorderPadding(0, 0, 1, 1).
		SetBackgroundColor(a.theme.Bg)
	height := min(len(logins), 10) + 2
	a.dialog = frame
	a.pages.AddPage("picker", newModalLayout(frame, a.theme.Bg, 30, height), false, true)
	a.tv.SetFocus(list)
}

// --- keys ---

// termKey handles a key while the terminal has focus: the prefix, Alt+n,
// and everything else for the terminal itself.
func (a *App) termKey(ev *tcell.EventKey) *tcell.EventKey {
	if a.prefixPending {
		a.prefixPending = false
		a.prefixCommand(ev)
		a.updateFooter()
		return nil
	}
	if a.prefix.match(ev) {
		a.prefixPending = true
		a.updateFooter()
		return nil
	}
	if n, ok := altDigit(ev); ok {
		a.activateTab(n - 1)
		return nil
	}
	v := a.activeView()
	if v.Session() == nil {
		if ev.Key() == tcell.KeyTab || ev.Key() == tcell.KeyEsc {
			a.setFocus(focusList)
		}
		return nil
	}
	wasScrolling := v.Scrolling()
	v.HandleKey(ev, a.reconnectActive, a.closeActive)
	if wasScrolling != v.Scrolling() {
		a.updateFooter()
	}
	return nil // never let tview see it: Ctrl+C would stop the app
}

// prefixCommand runs the tstui command after the prefix key.
func (a *App) prefixCommand(ev *tcell.EventKey) {
	if a.prefix.match(ev) {
		if s := a.sessions.Active(); s != nil {
			s.Send(vterm.EncodeKey(a.prefix.event(), s.Terminal().Modes()))
		}
		return
	}
	switch ev.Key() {
	case tcell.KeyTab, tcell.KeyBacktab:
		a.setFullscreen(false)
		a.setFocus(focusList)
		return
	case tcell.KeyLeft:
		a.focusDir(layout.Left)
		return
	case tcell.KeyRight:
		a.focusDir(layout.Right)
		return
	case tcell.KeyUp:
		a.focusDir(layout.Up)
		return
	case tcell.KeyDown:
		a.focusDir(layout.Down)
		return
	}
	switch ev.Rune() {
	case '1', '2', '3', '4', '5', '6', '7', '8', '9':
		a.activateTab(int(ev.Rune() - '1'))
	case 'n':
		a.nextTab(1)
	case 'p':
		a.nextTab(-1)
	case 'o':
		if a.sessions.NextPane() {
			a.sessionsChanged()
			a.setFocus(focusTerm)
		}
	case '|', '%':
		a.splitPicker(layout.Horizontal)
	case '-', '"':
		a.splitPicker(layout.Vertical)
	case 'z':
		if a.sessions.ToggleZoom() {
			a.sessionsChanged()
		}
	case 'f':
		a.toggleFullscreen()
	case 'c':
		a.closeActive()
	case 'd':
		a.sessions.Detach()
		a.setFullscreen(false)
		a.sessionsChanged()
		a.setFocus(focusList)
	case 'r':
		a.reconnectActive()
	case '[':
		a.activeView().EnterScroll()
	case 'l':
		a.showLogin("Log in to Teleport.")
	case ':':
		a.openPalette("")
	case '?':
		a.openHelp()
	case 'q':
		a.requestQuit()
	}
}

// activateTab shows tab i (0-based) and focuses the terminal.
func (a *App) activateTab(i int) {
	if a.sessions.Activate(i) {
		a.sessionsChanged()
		a.setFocus(focusTerm)
	}
}

func (a *App) closeActive() {
	s := a.sessions.Active()
	if s == nil {
		return
	}
	a.forget(s)
	a.sessions.Close(s)
	a.sessionsChanged()
	if a.sessions.Active() == nil {
		a.setFocus(focusList)
	}
}

func (a *App) forget(s *session.Session) {
	if u := a.sessUI[s]; u != nil && u.timer != nil {
		u.timer.Stop()
	}
	delete(a.sessUI, s)
	delete(a.views, s)
}

// reconnectActive restarts the active session: at once if it ended,
// after killing it if it still runs (a hung connection).
func (a *App) reconnectActive() {
	s := a.sessions.Active()
	if s == nil {
		return
	}
	if u := a.sessUI[s]; u != nil {
		if u.timer != nil {
			u.timer.Stop()
		}
		u.timer, u.reconnect, u.tries = nil, time.Time{}, 0
	}
	if err := s.Reconnect(); err != nil && !errors.Is(err, session.ErrRunning) {
		a.logger.Info("reconnect failed", "host", s.Spec().Host, "err", err)
	}
	a.sessionsChanged()
}

// --- focus, footer, quit ---

// setFocus moves the keyboard to f. While a dialog is open it only
// records where focus goes back to. A detached terminal re-attaches the
// first tab.
func (a *App) setFocus(f focusArea) {
	if f == focusTerm && a.sessions.Len() == 0 {
		f = focusList
	}
	if f == focusTerm && a.sessions.Active() == nil {
		a.sessions.Activate(0)
		a.sessionsChanged()
	}
	if a.dialog != nil || a.modal != nil {
		a.focus = f
		a.updateFooter()
		return
	}
	if f == focusTerm {
		a.tv.SetFocus(a.panes) // the focused pane; calls focusChanged
	} else {
		a.tv.SetFocus(a.list.table)
	}
	a.focusChanged(f)
}

// focusChanged follows tview's focus, which a mouse click can also move:
// it keeps the key routing, the border colours and the footer in step.
func (a *App) focusChanged(f focusArea) {
	if a.focus != f {
		a.prefixPending = false
	}
	a.focus = f
	if f == focusTerm {
		a.list.table.SetBorderColor(a.theme.Border)
	} else {
		a.list.table.SetBorderColor(a.theme.BorderFocus)
	}
	a.paintPanes()
	a.updateFooter()
}

func (a *App) updateFooter() {
	p := a.prefix.label
	switch {
	case a.paletteOpen && (a.flashText == "" || !time.Now().Before(a.flashUntil)):
		a.paletteHint()
	case a.flashText != "" && time.Now().Before(a.flashUntil):
		a.footer.view.SetText(fmt.Sprintf("[%s::b]%s", tag(a.flashColor), tview.Escape(a.flashText)))
	case a.focus == focusTerm && a.prefixPending:
		a.footer.set(hint{p + " …", "then:"}, hint{"tab", "Servers"}, hint{"1-9 n/p", "Tab"}, hint{"|/-", "Split"},
			hint{"←↑↓→ o", "Pane"}, hint{"z", "Zoom"}, hint{"f", "Fullscreen"}, hint{"c", "Close"},
			hint{"d", "Detach"}, hint{"r", "Reconnect"}, hint{"[", "Scroll"}, hint{":", "Cmd"}, hint{"l", "Login"},
			hint{"q", "Quit"}, hint{p, "Send " + p})
	case a.focus == focusTerm && a.activeView().Scrolling():
		a.footer.set(hint{"pgup/pgdn", "Page"}, hint{"↑↓ j/k", "Line"}, hint{"g/G", "Top/Bottom"},
			hint{"drag", "Copy"}, hint{"q", "Exit scroll"})
	case a.focus == focusTerm && a.sessions.Active() != nil && !a.activeRunning():
		a.footer.set(hint{"r", "Reconnect"}, hint{"c", "Close"}, hint{p + " tab", "Servers"}, hint{"alt-1..9", "Tab"})
	case a.focus == focusTerm:
		a.footer.set(hint{p + " tab", "Servers"}, hint{p + " 1-9", "Tab"}, hint{p + " |/-", "Split"},
			hint{p + " ←↑↓→", "Pane"}, hint{p + " f", "Fullscreen"}, hint{p + " c", "Close"},
			hint{p + " d", "Detach"}, hint{p + " [", "Scroll"}, hint{p + " " + p, "Send " + p})
	case a.dialog != nil:
		a.footer.set(hint{"esc", "Close"}, hint{"tab", "Next field"}, hint{"enter", "Select"})
	default:
		hints := []hint{{"enter", "Connect"}}
		if a.sessions.Len() > 0 {
			hints = append(hints, hint{"tab", "Terminal"}, hint{"|/-", "Split"}, hint{"F", "Fullscreen"}, hint{"alt-1..9", "Tab"})
		}
		hints = append(hints, hint{"/", "Filter"}, hint{":", "Cmd"}, hint{"space", "Mark"}, hint{"f", "Fav"}, hint{"N/A/C", "Sort"},
			hint{"u/d", "Copy"}, hint{"0-9", "Cluster"}, hint{"p", "Proxies"}, hint{"l", "Login"}, hint{"r", "Refresh"}, hint{"?", "Help"}, hint{"q", "Quit"})
		a.footer.set(hints...)
	}
}

// flashFor is how long a footer message stays.
const flashFor = 4 * time.Second

// flash shows a message in the footer for a few seconds.
func (a *App) flash(text string, color tcell.Color) {
	a.flashText, a.flashColor, a.flashUntil = text, color, time.Now().Add(flashFor)
	a.updateFooter()
	a.stopFlash()
	a.flashTimer = time.AfterFunc(flashFor+50*time.Millisecond, func() { a.postNoWait(a.updateFooter) })
}

func (a *App) stopFlash() {
	if a.flashTimer != nil {
		a.flashTimer.Stop()
		a.flashTimer = nil
	}
}

func (a *App) activeRunning() bool {
	if s := a.sessions.Active(); s != nil {
		st, _, _ := s.State()
		return st.Running()
	}
	return false
}

// requestQuit quits, asking first when sessions are still running.
func (a *App) requestQuit() {
	n, fwds := a.sessions.Running(), a.pf.Running()
	if n+fwds == 0 || a.quitDialog {
		a.shutdown()
		return
	}
	// The quit question replaces whatever dialog is open.
	if a.paletteOpen {
		a.closePalette()
	} else if a.dialogName != "" {
		a.closeDialog(a.dialogName)
	}
	a.quitDialog = true
	var what []string
	if n > 0 {
		what = append(what, plural(n, "session"))
	}
	if fwds > 0 {
		what = append(what, plural(fwds, "port forward"))
	}
	verb := "is"
	if n+fwds > 1 {
		verb = "are"
	}
	m := tview.NewModal().
		SetText(fmt.Sprintf("%s %s still open.\nQuit tstui and close them?", strings.Join(what, " and "), verb)).
		AddButtons([]string{"Quit", "Cancel"}).
		SetTextColor(a.theme.DialogFg).
		SetBackgroundColor(a.theme.Bg).
		SetButtonStyle(tcell.StyleDefault.Foreground(a.theme.ButtonFg).Background(a.theme.ButtonBg)).
		SetButtonActivatedStyle(tcell.StyleDefault.Foreground(a.theme.ButtonFocusFg).Background(a.theme.ButtonFocusBg)).
		SetDoneFunc(func(_ int, label string) {
			if label == "Quit" {
				a.shutdown()
				return
			}
			a.removePage("quit")
			a.dialog, a.quitDialog = nil, false
			a.setFocus(a.focus)
		})
	m.SetBorderColor(a.theme.BorderFocus).SetTitle(" <Quit> ").SetTitleColor(a.theme.Title)
	a.dialog = m
	a.pages.AddPage("quit", m, false, true)
	a.tv.SetFocus(m)
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// --- session changes ---

// sessionsChanged redraws what depends on the sessions and reacts to
// state changes (auto-reconnect). It runs on the UI goroutine.
func (a *App) sessionsChanged() {
	for _, s := range a.sessions.Sessions() {
		u := a.sessUI[s]
		if u == nil {
			u = &sessionUI{}
			a.sessUI[s] = u
		}
		st, _, err := s.State()
		if st != u.last {
			a.onSessionState(s, u, u.last, st, err)
			u.last = st
		}
	}
	if a.sessions.Len() == 0 {
		a.setFullscreen(false)
	}
	a.tabs.render(a.sessions)
	a.layoutPanes()
	a.paintPanes()
	// The focused pane may have changed: give it tview's focus too.
	if a.focus == focusTerm && a.dialog == nil && a.modal == nil && a.tv.GetFocus() != a.activeView() {
		a.tv.SetFocus(a.panes)
	}
	open := map[string]bool{}
	for _, s := range a.sessions.Sessions() {
		open[serverID(s.Spec().Cluster, s.Spec().Host)] = true
	}
	a.list.setOpen(open)
	a.info.Sessions = a.sessions.Len()
	a.renderHeader()
	a.updateFooter()
}

func (a *App) onSessionState(s *session.Session, u *sessionUI, from, to session.State, err error) {
	// Redraws are coalesced, so a short Connected state may never be seen
	// here: what counts is that the process that just ended had connected.
	if !from.Running() || to.Running() || !a.o.Config.Terminal.AutoReconnect || s.ConnectedFor() == 0 {
		return
	}
	_, code, _ := s.State()
	retry := (to == session.Disconnected && code != 0) || (to == session.Failed && errors.Is(err, session.ErrUnreachable))
	if !retry {
		return
	}
	if s.ConnectedFor() >= reconnectStable {
		u.tries = 0
	}
	if u.tries >= reconnectTries {
		return
	}
	delay := min(a.reconnectBase<<u.tries, reconnectMax)
	u.tries++
	u.reconnect = time.Now().Add(delay)
	u.timer = time.AfterFunc(delay, func() {
		a.postNoWait(func() {
			if a.sessUI[s] != u || u.reconnect.IsZero() {
				return // closed or reconnected by hand meanwhile
			}
			u.timer, u.reconnect = nil, time.Time{}
			_ = s.Restart()
			a.sessionsChanged()
		})
	})
}

// banner is the bottom line of a session that is not running.
func (a *App) banner(s *session.Session) string {
	if u := a.sessUI[s]; u != nil && !u.reconnect.IsZero() {
		_, code, _ := s.State()
		left := max(time.Until(u.reconnect).Round(time.Second), 0)
		return fmt.Sprintf("Connection lost (exit %d). Reconnecting in %s (try %d of %d) · r now, c close",
			code, left, u.tries, reconnectTries)
	}
	return DefaultBanner(s)
}

// layoutPanes shows the terminal column when there are sessions; the
// server list takes the whole width otherwise.
func (a *App) layoutPanes() {
	show := a.sessions.Len() > 0
	if show == a.termShown {
		return
	}
	a.termShown = show
	if show {
		a.body.ResizeItem(a.right, 0, 1)
	} else {
		a.body.ResizeItem(a.right, 0, 0)
	}
	a.layoutChrome()
	a.list.setCompact(show)
}
