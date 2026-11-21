package ui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/filter"
	"github.com/root-ali/tstui/internal/layout"
	"github.com/root-ali/tstui/internal/palette"
	"github.com/root-ali/tstui/internal/transfer"
	"github.com/root-ali/tstui/internal/tsh"
)

// cmdLineHeight is the palette's height when open (a bordered line).
const cmdLineHeight = 3

func (a *App) newCmdLine() *tview.InputField {
	in := tview.NewInputField().SetLabel("> ")
	in.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	a.styleCmdLine(in)
	in.SetChangedFunc(func(text string) {
		if a.searchOpen {
			a.searchChanged(text)
		} else {
			a.paletteHint()
		}
	})
	in.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if a.searchOpen {
			return a.searchKey(ev)
		}
		return a.paletteKey(ev)
	})
	return in
}

func (a *App) styleCmdLine(in *tview.InputField) {
	in.SetLabelColor(a.theme.MenuKey).SetFieldTextColor(a.theme.DialogField).
		SetFieldBackgroundColor(tcell.ColorDefault)
	in.SetFieldStyle(tcell.StyleDefault.Foreground(a.theme.DialogField).Background(tcell.ColorDefault))
	in.SetBorderColor(a.theme.BorderFocus).SetTitleColor(a.theme.Title).SetBackgroundColor(a.theme.Bg)
	if a.searchOpen {
		in.SetLabel("/ ").SetTitle(" Search ")
	} else {
		in.SetLabel("> ").SetTitle(" Command ")
	}
}

// openPalette opens the command line with initial typed in.
func (a *App) openPalette(initial string) {
	if !a.canOpenDialog() {
		return
	}
	a.history.Reset()
	a.styleCmdLine(a.cmdLine) // the label and title of the command line or the search bar
	a.cmdLine.SetText(initial)
	a.mainFlex.ResizeItem(a.cmdLine, cmdLineHeight, 0)
	a.paletteOpen = true
	a.layoutChrome() // the footer shows the palette's hints, even in fullscreen
	a.dialog, a.dialogName = a.cmdLine, "palette"
	a.tv.SetFocus(a.cmdLine)
	a.paletteHint()
}

func (a *App) closePalette() {
	if !a.paletteOpen {
		return
	}
	a.paletteOpen, a.searchOpen = false, false
	a.mainFlex.ResizeItem(a.cmdLine, 0, 0)
	a.layoutChrome()
	a.dialog, a.dialogName = nil, ""
	a.setFocus(a.focus)
}

// paletteKey handles the keys the command line treats specially: Enter,
// Esc, Tab completion and Up/Down history.
func (a *App) paletteKey(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyEscape:
		a.closePalette()
		return nil
	case tcell.KeyEnter:
		line := strings.TrimSpace(a.cmdLine.GetText())
		a.closePalette()
		if line != "" {
			a.history.Add(line)
			a.runCommand(line)
		}
		return nil
	case tcell.KeyTab:
		cands := palette.Complete(a.cmdLine.GetText(), a.paletteSources())
		if p := palette.CommonPrefix(cands); len(p) > len(strings.TrimLeft(a.cmdLine.GetText(), ": ")) {
			a.cmdLine.SetText(p)
		}
		return nil
	case tcell.KeyUp:
		a.cmdLine.SetText(a.history.Prev(a.cmdLine.GetText()))
		return nil
	case tcell.KeyDown:
		a.cmdLine.SetText(a.history.Next())
		return nil
	}
	return ev
}

// paletteSources are the completion values for the current state.
func (a *App) paletteSources() palette.Sources {
	src := palette.Sources{Hosts: a.list.hostnames(), Clusters: a.clusterNames(), Skins: a.o.Skins.Names()}
	for _, p := range a.proxyEntries() {
		src.Proxies = append(src.Proxies, p.Name)
	}
	return src
}

// paletteHint shows the completions, or the matching command's help, in
// the footer while the palette is open.
func (a *App) paletteHint() {
	if !a.paletteOpen {
		return
	}
	if a.searchOpen {
		a.footer.view.SetText(a.searchHint())
		return
	}
	text := a.cmdLine.GetText()
	cands := palette.Complete(text, a.paletteSources())
	var b strings.Builder
	switch {
	case len(cands) > 0 && strings.TrimSpace(text) != "":
		fmt.Fprintf(&b, "[%s::b]<tab>[%s::-] ", tag(a.theme.MenuKey), tag(a.theme.MenuText))
		for i, c := range cands {
			if i == 8 {
				fmt.Fprintf(&b, "… +%d", len(cands)-i)
				break
			}
			b.WriteString(tview.Escape(strings.TrimSpace(c)) + "  ")
		}
	default:
		name, _, _ := strings.Cut(strings.TrimSpace(text), " ")
		if spec, ok := palette.Lookup(name); ok && name != "" {
			fmt.Fprintf(&b, "[%s::b]:%s %s[%s::-]  %s", tag(a.theme.MenuKey), spec.Name, tview.Escape(spec.Usage),
				tag(a.theme.MenuText), tview.Escape(spec.Help))
		} else {
			fmt.Fprintf(&b, "[%s::b]<enter>[%s::-] run  [%s::b]<tab>[%s::-] complete  [%s::b]<↑↓>[%s::-] history  [%s::b]<esc>[%s::-] close  [%s]:help lists commands",
				tag(a.theme.MenuKey), tag(a.theme.MenuText), tag(a.theme.MenuKey), tag(a.theme.MenuText),
				tag(a.theme.MenuKey), tag(a.theme.MenuText), tag(a.theme.MenuKey), tag(a.theme.MenuText), tag(a.theme.DialogPlaceholder))
		}
	}
	a.footer.view.SetText(b.String())
}

