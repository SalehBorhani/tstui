package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/health"
	"github.com/root-ali/tstui/internal/palette"
	"github.com/root-ali/tstui/internal/tsh"
)

// proxyKey normalises a proxy address so "host" and "host:443" match.
func proxyKey(proxy string) string {
	if k, err := tsh.WebAddr(proxy); err == nil {
		return k
	}
	return proxy
}

// configProxyName is the config name of proxy, if the config has it.
func (a *App) configProxyName(proxy string) (string, bool) {
	key := proxyKey(proxy)
	for _, p := range a.o.Config.Proxies {
		if proxyKey(p.Addr) == key {
			return p.Name, true
		}
	}
	return "", false
}

// --- proxies ---

// openProxyPicker lists the known proxies with their health; Enter
// switches to one.
func (a *App) openProxyPicker() {
	entries := a.proxyEntries()
	if len(entries) == 0 {
		a.flash("No proxies known yet: log in to one (l).", a.theme.Info)
		return
	}
	items := make([]string, len(entries))
	cur := 0
	now := time.Now()
	for i, p := range entries {
		mark := "[ ]"
		if p.Current {
			mark, cur = "[x]", i
		}
		name := p.Addr
		if p.Name != p.Addr {
			name = p.Name + " (" + p.Addr + ")"
		}
		items[i] = tview.Escape(mark+" "+name) + "  " + a.header.proxyState(p.Health, now)
	}
	a.pickFrom("proxies", "<Proxies>", items, cur, func(i int) { a.switchProxyTo(entries[i].Addr) })
}

// switchProxyByName switches to a proxy given by config name, profile
// name or address.
func (a *App) switchProxyByName(name string) {
	for _, p := range a.proxyEntries() {
		if p.Name == name || p.Addr == name {
			a.switchProxyTo(p.Addr)
			return
		}
	}
	if strings.HasPrefix(name, "-") || strings.ContainsAny(name, " /@") {
		a.flash(fmt.Sprintf("Unknown proxy %q.", name), a.theme.Err)
		return
	}
	a.switchProxyTo(name) // an address not seen before
}

// switchProxyTo makes proxy current, with the user from the config or its
// tsh profile. Open sessions keep running on their own proxy.
func (a *App) switchProxyTo(proxy string) {
	if proxyKey(proxy) == proxyKey(a.info.Proxy) {
		return
	}
	user := ""
	if name, ok := a.configProxyName(proxy); ok {
		if p, ok := a.o.Config.ProxyByName(name); ok {
			user = p.User
		}
	}
	if user == "" {
		if p, ok := profileFor(proxy, a.profiles); ok {
			user = p.User
		}
	}
	a.switchProxy(proxy, user)
	if a.monitor != nil {
		a.monitor.Refresh()
	}
}

// --- favorites ---

// loadFavorites reads the favorites of every configured proxy.
func (a *App) loadFavorites() {
	a.favs = map[string]map[string]bool{}
	for name, hosts := range a.o.Config.Favorites {
		p, ok := a.o.Config.ProxyByName(name)
		if !ok {
			continue
		}
		set := map[string]bool{}
		for _, h := range hosts {
			set[h] = true
		}
		a.favs[proxyKey(p.Addr)] = set
	}
}

// favoritesFor is the favorite set of proxy (created when missing).
func (a *App) favoritesFor(proxy string) map[string]bool {
	key := proxyKey(proxy)
	if a.favs[key] == nil {
		a.favs[key] = map[string]bool{}
	}
	return a.favs[key]
}

