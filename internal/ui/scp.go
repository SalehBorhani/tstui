package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/transfer"
)

// remoteReady reports whether the current proxy is logged in with at
// least one SSH login, flashing why not.
func (a *App) remoteReady(what string) bool {
	if a.info.Proxy == "" {
		a.flash("No proxy yet: log in first (l).", a.theme.Err)
		return false
	}
	if a.info.State != auth.CertValid && a.info.State != auth.CertExpiring || time.Until(a.info.ValidUntil) <= 0 {
		a.flash("Log in first (L) to "+what+".", a.theme.Err)
		return false
	}
	if len(a.status.Logins) == 0 {
		a.flash("Your roles allow no SSH logins on this proxy.", a.theme.Err)
		return false
	}
	return true
}

// defaultLogin is the login last used on host, else the first allowed.
func (a *App) defaultLogin(host string) string {
	last := a.o.Logins.Get(a.info.Proxy, host)
	for _, l := range a.status.Logins {
		if l == last {
			return l
		}
	}
	if len(a.status.Logins) > 0 {
		return a.status.Logins[0]
	}
	return ""
}

// spinFrames animate running work.
var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// scpDialog is the file transfer dialog for one server.
type scpDialog struct {
	a       *App
	host    string
	form    *tview.Form
	status  *tview.TextView
	running bool
	cancel  context.CancelFunc
	started time.Time
	spin    int
	msg     string // last result, colour-tagged
}

// openSCP opens the transfer dialog for the selected server.
func (a *App) openSCP(dir transfer.Direction) {
	srv, ok := a.list.selected()
	if !ok {
		a.flash("Select a server first.", a.theme.Err)
		return
	}
	if !a.canOpenDialog() || !a.remoteReady("copy files") {
		return
	}
	d := &scpDialog{a: a, host: srv.Hostname, form: a.newForm(), status: a.newText()}
	logins := a.status.Logins
	login := a.defaultLogin(srv.Hostname)

	direction := tview.NewDropDown().SetLabel("Direction").
		SetOptions([]string{"upload   local → server", "download server → local"}, nil).
		SetCurrentOption(int(dir))
	loginDD := tview.NewDropDown().SetLabel("Login").SetOptions(logins, nil)
	for i, l := range logins {
		if l == login {
			loginDD.SetCurrentOption(i)
		}
	}
	local := tview.NewInputField().SetLabel("Local path").SetFieldWidth(0).
		SetPlaceholder("~/file, ./dir/ (tab or ↓ completes)")
	local.SetAutocompleteFunc(func(text string) []string {
		if text == "" {
			return nil
		}
		return transfer.CompleteLocal(text, a.home)
	})
	local.SetAutocompletedFunc(func(text string, _ int, source int) bool {
		local.SetText(text)
		// Keep the list open after picking a directory, to go deeper.
		return source != tview.AutocompletedNavigate && !strings.HasSuffix(text, "/")
	})
	remote := tview.NewInputField().SetLabel("Remote path").SetFieldWidth(0).
		SetPlaceholder("empty = home directory of the login")
	recursive := tview.NewCheckbox().SetLabel("Recursive")
	d.form.AddFormItem(direction).AddFormItem(loginDD).AddFormItem(local).AddFormItem(remote).AddFormItem(recursive)

	d.form.AddButton("Start", func() {
		if d.running {
			return
		}
		dirIdx, _ := direction.GetCurrentOption()
		_, chosen := loginDD.GetCurrentOption()
		spec := transfer.Spec{
			Proxy: a.info.Proxy, Cluster: srv.Cluster, Insecure: a.o.Insecure,
			Login: chosen, Host: srv.Hostname, Direction: transfer.Direction(dirIdx),
			Local: local.GetText(), Remote: remote.GetText(), Recursive: recursive.IsChecked(),
		}
		d.start(spec)
	})
	d.form.AddButton("Close", d.close)
	d.form.SetCancelFunc(d.close)
	d.render()

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(d.form, 8, 0, true).
		AddItem(d.status, 0, 1, false)
	title := fmt.Sprintf("<Copy files> %s", tview.Escape(srv.Hostname))
	a.openDialog("scp", newModalLayout(a.framed(title, body), a.theme.Bg, 72, 18), d.form)
	d.form.SetFocus(2) // the local path
	a.tv.SetFocus(d.form)
}

