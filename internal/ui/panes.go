package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/layout"
	"github.com/root-ali/tstui/internal/session"
	"github.com/root-ali/tstui/internal/tsh"
)

// paneView draws the active tab's sessions as split panes, one
// TerminalView each, and hands focus, keys, pastes and mouse events to
// them. In fullscreen the panes have no border and 1-cell dividers
// separate them; the dividers around the focused pane are highlighted.
type paneView struct {
	*tview.Box
	theme Theme

	tab   func() *session.Tab                  // active tab, nil when none
	view  func(*session.Session) *TerminalView // the pane's view
	empty *TerminalView                        // placeholder when no tab is active
	// focused reports whether the terminal area has the keyboard (for the
	// highlight).
	focused func() bool
	// onSize is told the size a new tab's terminal gets.
	onSize func(cols, rows int)
	// badge is the text drawn in the bottom right corner in fullscreen,
	// where there is no footer ("" for none).
	badge func() string

	fullscreen bool
	area       layout.Rect                       // the panes' area at the last Draw
	placed     []layout.Placed[*session.Session] // panes at the last Draw
}

func newPaneView(theme Theme) *paneView {
	p := &paneView{Box: tview.NewBox(), theme: theme, empty: NewTerminalView(theme)}
	p.empty.SetTitle(" Terminal ")
	return p
}

// gap is the divider width.
func (p *paneView) gap() int {
	if p.fullscreen {
		return 1
	}
	return 0
}

// shown returns the views drawn last, or the placeholder.
func (p *paneView) shown() []*TerminalView {
	if len(p.placed) == 0 {
		return []*TerminalView{p.empty}
	}
	out := make([]*TerminalView, len(p.placed))
	for i, pl := range p.placed {
		out[i] = p.view(pl.Value)
	}
	return out
}

// rectOf is where s was drawn last.
func (p *paneView) rectOf(s *session.Session) (layout.Rect, bool) {
	for _, pl := range p.placed {
		if pl.Value == s {
			return pl.Rect, true
		}
	}
	return layout.Rect{}, false
}

// Draw implements tview.Primitive.
func (p *paneView) Draw(screen tcell.Screen) {
	p.DrawForSubclass(screen, p)
	x, y, w, h := p.GetInnerRect()
	p.placed = nil
	if w <= 0 || h <= 0 {
		return
	}
	if p.onSize != nil {
		if p.fullscreen {
			p.onSize(w, h)
		} else {
			p.onSize(w-2, h-2)
		}
	}
	area := layout.Rect{X: x, Y: y, W: w, H: h}
	p.area = area
	t := p.tab()
	if t == nil {
		p.empty.SetBorder(!p.fullscreen)
		p.empty.SetRect(x, y, w, h)
		p.empty.Draw(screen)
		p.drawBadge(screen, area)
		return
	}
	var dividers []layout.Rect
	if t.Zoomed() {
		p.placed = []layout.Placed[*session.Session]{{Value: t.Focus(), Rect: area}}
	} else {
		p.placed, dividers = t.Tree().Layout(area, p.gap())
	}
	var focusRect layout.Rect
	for _, pl := range p.placed {
		v := p.view(pl.Value)
		v.SetBorder(!p.fullscreen)
		v.SetRect(pl.Rect.X, pl.Rect.Y, pl.Rect.W, pl.Rect.H)
		v.Draw(screen)
		pl.Value.ClearActivity() // shown is seen
		if pl.Value == t.Focus() {
			focusRect = pl.Rect
		}
	}
	p.drawDividers(screen, dividers, focusRect)
	p.drawBadge(screen, area)
}

// drawDividers draws the lines between panes; cells next to the focused
// pane get the focus colour, as in tmux.
func (p *paneView) drawDividers(screen tcell.Screen, dividers []layout.Rect, focus layout.Rect) {
	normal := tcell.StyleDefault.Foreground(p.theme.Border).Background(p.theme.Bg)
	hot := normal
	if p.focused == nil || p.focused() {
		hot = tcell.StyleDefault.Foreground(p.theme.BorderFocus).Background(p.theme.Bg)
	}
	near := func(cx, cy int) bool {
		return cx >= focus.X-1 && cx <= focus.X+focus.W && cy >= focus.Y-1 && cy <= focus.Y+focus.H
	}
	for _, d := range dividers {
		r := '─'
		if d.H > d.W || (d.W == 1 && d.H == 1) {
			r = '│'
		}
		for cy := d.Y; cy < d.Y+d.H; cy++ {
			for cx := d.X; cx < d.X+d.W; cx++ {
				style := normal
				if near(cx, cy) {
					style = hot
				}
				screen.SetContent(cx, cy, r, nil, style)
			}
		}
	}
}

