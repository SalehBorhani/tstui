package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/portfwd"
)

// pfView is the port forward manager: running forwards first, then the
// saved ones from the config that are not running.
type pfView struct {
	a     *App
	table *tview.Table
	info  *tview.TextView
	rows  []pfRow
}

type pfRow struct {
	fwd   *portfwd.Info       // a forward tstui runs (or ran)
	saved *config.PortForward // a saved forward, not running
}

// openPF shows the port forward manager.
func (a *App) openPF() {
	if !a.canOpenDialog() {
		return
	}
	v := &pfView{a: a, info: a.newText()}
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).
		SetSelectedStyle(tcell.StyleDefault.Foreground(a.theme.TableCursorFg).Background(a.theme.TableCursorBg))
	v.table.SetBackgroundColor(a.theme.Bg)
	v.table.SetInputCapture(v.key)
	v.table.SetSelectionChangedFunc(func(int, int) { v.renderInfo() })
	a.pfView = v
	v.render()
	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(v.table, 0, 1, true).
		AddItem(v.info, 4, 0, false)
	title := "<Port forwards> n new · enter start saved · s stop · x remove · f save · D delete saved · esc close"
	a.openDialog("pf", newViewLayout(a.framed(title, body), a.theme.Bg, 80, 70, 14), v.table)
}

func (v *pfView) close() {
	v.a.pfView = nil
	v.a.closeDialog("pf")
}