// toggleFavorite stars or unstars host on the current proxy and saves it
// to the config when the proxy is configured there.
func (a *App) toggleFavorite(host string) {
	if host == "" || a.info.Proxy == "" {
		return
	}
	set := a.favoritesFor(a.info.Proxy)
	on := !set[host]
	if on {
		set[host] = true
	} else {
		delete(set, host)
	}
	a.list.setFavorites(set)
	verb := "Added"
	if !on {
		verb = "Removed"
	}
	name, ok := a.configProxyName(a.info.Proxy)
	if !ok {
		a.flash(fmt.Sprintf("%s favorite %s (this session only: the proxy is not in config.yaml).", verb, host), a.theme.Info)
		return
	}
	hosts := make([]string, 0, len(set))
	for h := range set {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	if len(hosts) == 0 {
		delete(a.o.Config.Favorites, name)
	} else {
		a.o.Config.Favorites[name] = hosts
	}
	if err := a.saveConfig(); err != nil {
		a.flash(fmt.Sprintf("%s favorite %s, but could not save it: %v", verb, host, err), a.theme.Err)
		return
	}
	a.flash(fmt.Sprintf("%s favorite %s.", verb, host), a.theme.OK)
}

// errNoConfigPath means tstui was started without a config path.
var errNoConfigPath = errors.New("no config file path")

// saveConfig writes the config file. The first save of a run keeps a copy
// of the previous file as config.yaml.bak, since saving rewrites the file
// and drops comments.
func (a *App) saveConfig() error {
	path := a.o.ConfigPath
	if path == "" {
		return errNoConfigPath
	}
	if !a.configBackedUp {
		if err := copyFile(path, path+".bak"); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("back up %s: %w", path, err)
		}
		a.configBackedUp = true
	}
	return config.Save(path, a.o.Config)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, config.FileMode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// --- skins ---

// setSkin switches the colours now and saves the choice.
func (a *App) setSkin(name string) {
	t, ok := a.o.Skins.Theme(name)
	if !ok {
		a.flash(fmt.Sprintf("Unknown skin %q; skins: %s.", name, strings.Join(a.o.Skins.Names(), ", ")), a.theme.Err)
		return
	}
	a.applyTheme(t)
	a.o.Config.Skin = name
	if err := a.saveConfig(); err != nil && !errors.Is(err, errNoConfigPath) {
		a.flash(fmt.Sprintf("Skin %s applied, but not saved: %v", name, err), a.theme.Warn)
		return
	}
	a.flash("Skin "+name+".", a.theme.OK)
}

// applyTheme recolours the running UI. Dialogs opened later pick the new
// colours up when they are built.
func (a *App) applyTheme(t Theme) {
	a.theme = t
	t.apply()
	a.header.theme = t
	a.footer.theme = t
	a.tabs.theme = t
	a.panes.theme = t
	a.panes.SetBackgroundColor(t.Bg)
	for _, v := range append(a.allViews(), a.panes.empty) {
		v.theme = t
		v.SetTitleColor(t.Title).SetBackgroundColor(t.Bg)
	}
	a.list.applyTheme(t)
	a.styleCmdLine(a.cmdLine)
	a.list.rerender()
	a.focusChanged(a.focus)
	a.sessionsChanged()
}

// --- help ---

// openHelp shows every key and palette command.
func (a *App) openHelp() {
	if !a.canOpenDialog() {
		return
	}
	k := func(key, text string) string {
		return fmt.Sprintf("  [%s::b]%-18s[%s::-]%s\n", tag(a.theme.MenuKey), tview.Escape(key), tag(a.theme.DialogFg), tview.Escape(text))
	}
	h := func(title string) string { return fmt.Sprintf("\n[%s::b]%s[-::-]\n", tag(a.theme.Title), title) }
	p := a.prefix.label
	var b strings.Builder
	b.WriteString(h("Server list"))
	for _, kv := range [][2]string{
		{"enter", "connect (or show its open session)"}, {"n / shift-enter", "open another session"},
		{"/", "search servers as you type (text, key=value, key!=value)"}, {":", "command palette"},
		{"space", "mark / unmark server (for :exec)"}, {"a", "mark all shown servers (again: unmark)"},
		{"f", "toggle favorite (pinned at the top)"}, {"N / A / C", "sort by name / address / cluster (again: reverse)"},
		{"u / d", "upload / download files (scp)"},
		{"0-9", "show one cluster's servers (0: all clusters)"}, {"p", "proxies"}, {"r", "refresh servers, login and health"}, {"l / L", "log in (L: re-login from a notice)"},
		{"o", "log out"}, {"tab", "go to the terminal"}, {"alt-1..9", "show tab 1-9"},
		{"| / -", "open the server as a split right / below"}, {"F", "fullscreen terminal"},
		{"esc", "clear marks, then the notice"}, {"?", "this help"}, {"q / ctrl-c", "quit"},
	} {
		b.WriteString(k(kv[0], kv[1]))
	}
	b.WriteString(h("Terminal (prefix " + p + ")"))
	for _, kv := range [][2]string{
		{p + " tab", "back to the server list"}, {p + " 1-9", "show tab 1-9"}, {p + " n / p", "next / previous tab"},
		{p + " | or %", "split: a server right of this pane"}, {p + " - or \"", "split: a server below this pane"},
		{p + " ←↑↓→", "go to the pane in that direction"}, {p + " o", "next pane"},
		{p + " z", "zoom the pane (again: all panes)"}, {p + " f", "fullscreen (nothing but the terminal)"},
		{p + " c", "close the pane (the tab goes with its last)"},
		{p + " d", "detach"}, {p + " r", "reconnect"}, {p + " [", "scroll mode (q leaves)"},
		{p + " :", "command palette"}, {p + " ?", "this help"},
		{p + " l", "log in"}, {p + " q", "quit"}, {p + " " + p, "send " + p + " itself"}, {"alt-1..9", "show tab 1-9"},
	} {
		b.WriteString(k(kv[0], kv[1]))
	}
	b.WriteString(h("Commands (: then tab completes, ↑↓ history)"))
	for _, c := range palette.Commands {
		name := ":" + c.Name
		if c.Usage != "" {
			name += " " + c.Usage
		}
		help := c.Help
		if len(c.Aliases) > 0 {
			help += " (also :" + strings.Join(c.Aliases, ", :") + ")"
		}
		fmt.Fprintf(&b, "  [%s::b]%-20s[%s::-]%s\n", tag(a.theme.MenuKey), tview.Escape(name), tag(a.theme.DialogFg), tview.Escape(help))
	}
	b.WriteString(h("Port forwards (:pf)"))
	b.WriteString(k("n", "new forward") + k("enter", "start a saved forward") + k("s", "stop") +
		k("x", "remove an ended forward") + k("f", "save as favorite") + k("D", "delete a saved forward") + k("esc", "close"))
	b.WriteString(h("Multi-exec results"))
	b.WriteString(k("↑↓", "pick a server, its output on the right") + k("x", "cancel the run") +
		k("e", "export results to a file") + k("esc", "close (when finished)"))

	text := a.newText().SetWrap(false).SetText(b.String())
	text.SetDoneFunc(func(tcell.Key) { a.closeDialog("help") })
	text.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		if ev.Rune() == 'q' || ev.Rune() == '?' {
			a.closeDialog("help")
			return nil
		}
		return ev
	})
	a.openDialog("help", newViewLayout(a.framed("<Help> esc closes · ↑↓ scroll", text), a.theme.Bg, 80, 70, 20), text)
}