// drawBadge draws the badge text in the bottom right corner (fullscreen
// only: otherwise the footer says it).
func (p *paneView) drawBadge(screen tcell.Screen, area layout.Rect) {
	if !p.fullscreen || p.badge == nil {
		return
	}
	text := p.badge()
	if text == "" {
		return
	}
	text = " " + text + " "
	n := min(tview.TaggedStringWidth(tview.Escape(text)), area.W)
	tview.Print(screen, fmt.Sprintf("[%s:%s:b]%s", tag(p.theme.TableCursorFg), tag(p.theme.TableCursorBg), tview.Escape(text)),
		area.X+area.W-n, area.Y+area.H-1, n, tview.AlignLeft, p.theme.TableCursorFg)
}

// Focus implements tview.Primitive: the focused pane gets it.
func (p *paneView) Focus(delegate func(tview.Primitive)) {
	if t := p.tab(); t != nil {
		delegate(p.view(t.Focus()))
		return
	}
	delegate(p.empty)
}

// HasFocus implements tview.Primitive.
func (p *paneView) HasFocus() bool {
	if p.empty.HasFocus() {
		return true
	}
	if t := p.tab(); t != nil {
		for _, s := range t.Panes() {
			if p.view(s).HasFocus() {
				return true
			}
		}
	}
	return p.Box.HasFocus()
}

// focusedView is the view that has tview's focus, or nil.
func (p *paneView) focusedView() *TerminalView {
	if t := p.tab(); t != nil {
		if v := p.view(t.Focus()); v.HasFocus() {
			return v
		}
	}
	if p.empty.HasFocus() {
		return p.empty
	}
	return nil
}

// InputHandler implements tview.Primitive: tview hands keys down from the
// root, so they must reach the focused pane.
func (p *paneView) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return p.WrapInputHandler(func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		if v := p.focusedView(); v != nil {
			if h := v.InputHandler(); h != nil {
				h(ev, setFocus)
			}
		}
	})
}

// PasteHandler implements tview.Primitive.
func (p *paneView) PasteHandler() func(string, func(tview.Primitive)) {
	return p.WrapPasteHandler(func(text string, setFocus func(tview.Primitive)) {
		if v := p.focusedView(); v != nil {
			if h := v.PasteHandler(); h != nil {
				h(text, setFocus)
			}
		}
	})
}

// MouseHandler implements tview.Primitive: the pane under the pointer
// gets the event (tview sends a drag straight to the pane capturing it).
func (p *paneView) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return p.WrapMouseHandler(func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if !p.InRect(ev.Position()) {
			return false, nil
		}
		for _, v := range p.shown() {
			if consumed, capture := v.MouseHandler()(action, ev, setFocus); consumed {
				return true, capture
			}
		}
		return true, nil // a divider
	})
}

// --- the app side: views, focus, fullscreen, splits ---

// openMode is how connectTo opens a session.
type openMode int

const (
	modeReuse  openMode = iota // focus the server's open session, else a new tab
	modeNewTab                 // always a new tab
	modeSplitH                 // a pane right of the focused one
	modeSplitV                 // a pane below the focused one
)

func (m openMode) split() bool { return m == modeSplitH || m == modeSplitV }

func splitMode(d layout.Dir) openMode {
	if d == layout.Horizontal {
		return modeSplitH
	}
	return modeSplitV
}

func (m openMode) dir() layout.Dir {
	if m == modeSplitH {
		return layout.Horizontal
	}
	return layout.Vertical
}

// viewFor returns s's terminal view, made on first use.
func (a *App) viewFor(s *session.Session) *TerminalView {
	if v := a.views[s]; v != nil {
		return v
	}
	v := NewTerminalView(a.theme)
	v.SetSession(s)
	v.SetBackgroundColor(a.theme.Bg)
	v.banner = a.banner
	v.onFocus = func() { a.focusPane(s) }
	v.onCopy = a.copied
	v.SetFocusFunc(func() { a.focusChanged(focusTerm) })
	a.views[s] = v
	return v
}

func (a *App) copied(n int) {
	a.flash(fmt.Sprintf("Copied %d characters to the clipboard (OSC 52).", n), a.theme.OK)
}

// allViews returns every session's view.
func (a *App) allViews() []*TerminalView {
	out := make([]*TerminalView, 0, len(a.views))
	for _, v := range a.views {
		out = append(out, v)
	}
	return out
}

// activeView is the focused pane's view, or the placeholder: never nil.
func (a *App) activeView() *TerminalView {
	if s := a.sessions.Active(); s != nil {
		return a.viewFor(s)
	}
	return a.panes.empty
}