func (v *pfView) render() {
	a := v.a
	sel, _ := v.table.GetSelection()
	v.rows = v.rows[:0]
	running := map[string]bool{}
	for _, f := range a.pf.List() {
		f := f
		v.rows = append(v.rows, pfRow{fwd: &f})
		if f.Spec.Name != "" && (f.State == portfwd.Starting || f.State == portfwd.Active) {
			running[f.Spec.Name] = true
		}
	}
	for i := range a.o.Config.PortForwards {
		if pf := &a.o.Config.PortForwards[i]; !running[pf.Name] {
			v.rows = append(v.rows, pfRow{saved: pf})
		}
	}
	v.table.Clear()
	for col, h := range []string{"NAME", "LOCAL", "REMOTE", "VIA", "STATE", "INFO"} {
		v.table.SetCell(0, col, tview.NewTableCell(h).SetTextColor(a.theme.TableHeader).
			SetAttributes(tcell.AttrBold).SetSelectable(false).SetExpansion(1))
	}
	for i, r := range v.rows {
		var cells []string
		color := a.theme.TableFg
		if r.fwd != nil {
			f := r.fwd
			state, info := f.State.String(), f.LastLine
			switch f.State {
			case portfwd.Active:
				color = a.theme.OK
				info = "up " + time.Since(f.Started).Round(time.Second).String()
			case portfwd.Starting:
				color = a.theme.Warn
				if info == "" {
					info = "waiting for the local port (a security key tap?)"
				}
			case portfwd.Failed:
				color = a.theme.Err
				if f.Err != nil {
					info = f.Err.Error()
				}
			case portfwd.Stopped:
				color = a.theme.DialogPlaceholder
			}
			cells = []string{dash(f.Spec.Name), f.Spec.Local(), fmt.Sprintf("%s:%d", f.Spec.RemoteHost, f.Spec.RemotePort),
				f.Spec.Login + "@" + f.Spec.Host, state, info}
		} else {
			s := r.saved
			bind, lport, rhost, rport, _ := portfwd.ParseForward(s.Forward)
			via := s.Host
			if s.Login != "" {
				via = s.Login + "@" + s.Host
			}
			if s.Proxy != "" {
				via += " (" + s.Proxy + ")"
			}
			color = a.theme.DialogPlaceholder
			cells = []string{s.Name, fmt.Sprintf("%s:%d", bind, lport), fmt.Sprintf("%s:%d", rhost, rport), via, "saved", "enter starts it"}
		}
		for col, text := range cells {
			v.table.SetCell(i+1, col, tview.NewTableCell(tview.Escape(text)).SetTextColor(color).SetExpansion(1))
		}
	}
	if len(v.rows) == 0 {
		v.table.SetCell(1, 0, tview.NewTableCell("No port forwards. Press n to create one.").
			SetTextColor(a.theme.DialogPlaceholder).SetSelectable(false))
	} else {
		v.table.Select(min(max(sel, 1), len(v.rows)), 0)
	}
	v.renderInfo()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (v *pfView) renderInfo() {
	a := v.a
	r, ok := v.current()
	text := fmt.Sprintf("[%s]Forwards listen on 127.0.0.1 unless a bind address is given, and stop when tstui quits.", tag(a.theme.DialogPlaceholder))
	if ok && r.fwd != nil {
		f := r.fwd
		text = fmt.Sprintf("[%s]%s → %s · started %s", tag(a.theme.DialogFg), tview.Escape(f.Spec.Local()),
			tview.Escape(f.Spec.Remote()), f.Started.Format(time.TimeOnly))
		if f.LastLine != "" {
			text += fmt.Sprintf("\n[%s]tsh: %s", tag(a.theme.DialogPlaceholder), tview.Escape(f.LastLine))
		}
	}
	v.info.SetText(text)
}

func (v *pfView) current() (pfRow, bool) {
	row, _ := v.table.GetSelection()
	if row < 1 || row > len(v.rows) {
		return pfRow{}, false
	}
	return v.rows[row-1], true
}

func (v *pfView) key(ev *tcell.EventKey) *tcell.EventKey {
	a := v.a
	if ev.Key() == tcell.KeyEscape {
		v.close()
		return nil
	}
	r, ok := v.current()
	switch {
	case ev.Key() == tcell.KeyEnter:
		if ok && r.saved != nil {
			a.startSavedForward(*r.saved)
			v.render()
		}
	case ev.Rune() == 'q':
		v.close()
	case ev.Rune() == 'n':
		v.close()
		a.openForwardForm()
	case ev.Rune() == 's':
		if ok && r.fwd != nil {
			a.pf.Stop(r.fwd.ID)
		}
	case ev.Rune() == 'x':
		if ok && r.fwd != nil {
			a.pf.Remove(r.fwd.ID)
			v.render()
		}
	case ev.Rune() == 'f':
		if ok && r.fwd != nil {
			spec := r.fwd.Spec
			v.close()
			a.prompt("pf-save", "<Save port forward>", "Name ", spec.Name, nil, func(name string) {
				a.saveForward(name, spec)
				a.openPF()
			})
		}
	case ev.Rune() == 'D':
		if ok && r.saved != nil {
			a.deleteSavedForward(r.saved.Name)
			v.render()
		}
	default:
		return ev
	}
	return nil
}

// startSavedForward starts a forward saved in the config.
func (a *App) startSavedForward(pf config.PortForward) {
	if !a.remoteReady("start a port forward") {
		return
	}
	bind, lport, rhost, rport, err := portfwd.ParseForward(pf.Forward)
	if err != nil {
		a.flash(err.Error(), a.theme.Err)
		return
	}
	proxy := a.info.Proxy
	if pf.Proxy != "" {
		proxy = pf.Proxy
		if p, ok := a.o.Config.ProxyByName(pf.Proxy); ok {
			proxy = p.Addr
		}
	}
	login := pf.Login
	if login == "" {
		login = a.defaultLogin(pf.Host)
	}
	cluster := pf.Cluster
	if cluster == "" && proxyKey(proxy) == proxyKey(a.info.Proxy) {
		if srv, ok := a.list.find(pf.Host); ok {
			cluster = srv.Cluster
		}
	}
	spec := portfwd.Spec{Name: pf.Name, Proxy: proxy, Cluster: cluster, Insecure: a.o.Insecure, Login: login, Host: pf.Host,
		BindAddr: bind, LocalPort: lport, RemoteHost: rhost, RemotePort: rport}
	if _, err := a.pf.Start(spec); err != nil {
		a.flash("Port forward "+pf.Name+": "+err.Error(), a.theme.Err)
		return
	}
	a.flash("Port forward "+pf.Name+" starting on "+spec.Normalize().Local()+".", a.theme.OK)
}

// saveForward adds or replaces a saved forward in the config.
func (a *App) saveForward(name string, s portfwd.Spec) {
	name = strings.TrimSpace(name)
	pf := config.PortForward{Name: name, Cluster: s.Cluster, Host: s.Host, Login: s.Login, Forward: s.ForwardSpec()}
	if cfgName, ok := a.configProxyName(s.Proxy); ok {
		pf.Proxy = cfgName
	} else if proxyKey(s.Proxy) != proxyKey(a.info.Proxy) {
		pf.Proxy = s.Proxy
	}
	old := append([]config.PortForward(nil), a.o.Config.PortForwards...)
	replaced := false
	for i := range a.o.Config.PortForwards {
		if a.o.Config.PortForwards[i].Name == name {
			a.o.Config.PortForwards[i], replaced = pf, true
		}
	}
	if !replaced {
		a.o.Config.PortForwards = append(a.o.Config.PortForwards, pf)
	}
	if err := a.o.Config.Validate(); err != nil {
		a.o.Config.PortForwards = old
		a.flash("Cannot save: "+err.Error(), a.theme.Err)
		return
	}
	if err := a.saveConfig(); err != nil {
		a.flash("Saved for this session only: "+err.Error(), a.theme.Warn)
		return
	}
	a.flash("Saved port forward "+name+".", a.theme.OK)
}

func (a *App) deleteSavedForward(name string) {
	out := a.o.Config.PortForwards[:0:0]
	for _, pf := range a.o.Config.PortForwards {
		if pf.Name != name {
			out = append(out, pf)
		}
	}
	a.o.Config.PortForwards = out
	if err := a.saveConfig(); err != nil {
		a.flash("Deleted for this session only: "+err.Error(), a.theme.Warn)
		return
	}
	a.flash("Deleted saved port forward "+name+".", a.theme.OK)
}

// openForwardForm asks for a new forward and starts it.
func (a *App) openForwardForm() {
	if !a.canOpenDialog() || !a.remoteReady("start a port forward") {
		return
	}
	host := a.list.selectedHost()
	form := a.newForm()
	errLine := a.newText()
	server := tview.NewInputField().SetLabel("Server").SetText(host).SetFieldWidth(0)
	server.SetAutocompleteFunc(func(text string) []string {
		var out []string
		for _, h := range a.list.hostnames() {
			if text != "" && strings.HasPrefix(h, text) && h != text {
				out = append(out, h)
			}
		}
		return out
	})
	logins := a.status.Logins
	loginDD := tview.NewDropDown().SetLabel("Login").SetOptions(logins, nil)
	for i, l := range logins {
		if l == a.defaultLogin(host) {
			loginDD.SetCurrentOption(i)
		}
	}
	fwd := tview.NewInputField().SetLabel("Forward").SetFieldWidth(0).
		SetPlaceholder("[bind:]localport:host:port, e.g. 5432:localhost:5432")
	name := tview.NewInputField().SetLabel("Name").SetFieldWidth(0).SetPlaceholder("optional; needed to save")
	save := tview.NewCheckbox().SetLabel("Save in config")
	form.AddFormItem(server).AddFormItem(loginDD).AddFormItem(fwd).AddFormItem(name).AddFormItem(save)
	errLine.SetText(fmt.Sprintf("[%s]The forward runs `tsh ssh -N --forward=…` until you stop it or quit tstui.", tag(a.theme.DialogPlaceholder)))
	fail := func(err error) {
		errLine.SetText(fmt.Sprintf("[%s]✘ %s", tag(a.theme.Err), tview.Escape(err.Error())))
	}
	form.AddButton("Start", func() {
		bind, lport, rhost, rport, err := portfwd.ParseForward(fwd.GetText())
		if err != nil {
			fail(err)
			return
		}
		_, login := loginDD.GetCurrentOption()
		host := strings.TrimSpace(server.GetText())
		cluster := a.list.view
		if srv, ok := a.list.find(host); ok {
			cluster = srv.Cluster
		}
		if sel, ok := a.list.selected(); ok && sel.Hostname == host {
			cluster = sel.Cluster // the server the form was opened on
		}
		spec := portfwd.Spec{Name: strings.TrimSpace(name.GetText()), Proxy: a.info.Proxy, Cluster: cluster, Insecure: a.o.Insecure,
			Login: login, Host: host,
			BindAddr: bind, LocalPort: lport, RemoteHost: rhost, RemotePort: rport}
		if save.IsChecked() && spec.Name == "" {
			fail(fmt.Errorf("give a name to save the forward"))
			return
		}
		if _, err := a.pf.Start(spec); err != nil {
			fail(err)
			return
		}
		a.closeDialog("pf-new")
		if save.IsChecked() {
			a.saveForward(spec.Name, spec.Normalize())
		}
		a.openPF()
	})
	form.AddButton("Cancel", func() { a.closeDialog("pf-new"); a.openPF() })
	form.SetCancelFunc(func() { a.closeDialog("pf-new"); a.openPF() })
	body := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(form, 8, 0, true).AddItem(errLine, 0, 1, false)
	a.openDialog("pf-new", newModalLayout(a.framed("<New port forward>", body), a.theme.Bg, 70, 13), form)
	if host != "" {
		form.SetFocus(2)
		a.tv.SetFocus(form)
	}
}