// runCommand runs one palette line.
func (a *App) runCommand(line string) {
	cmd, err := palette.Parse(line)
	if err != nil {
		if !errors.Is(err, palette.ErrEmpty) {
			a.flash(err.Error(), a.theme.Err)
		}
		return
	}
	switch cmd.Name {
	case "proxy":
		if cmd.Arg == "" {
			a.openProxyPicker()
			return
		}
		a.switchProxyByName(cmd.Arg)
	case "cluster":
		a.clusterCommand(cmd.Arg)
	case "login":
		a.showLogin("Log in to Teleport.")
	case "logout":
		a.logout()
	case "ssh":
		a.sshCommand(cmd.Arg)
	case "sessions":
		a.openSessionPicker()
	case "split":
		a.splitCommand(cmd.Arg, layout.Vertical)
	case "vsplit":
		a.splitCommand(cmd.Arg, layout.Horizontal)
	case "fullscreen":
		a.toggleFullscreen()
	case "zoom":
		if !a.sessions.ToggleZoom() {
			a.flash("Nothing to zoom: the tab has one pane.", a.theme.Info)
			return
		}
		a.sessionsChanged()
		a.setFocus(focusTerm)
	case "filter":
		a.applyFilter(cmd.Arg)
	case "fav":
		host := cmd.Arg
		if host == "" {
			host = a.list.selectedHost()
		}
		a.toggleFavorite(host)
	case "sort":
		key, _ := parseSortKey(cmd.Arg) // Parse checked it against the choices
		a.sortList(key)
	case "refresh":
		a.refresh()
	case "skin":
		a.setSkin(cmd.Arg)
	case "scp":
		dir := transfer.Upload
		if cmd.Arg == "down" {
			dir = transfer.Download
		}
		a.openSCP(dir)
	case "pf":
		a.openPF()
	case "exec":
		a.startExec(cmd.Arg)
	case "help":
		a.openHelp()
	case "quit":
		a.requestQuit()
	}
}

// applyFilter parses and applies a server filter; "" clears it.
func (a *App) applyFilter(expr string) {
	f, err := filter.Parse(expr)
	if err != nil {
		a.flash(err.Error(), a.theme.Err)
		return
	}
	a.list.setFilter(f)
	if f.Empty() {
		a.flash("Filter cleared.", a.theme.Info)
	}
}

// sshCommand opens a session to host, which may not be in the list (it
// is resolved by tsh on the proxy).
func (a *App) sshCommand(host string) {
	if srv, ok := a.resolveHost(host); ok {
		a.connectTo(srv, modeReuse)
	}
}

// resolveHost checks a typed server name and finds its server; it
// flashes why when the name is invalid.
func (a *App) resolveHost(host string) (tsh.Server, bool) {
	if host == "" || strings.HasPrefix(host, "-") || strings.ContainsAny(host, "@ \t") {
		a.flash(fmt.Sprintf("Invalid server name %q.", host), a.theme.Err)
		return tsh.Server{}, false
	}
	srv, ok := a.list.find(host)
	if !ok {
		// Not listed: tsh resolves it in the cluster on view, if any.
		srv = tsh.Server{Hostname: host, Cluster: a.list.view}
	}
	return srv, true
}

// refresh reloads the servers, the login state and the health checks.
func (a *App) refresh() {
	if a.info.Proxy == "" {
		return
	}
	a.serversLoaded = false
	a.loadServers()
	if a.watcher != nil {
		a.watcher.Refresh()
	}
	if a.monitor != nil {
		a.monitor.Refresh()
	}
}

// openSessionPicker lists the open sessions; Enter shows one.
func (a *App) openSessionPicker() {
	ss := a.sessions.Sessions()
	if len(ss) == 0 {
		a.flash("No sessions open.", a.theme.Info)
		return
	}
	items := make([]string, len(ss))
	current := 0
	for i, s := range ss {
		st, _, _ := s.State()
		tab := a.sessions.TabOf(s)
		pos := fmt.Sprintf("%d", tab+1)
		if t := a.sessions.Tabs()[tab]; len(t.Panes()) > 1 {
			for j, p := range t.Panes() {
				if p == s {
					pos = fmt.Sprintf("%d.%d", tab+1, j+1)
				}
			}
		}
		if s == a.sessions.Active() {
			current = i
		}
		items[i] = fmt.Sprintf("%s: %s  %s@%s  (%s)", pos, tview.Escape(s.Name()), tview.Escape(s.Spec().Login),
			tview.Escape(s.Spec().Host), st)
	}
	a.pickFrom("sessions", "<Sessions>", items, current, func(i int) { a.focusPane(ss[i]) })
}