// focusPane gives s the keyboard (a click on its pane).
func (a *App) focusPane(s *session.Session) {
	if a.sessions.FocusSession(s) {
		a.sessionsChanged()
	}
	a.setFocus(focusTerm)
}

// focusDir moves the focus to the pane next to the focused one; in a
// zoomed tab that also ends the zoom.
func (a *App) focusDir(d layout.Direction) {
	t := a.sessions.ActiveTab()
	if t == nil {
		return
	}
	placed, _ := t.Tree().Layout(a.panes.area, a.panes.gap())
	if n, ok := layout.Neighbor(placed, t.Focus(), d); ok {
		a.focusPane(n)
	}
}

// nextTab shows the tab delta places away, wrapping.
func (a *App) nextTab(delta int) {
	n := a.sessions.TabCount()
	if n == 0 {
		return
	}
	a.activateTab(((max(a.sessions.ActiveIndex(), 0)+delta)%n + n) % n)
}

// paintPanes sets every pane's title and border colour.
func (a *App) paintPanes() {
	active := a.sessions.Active()
	for _, s := range a.sessions.Sessions() {
		v := a.viewFor(s)
		st, _, _ := s.State()
		v.SetTitle(fmt.Sprintf(" [%s::b]%s[-::-] %s@%s · %s ", tag(a.theme.Title), tview.Escape(s.Name()),
			tview.Escape(s.Spec().Login), tview.Escape(s.Spec().Host), st))
		if s == active && a.focus == focusTerm {
			v.SetBorderColor(a.theme.BorderFocus)
		} else {
			v.SetBorderColor(a.theme.Border)
		}
	}
	if a.focus == focusTerm {
		a.panes.empty.SetBorderColor(a.theme.BorderFocus)
	} else {
		a.panes.empty.SetBorderColor(a.theme.Border)
	}
}

// toggleFullscreen turns fullscreen on (focusing the terminal) or off.
func (a *App) toggleFullscreen() {
	if a.fullscreen {
		a.setFullscreen(false)
		return
	}
	if a.sessions.Len() == 0 {
		a.flash("No session open: fullscreen shows the terminal.", a.theme.Info)
		return
	}
	a.setFullscreen(true)
	a.setFocus(focusTerm)
}

// setFullscreen shows only the terminal panes, or the whole UI again.
func (a *App) setFullscreen(on bool) {
	if on && a.sessions.Len() == 0 {
		on = false
	}
	if on == a.fullscreen {
		return
	}
	a.fullscreen = on
	a.panes.fullscreen = on
	a.layoutChrome()
	a.updateFooter()
}

// layoutChrome sizes the header, footer, server list and tab bar for the
// fullscreen and palette state. The footer shows while the palette is
// open, for its hints.
func (a *App) layoutChrome() {
	if a.fullscreen {
		a.mainFlex.ResizeItem(a.header.view, 0, 0)
		a.body.ResizeItem(a.list.table, 0, 0)
		a.right.ResizeItem(a.tabs.view, 0, 0)
	} else {
		a.mainFlex.ResizeItem(a.header.view, headerHeight, 0)
		a.right.ResizeItem(a.tabs.view, 1, 0)
		if a.termShown {
			a.body.ResizeItem(a.list.table, listWidth, 0)
		} else {
			a.body.ResizeItem(a.list.table, 0, 1)
		}
	}
	if a.fullscreen && !a.paletteOpen {
		a.mainFlex.ResizeItem(a.footer.view, 0, 0)
	} else {
		a.mainFlex.ResizeItem(a.footer.view, 1, 0)
	}
}

// toastTop is the toast's row offset: under the header, if it is shown.
func (a *App) toastTop() int {
	if a.fullscreen {
		return 0
	}
	return headerHeight
}

// paneBadge is what fullscreen shows instead of the footer: a footer
// message while it lasts, else the keys after the prefix.
func (a *App) paneBadge() string {
	if a.flashText != "" && time.Now().Before(a.flashUntil) {
		return a.flashText
	}
	if a.prefixPending {
		tab := ""
		if n := a.sessions.TabCount(); n > 1 {
			tab = fmt.Sprintf("tab %d/%d · ", a.sessions.ActiveIndex()+1, n)
		}
		return fmt.Sprintf("%s … %sf exit fullscreen · |/- split · arrows pane · ? keys", a.prefix.label, tab)
	}
	return ""
}

// canSplit says whether the focused pane has room for a split in d, and
// flashes why not. With no tab shown, a split opens a new tab.
func (a *App) canSplit(d layout.Dir) bool {
	s := a.sessions.Active()
	if s == nil {
		return true
	}
	r, ok := a.panes.rectOf(s)
	if !ok || layout.CanSplit(r, d, a.panes.gap()) {
		return true
	}
	a.flash("Pane too small to split.", a.theme.Warn)
	return false
}