// start validates spec and runs the transfer in the background.
func (d *scpDialog) start(spec transfer.Spec) {
	a := d.a
	if _, err := transfer.Args(spec, a.home); err != nil {
		d.msg = fmt.Sprintf("[%s]✘ %s", tag(a.theme.Err), tview.Escape(err.Error()))
		d.render()
		return
	}
	if err := a.o.Logins.Set(a.info.Proxy, spec.Host, spec.Login); err != nil {
		a.logger.Warn("remember login", "err", err)
	}
	ctx, cancel := context.WithCancel(a.ctx)
	d.running, d.cancel, d.started, d.msg = true, cancel, time.Now(), ""
	d.form.GetButton(1).SetLabel("Cancel")
	d.render()

	done := make(chan struct{})
	a.bgDone.Add(2)
	a.goSafe(func() { // spinner
		defer a.bgDone.Done()
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				a.post(func() {
					if d.running {
						d.spin++
						d.render()
					}
				})
			case <-done:
				return
			case <-a.quit:
				return
			}
		}
	})
	a.goSafe(func() {
		defer a.bgDone.Done()
		err := transfer.Run(ctx, a.o.Client, spec, a.home)
		close(done)
		cancel()
		rec := transfer.Record{Spec: spec, Start: d.started, Duration: time.Since(d.started), Err: err}
		if lerr := a.transfers.Add(rec); lerr != nil {
			a.logger.Warn("transfer log", "err", lerr)
		}
		a.logger.Info("transfer", "direction", spec.Direction.String(), "host", spec.Host, "ok", err == nil,
			"duration", rec.Duration.Round(time.Millisecond))
		a.post(func() {
			d.running, d.cancel = false, nil
			d.form.GetButton(1).SetLabel("Close")
			if err != nil {
				d.msg = fmt.Sprintf("[%s]✘ %s", tag(a.theme.Err), tview.Escape(err.Error()))
			} else {
				d.msg = fmt.Sprintf("[%s]✔ %s finished in %s", tag(a.theme.OK), spec.Direction, rec.Duration.Round(100*time.Millisecond))
				if a.dialogName != "scp" {
					a.flash(fmt.Sprintf("%s to %s finished.", spec.Direction, spec.Host), a.theme.OK)
				}
			}
			d.render()
		})
	})
}

// close cancels a running transfer, or closes the dialog.
func (d *scpDialog) close() {
	if d.running && d.cancel != nil {
		d.cancel()
		return
	}
	d.a.closeDialog("scp")
}

func (d *scpDialog) render() {
	a := d.a
	var b strings.Builder
	switch {
	case d.running:
		fmt.Fprintf(&b, "[%s]%s copying… %s  (Cancel or esc stops it)\n", tag(a.theme.Info), spinFrames[d.spin%len(spinFrames)],
			time.Since(d.started).Round(time.Second))
	case d.msg != "":
		b.WriteString(d.msg + "\n")
	default:
		fmt.Fprintf(&b, "[%s]A directory upload is made recursive. tsh scp runs with an argv, never a shell.\n", tag(a.theme.DialogPlaceholder))
	}
	recs := a.transfers.Records()
	if len(recs) > 0 {
		fmt.Fprintf(&b, "\n[%s::b]Recent transfers[-::-]\n", tag(a.theme.Title))
		for i := len(recs) - 1; i >= 0 && i >= len(recs)-4; i-- {
			color := a.theme.OK
			if recs[i].Err != nil {
				color = a.theme.Err
			}
			fmt.Fprintf(&b, "[%s]%s[-]\n", tag(color), tview.Escape(recs[i].String()))
		}
	}
	d.status.SetText(b.String())
}
