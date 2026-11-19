package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/multiexec"
	"github.com/root-ali/tstui/internal/transfer"
	"github.com/root-ali/tstui/internal/tsh"
)

// startExec begins a multi-exec on the marked servers (or the selected
// one): ask for the command if not given, then the login, then confirm.
func (a *App) startExec(command string) {
	if a.execView != nil && a.execView.running {
		if a.canOpenDialog() {
			a.execView.show() // back to the running one
		}
		return
	}
	targets := a.list.markedHosts()
	if len(targets) == 0 {
		if srv, ok := a.list.selected(); ok {
			targets = []tsh.Server{srv}
		}
	}
	if len(targets) == 0 {
		a.flash("Mark servers with space (a marks all shown), then :exec.", a.theme.Err)
		return
	}
	if !a.remoteReady("run commands") {
		return
	}
	if strings.TrimSpace(command) == "" {
		a.prompt("exec-cmd", fmt.Sprintf("<Run on %d server(s)>", len(targets)), "Command ", "", nil, func(c string) {
			if strings.TrimSpace(c) != "" {
				a.execWithLogin(targets, c)
			}
		})
		return
	}
	a.execWithLogin(targets, command)
}

func (a *App) execWithLogin(targets []tsh.Server, command string) {
	cmd, err := multiexec.ValidateCommand(command)
	if err != nil {
		a.flash(err.Error(), a.theme.Err)
		return
	}
	logins := a.status.Logins
	if len(logins) == 1 {
		a.execConfirm(targets, logins[0], cmd)
		return
	}
	cur := 0
	last := a.defaultLogin(targets[0].Hostname)
	for i, l := range logins {
		if l == last {
			cur = i
		}
	}
	a.pickFrom("exec-login", fmt.Sprintf("<Login for %d server(s)>", len(targets)), logins, cur, func(i int) {
		a.execConfirm(targets, logins[i], cmd)
	})
}

// execConfirm lists the targets and the command. A risky command needs
// "yes" typed out before it runs.
func (a *App) execConfirm(targets []tsh.Server, login, command string) {
	if !a.canOpenDialog() {
		return
	}
	risky := multiexec.Risky(command)
	conc, timeout := a.o.Config.Exec.Concurrency, a.o.Config.Exec.TimeoutDuration()
	var b strings.Builder
	fmt.Fprintf(&b, "[%s]Run as [%s::b]%s[-::-][%s] on [::b]%d server(s)[::-], %d at a time, %s timeout each:\n\n",
		tag(a.theme.DialogFg), tag(a.theme.InfoValue), tview.Escape(login), tag(a.theme.DialogFg), len(targets), conc, timeout)
	fmt.Fprintf(&b, "  [%s::b]%s[-::-]\n\n", tag(a.theme.InfoValue), tview.Escape(command))
	const show = 12
	for i, t := range targets {
		if i == show {
			fmt.Fprintf(&b, "  [%s]… and %d more\n", tag(a.theme.DialogPlaceholder), len(targets)-show)
			break
		}
		fmt.Fprintf(&b, "  [%s]• %s %s %s\n", tag(a.theme.DialogFg), tview.Escape(t.Hostname),
			tview.Escape(a.list.shortName(t.Cluster)), tview.Escape(t.Labels["env"]))
	}
	if len(risky) > 0 {
		fmt.Fprintf(&b, "\n[%s::b]⚠ DANGEROUS COMMAND: %s[-::-]\n[%s]This can destroy data or take servers down on every host listed. Type yes to run it.\n",
			tag(a.theme.Err), tview.Escape(strings.Join(risky, "; ")), tag(a.theme.Err))
	}
	text := a.newText().SetText(b.String())
	errLine := a.newText()
	form := a.newForm()
	var yes *tview.InputField
	if len(risky) > 0 {
		yes = tview.NewInputField().SetLabel("Type yes ").SetFieldWidth(6)
		form.AddFormItem(yes)
	}
	run := func() {
		if yes != nil && strings.TrimSpace(yes.GetText()) != "yes" {
			errLine.SetText(fmt.Sprintf("[%s::b]Type yes in the field to confirm.", tag(a.theme.Err)))
			form.SetFocus(0) // back to the field, e.g. from "Run anyway"
			a.tv.SetFocus(form)
			return
		}
		a.closeDialog("exec-confirm")
		if err := a.o.Logins.Set(a.info.Proxy, targets[0].Hostname, login); err != nil {
			a.logger.Warn("remember login", "err", err)
		}
		a.execRun(targets, login, command, risky)
	}
	label := "Run"
	if len(risky) > 0 {
		label = "Run anyway"
	}
	form.AddButton(label, run).AddButton("Cancel", func() { a.closeDialog("exec-confirm") })
	form.SetCancelFunc(func() { a.closeDialog("exec-confirm") })
	if yes != nil {
		// Enter is taken here so the Form does not move on to the buttons.
		yes.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
			if ev.Key() == tcell.KeyEnter {
				run()
				return nil
			}
			return ev
		})
	}
	formH := 1 // the buttons
	if yes != nil {
		formH = 3 // the field, a blank line, the buttons
	}
	body := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(text, 0, 1, false).
		AddItem(errLine, 1, 0, false).AddItem(form, formH, 0, true)
	frame := a.framed("<Confirm multi-exec>", body)
	if len(risky) > 0 {
		frame.SetBorderColor(a.theme.Err).SetTitleColor(a.theme.Err)
	}
	height := min(len(targets), show+1) + 11
	if len(risky) > 0 {
		height += 4
	}
	a.openDialog("exec-confirm", newModalLayout(frame, a.theme.Bg, 70, height), form)
}