// --- health and expiry ---

// healthProxies is what the monitor checks; it runs on the monitor's
// goroutine, so it reads a copy kept under a lock.
func (a *App) healthProxies() []string {
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	return append([]string(nil), a.healthTargets...)
}

func (a *App) setHealthTargets(entries []proxyEntry) {
	a.healthMu.Lock()
	defer a.healthMu.Unlock()
	a.healthTargets = a.healthTargets[:0]
	for _, e := range entries {
		a.healthTargets = append(a.healthTargets, e.Addr)
	}
}

// runHealth runs the monitor until the app stops.
func (a *App) runHealth() {
	defer a.bgDone.Done()
	a.monitor.Run(a.ctx, a.healthProxies, func(r health.Report) {
		a.post(func() { a.onHealth(r) })
	})
}

// onHealth stores a proxy's report and updates the header.
func (a *App) onHealth(r health.Report) {
	a.healthReports[proxyKey(r.Proxy)] = r
	if proxyKey(r.Proxy) == proxyKey(a.info.Proxy) {
		a.info.HealthChecked = r.Checked
		if r.Reachable {
			a.info.PingErr = nil
			a.info.Latency = r.Latency
			a.info.ServerVersion, a.info.MinClientVersion = r.ServerVersion, r.MinClientVersion
			if a.list.stale != "" && !a.serversLoading && a.loggedInNow() {
				a.loadServers() // the proxy is back: replace the stale list
			}
		} else {
			a.info.PingErr = r.Err
		}
	}
	a.renderHeader()
}

// loggedInNow reports whether the current proxy's certificate is valid.
func (a *App) loggedInNow() bool {
	return a.info.State == auth.CertValid || a.info.State == auth.CertExpiring
}

// checkCompat shows the full version warning once per proxy and version
// pair; the header keeps the short form.
func (a *App) checkCompat() {
	cp := health.CheckVersions(a.info.ClientVersion, a.info.ServerVersion, a.info.MinClientVersion)
	key := a.info.Proxy + "|" + a.info.ClientVersion + "|" + a.info.ServerVersion + "|" + a.info.MinClientVersion
	if cp.Level != health.CompatWarn || a.compatShown[key] {
		return
	}
	a.compatShown[key] = true
	a.showToast("compat", "⚠ "+cp.Message, tcell.ColorYellow, toastFor)
}

// checkExpiry raises a notice when the certificate crosses 30 minutes,
// 5 minutes and expiry; a new certificate clears it.
func (a *App) checkExpiry(now time.Time) {
	var until time.Time
	switch a.info.State {
	case auth.CertValid, auth.CertExpiring, auth.CertExpired:
		if !a.info.Checking {
			until = a.info.ValidUntil
		}
	}
	lvl := a.expiry.Update(until, now)
	if until.IsZero() || health.LevelFor(until.Sub(now)) == health.ExpiryOK {
		a.dismissToast("expiry")
		return
	}
	text, color := a.expiryText(until, now)
	switch lvl {
	case health.ExpirySoon:
		a.showToast("expiry", text, color, toastFor)
	case health.ExpiryImminent, health.ExpiryPast:
		a.showToast("expiry", text, color, 0) // stays until a re-login or esc
	default:
		if a.toastKind == "expiry" {
			a.toastText, a.toastColor = text, color // keep the countdown live
		}
	}
}

func (a *App) expiryText(until, now time.Time) (string, tcell.Color) {
	hint := "press L to log in again (" + a.prefix.label + " l in a terminal)"
	left := until.Sub(now)
	switch health.LevelFor(left) {
	case health.ExpiryPast:
		return "✘ Certificate expired · " + hint, a.theme.Err
	case health.ExpiryImminent:
		return "⚠ Certificate expires in " + countdown(left) + "! " + hint, a.theme.Err
	}
	return "⚠ Certificate expires in " + countdown(left) + " · " + hint, tcell.ColorYellow
}