// splitPicker asks for a server and opens it as a split in d.
func (a *App) splitPicker(d layout.Dir) {
	if !a.canSplit(d) {
		return
	}
	title := "<Split right>"
	if d == layout.Vertical {
		title = "<Split below>"
	}
	pre := ""
	if s := a.sessions.Active(); s != nil {
		pre = s.Spec().Host
	}
	a.openServerPicker(title, pre, func(srv tsh.Server) { a.connectTo(srv, splitMode(d)) })
}

// splitSelected opens the list's selected server as a split in d.
func (a *App) splitSelected(d layout.Dir) {
	srv, ok := a.list.selected()
	if !ok || !a.canSplit(d) {
		return
	}
	a.connectTo(srv, splitMode(d))
}

// splitCommand is :split / :vsplit: host, or the picker without one.
func (a *App) splitCommand(host string, d layout.Dir) {
	if host == "" {
		a.splitPicker(d)
		return
	}
	srv, ok := a.resolveHost(host)
	if !ok || !a.canSplit(d) {
		return
	}
	a.connectTo(srv, splitMode(d))
}

// openServerPicker lists the shown servers with a filter line; Enter
// picks the highlighted one. preselect is highlighted first.
func (a *App) openServerPicker(title, preselect string, pick func(tsh.Server)) {
	if !a.canOpenDialog() {
		return
	}
	servers := a.list.viewServers()
	if len(servers) == 0 && a.o.DemoShell {
		servers = []tsh.Server{{Hostname: "local"}} // another local shell
	}
	if len(servers) == 0 {
		a.flash("No servers to pick from.", a.theme.Warn)
		return
	}
	const name = "split"
	in := tview.NewInputField().SetLabel("filter: ").
		SetLabelColor(a.theme.DialogLabel).SetFieldTextColor(a.theme.DialogField).
		SetFieldBackgroundColor(tcell.ColorDefault)
	in.SetFieldStyle(tcell.StyleDefault.Foreground(a.theme.DialogField).Background(tcell.ColorDefault).Underline(true))
	in.SetBackgroundColor(a.theme.Bg)
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true).
		SetMainTextColor(a.theme.DialogField).
		SetSelectedTextColor(a.theme.TableCursorFg).
		SetSelectedBackgroundColor(a.theme.TableCursorBg)
	list.SetBackgroundColor(a.theme.Bg)

	clusters := map[string]bool{}
	for _, s := range servers {
		clusters[s.Cluster] = true
	}
	width := 40
	var shown []tsh.Server
	fill := func(text string) {
		q := strings.ToLower(strings.TrimSpace(text))
		list.Clear()
		shown = shown[:0]
		cur := 0
		for _, s := range servers {
			if q != "" && !strings.Contains(strings.ToLower(s.Hostname), q) {
				continue
			}
			label := tview.Escape(s.Hostname)
			if len(clusters) > 1 && s.Cluster != "" {
				label += fmt.Sprintf("  [%s](%s)", tag(a.theme.DialogPlaceholder), tview.Escape(s.Cluster))
			}
			if s.Hostname == preselect && q == "" {
				cur = len(shown)
			}
			shown = append(shown, s)
			list.AddItem(label, "", 0, nil)
			width = max(width, tview.TaggedStringWidth(label)+8)
		}
		list.SetCurrentItem(cur)
	}
	fill("")
	choose := func() {
		i := list.GetCurrentItem()
		if i < 0 || i >= len(shown) {
			return
		}
		srv := shown[i]
		a.closeDialog(name)
		pick(srv)
	}
	in.SetChangedFunc(fill)
	in.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		switch ev.Key() {
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn, tcell.KeyHome, tcell.KeyEnd:
			list.InputHandler()(ev, func(tview.Primitive) {})
			return nil
		case tcell.KeyEnter:
			choose()
			return nil
		case tcell.KeyEscape:
			a.closeDialog(name)
			return nil
		}
		return ev
	})
	list.SetSelectedFunc(func(int, string, string, rune) { choose() })
	hint := a.newText().SetText(fmt.Sprintf("[%s]type to filter · ↑↓ pick · enter open · esc cancel", tag(a.theme.DialogPlaceholder)))
	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(in, 1, 0, true).
		AddItem(list, 0, 1, false).
		AddItem(hint, 1, 0, false)
	height := min(len(servers), 15) + 4
	a.openDialog(name, newModalLayout(a.framed(title, body), a.theme.Bg, width, height), in)
}