// execView shows a multi-exec's progress and results.
type execView struct {
	a        *App
	frame    *tview.Flex
	table    *tview.Table
	output   *tview.TextView
	command  string
	login    string
	started  time.Time
	results  []multiexec.Result
	running  bool
	cancel   context.CancelFunc
	finished time.Time
}

// execRun audits, starts the run and shows its view.
func (a *App) execRun(targets []tsh.Server, login, command string, risky []string) {
	v := &execView{a: a, command: command, login: login, started: time.Now(), running: true}
	mt := make([]multiexec.Target, len(targets))
	hosts := make([]string, len(targets))
	v.results = make([]multiexec.Result, len(targets))
	for i, t := range targets {
		mt[i] = multiexec.Target{Proxy: a.info.Proxy, Cluster: t.Cluster, Login: login, Host: t.Hostname}
		hosts[i] = mt[i].Name()
		v.results[i] = multiexec.Result{Target: mt[i], Status: multiexec.Pending, ExitCode: -1}
	}
	entry := multiexec.AuditEntry{Time: v.started, Event: "start", Proxy: a.info.Proxy, Login: login,
		Hosts: hosts, Command: command, Risky: risky}
	if err := a.audit.Write(entry); err != nil {
		// Refuse to run what cannot be audited.
		a.flash("Multi-exec not run: "+err.Error(), a.theme.Err)
		return
	}
	a.logger.Info("multi-exec start", "hosts", len(hosts), "risky", len(risky) > 0)
	ctx, cancel := context.WithCancel(a.ctx)
	v.cancel = cancel
	a.execView = v
	a.list.clearMarks()
	v.show()

	o := multiexec.Options{
		Concurrency: a.o.Config.Exec.Concurrency,
		Timeout:     a.o.Config.Exec.TimeoutDuration(),
		Insecure:    a.o.Insecure,
		OnUpdate: func(i int, r multiexec.Result) {
			a.post(func() {
				v.results[i] = r
				v.render()
			})
		},
	}
	a.bgDone.Add(1)
	a.goSafe(func() {
		defer a.bgDone.Done()
		res := multiexec.Run(ctx, a.o.Client, mt, command, o)
		cancel()
		if err := a.audit.Write(multiexec.FinishEntry(entry, res, time.Now())); err != nil {
			a.logger.Warn("audit log", "err", err)
		}
		a.post(func() {
			v.results, v.running, v.finished = res, false, time.Now()
			a.logger.Info("multi-exec finished", "hosts", len(res))
			v.render()
		})
	})
}

func (v *execView) show() {
	a := v.a
	v.table = tview.NewTable().SetSelectable(true, false).SetFixed(1, 0).
		SetSelectedStyle(tcell.StyleDefault.Foreground(a.theme.TableCursorFg).Background(a.theme.TableCursorBg))
	v.table.SetBackgroundColor(a.theme.Bg)
	v.table.SetSelectionChangedFunc(func(int, int) { v.renderOutput() })
	v.table.SetInputCapture(v.key)
	v.output = a.newText().SetWrap(true).SetWordWrap(false)
	v.output.SetBorder(true).SetBorderColor(a.theme.Border).SetTitleColor(a.theme.Title)
	body := tview.NewFlex().
		AddItem(v.table, 0, 2, true).
		AddItem(v.output, 0, 3, false)
	v.frame = a.framed("<Multi-exec>", body)
	a.openDialog("exec", newViewLayout(v.frame, a.theme.Bg, 90, 70, 16), v.table)
	v.render()
}

func (v *execView) render() {
	a := v.a
	if v.table == nil {
		return
	}
	sel, _ := v.table.GetSelection()
	v.table.Clear()
	for col, h := range []string{"HOST", "STATUS", "EXIT", "TIME"} {
		v.table.SetCell(0, col, tview.NewTableCell(h).SetTextColor(a.theme.TableHeader).
			SetAttributes(tcell.AttrBold).SetSelectable(false).SetExpansion(1))
	}
	done, ok := 0, 0
	multiCluster := false
	for _, r := range v.results {
		if r.Target.Cluster != v.results[0].Target.Cluster {
			multiCluster = true
		}
	}
	for i, r := range v.results {
		color := a.theme.DialogPlaceholder
		switch r.Status {
		case multiexec.OK:
			color = a.theme.OK
		case multiexec.Failed, multiexec.Error, multiexec.TimedOut:
			color = a.theme.Err
		case multiexec.Running:
			color = a.theme.Warn
		}
		exit, dur := "", ""
		if r.Status.Done() {
			done++
			if r.ExitCode >= 0 {
				exit = fmt.Sprint(r.ExitCode)
			}
			dur = r.Duration.Round(100 * time.Millisecond).String()
		}
		if r.Status == multiexec.OK {
			ok++
		}
		host := r.Target.Host
		if multiCluster {
			host += " · " + a.list.shortName(r.Target.Cluster)
		}
		for col, text := range []string{host, r.Status.String(), exit, dur} {
			v.table.SetCell(i+1, col, tview.NewTableCell(tview.Escape(text)).SetTextColor(color).SetExpansion(1))
		}
	}
	v.table.Select(min(max(sel, 1), len(v.results)), 0)
	state := fmt.Sprintf("running %d/%d done · x cancel", done, len(v.results))
	if !v.running {
		state = fmt.Sprintf("finished: %d ok, %d not ok · e export · esc close", ok, len(v.results)-ok)
	}
	v.frame.SetTitle(" <Multi-exec> " + tview.Escape(truncate(v.command, 40)) + " · " + state + " ")
	v.renderOutput()
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func (v *execView) renderOutput() {
	a := v.a
	row, _ := v.table.GetSelection()
	if row < 1 || row > len(v.results) {
		return
	}
	r := v.results[row-1]
	v.output.SetTitle(" " + tview.Escape(r.Target.Login+"@"+r.Target.Name()) + " ")
	var b strings.Builder
	if r.Err != nil {
		fmt.Fprintf(&b, "[%s]%s[-]\n", tag(a.theme.Err), tview.Escape(r.Err.Error()))
	}
	if len(r.Stdout) > 0 {
		b.WriteString(tview.Escape(tsh.CleanOutput(r.Stdout)) + "\n")
	}
	if len(r.Stderr) > 0 {
		fmt.Fprintf(&b, "[%s]%s[-]\n", tag(a.theme.Warn), tview.Escape(tsh.CleanOutput(r.Stderr)))
	}
	if r.Truncated {
		fmt.Fprintf(&b, "[%s](output truncated)\n", tag(a.theme.DialogPlaceholder))
	}
	if b.Len() == 0 {
		fmt.Fprintf(&b, "[%s](%s, no output)", tag(a.theme.DialogPlaceholder), r.Status)
	}
	v.output.SetText(b.String()).ScrollToBeginning()
}

func (v *execView) key(ev *tcell.EventKey) *tcell.EventKey {
	a := v.a
	switch {
	case ev.Key() == tcell.KeyEscape || ev.Rune() == 'q':
		if v.running {
			a.flash("Still running: x cancels it.", a.theme.Warn)
			return nil
		}
		a.closeDialog("exec")
		return nil
	case ev.Rune() == 'x':
		if v.running && v.cancel != nil {
			v.cancel()
			a.flash("Cancelling multi-exec…", a.theme.Warn)
		}
		return nil
	case ev.Rune() == 'e':
		if v.running {
			a.flash("Wait for the run to finish, or cancel it with x.", a.theme.Warn)
			return nil
		}
		def := "~/tstui-exec-" + v.started.Format("20060102-150405") + ".txt"
		a.closeDialog("exec")
		a.prompt("exec-export", "<Export results>", "File ", def, func(s string) []string {
			return transfer.CompleteLocal(s, a.home)
		}, func(path string) {
			a.exportExec(v, path)
		})
		return nil
	}
	return ev
}

// exportExec writes the results to path (mode 0600) and shows the view
// again.
func (a *App) exportExec(v *execView, path string) {
	defer v.show()
	p, err := transfer.LocalPath(path, a.home)
	if err != nil {
		a.flash("Export: "+err.Error(), a.theme.Err)
		return
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		a.flash("Export: "+err.Error(), a.theme.Err)
		return
	}
	werr := multiexec.Export(f, v.command, v.started, v.results)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		a.flash("Export: "+werr.Error(), a.theme.Err)
		return
	}
	a.flash("Results exported to "+p+".", a.theme.OK)
}
