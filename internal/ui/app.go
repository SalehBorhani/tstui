package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/config"
	"github.com/root-ali/tstui/internal/health"
	"github.com/root-ali/tstui/internal/layout"
	"github.com/root-ali/tstui/internal/multiexec"
	"github.com/root-ali/tstui/internal/palette"
	"github.com/root-ali/tstui/internal/portfwd"
	"github.com/root-ali/tstui/internal/session"
	"github.com/root-ali/tstui/internal/transfer"
	"github.com/root-ali/tstui/internal/tsh"
)

// Authenticator is what the application needs from auth.Service.
type Authenticator interface {
	Loginer
	Logout(ctx context.Context, proxy, user string) error
}

// Options configures NewApp.
type Options struct {
	Version string
	Client  tsh.Client
	Auth    Authenticator
	Config  *config.Config
	// Creds are the login values from flags, environment and config. The
	// app owns their secrets: a password is used for the first login only.
	Creds    auth.Credentials
	Insecure bool
	Logger   *slog.Logger
	Theme    *Theme
	// Skins are the skins Config.Skin and :skin choose from; default the
	// built-in ones.
	Skins *Skins
	// ExitAfterLogin stops the app as soon as the proxy is logged in (the
	// --login-only mode); see LoginResult.
	ExitAfterLogin bool
	// WatchInterval is how often `tsh status` is polled; default 30s.
	WatchInterval time.Duration
	// Spawner starts session programs; default `tsh ssh` through Client.
	Spawner session.Spawner
	// DemoShell (hidden --demo-shell) opens a local shell tab at start, and
	// Enter on a server opens one too instead of `tsh ssh`, so the
	// terminal can be tried without a cluster. Spawner should then be a
	// session.ShellSpawner.
	DemoShell bool
	// Logins remembers the login chosen per server; default in memory.
	Logins *session.LoginMemory
	// ConfigPath is where favorites, saved port forwards and the skin are
	// saved; empty keeps such changes in memory.
	ConfigPath string
	// StateDir holds the multi-exec audit log and the transfer log; empty
	// disables both files.
	StateDir string
	// HealthInterval is how often every proxy is checked; default 60s.
	HealthInterval time.Duration
	// Home expands ~ in local paths; default the user's home directory.
	Home string
}

// App is the tstui terminal UI: header, server list, the terminal with
// its tabs, and footer, with the login dialog on top when the current
// proxy needs a login.
//
// Startup: the proxy comes from the command line or config, else from the
// current tsh profile in ~/.tsh, and the user likewise. A valid profile
// is used as is. An expired or missing one opens the login dialog with
// what is known filled in; with no proxy at all, the dialog asks for it.
type App struct {
	o      Options
	theme  Theme
	tv     *tview.Application
	pages  *tview.Pages
	header *header
	list   *serverList
	footer *footer
	body   *tview.Flex // server list | right
	right  *tview.Flex // tab bar over the panes
	tabs   *tabBar
	panes  *paneView                          // the active tab's sessions
	views  map[*session.Session]*TerminalView // one per session
	logger *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	posts    chan func()
	quit     chan struct{}
	quitOnce sync.Once
	pumpDone chan struct{}
	redraw   chan struct{} // a session changed; coalesced
	bgDone   sync.WaitGroup
	crash    atomic.Pointer[PanicError] // the first panic; see goSafe

	// Sessions, owned by the UI goroutine.
	sessions       *session.Manager
	sessUI         map[*session.Session]*sessionUI
	prefix         prefixKey
	focus          focusArea
	prefixPending  bool
	termShown      bool
	fullscreen     bool            // only the panes are shown
	dialog         tview.Primitive // login picker or quit confirmation
	quitDialog     bool
	pendingConnect func() // run after the next successful login
	flashText      string
	flashColor     tcell.Color
	flashUntil     time.Time
	flashTimer     *time.Timer
	reconnectBase  time.Duration // first auto-reconnect delay (tests shorten it)

	// State below is owned by the UI goroutine.
	info headerInfo
	// proxyGen counts switchProxy calls. Results of tsh calls made for a
	// proxy carry the generation they were started in and are dropped
	// once it is stale: after A -> B -> A, the proxy name alone would let
	// an old listing or an old watcher's event through.
	proxyGen       uint64
	profiles       []tsh.Profile
	watcherStop    context.CancelFunc
	watcher        *auth.Watcher
	modal          *LoginModal
	serversLoaded  bool
	serversLoading bool
	// clusters are the current proxy's clusters (nil until listed, or
	// when tsh cannot list them); clusterErrs says which failed to list.
	clusters    []tsh.Cluster
	clusterErrs map[string]error

	loginErr error // result for ExitAfterLogin
	loggedIn bool
	status   tsh.Status

	// Extras, owned by the UI goroutine unless noted.
	mainFlex    *tview.Flex
	cmdLine     *tview.InputField
	history     *palette.History
	paletteOpen bool
	dialogName  string

	toastKind, toastText string
	toastColor           tcell.Color
	toastUntil           time.Time
	expiry               health.ExpiryAlerts

	monitor       *health.Monitor
	healthMu      sync.Mutex // guards healthTargets, read by the monitor
	healthTargets []string
	healthReports map[string]health.Report

	compatShown    map[string]bool            // version warnings already shown
	favs           map[string]map[string]bool // proxy key -> hostnames
	configBackedUp bool

	cache     tsh.ServerCache // last good listing per proxy, for offline use
	pf        *portfwd.Manager
	pfView    *pfView
	transfers *transfer.Log
	audit     *multiexec.Audit
	execView  *execView
	home      string
}

// NewApp builds the application on screen. Call Run to start it.
func NewApp(screen tcell.Screen, o Options) *App {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if o.Config == nil {
		o.Config = config.Default()
	}
	if o.Skins == nil {
		o.Skins = BuiltinSkins()
	}
	theme, ok := o.Skins.Theme(o.Config.Skin)
	if !ok {
		o.Logger.Warn("unknown skin, using the default", "skin", o.Config.Skin)
	}
	if o.Theme != nil {
		theme = *o.Theme
	}
	theme.apply()
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir()
	}
	if o.Spawner == nil {
		o.Spawner = session.TshSpawner{Client: o.Client, Insecure: o.Insecure}
	}
	if o.Logins == nil {
		o.Logins, _ = session.LoadLogins("")
	}
	a := &App{
		o:        o,
		theme:    theme,
		tv:       tview.NewApplication().SetScreen(screen),
		header:   newHeader(theme),
		list:     newServerList(theme),
		footer:   newFooter(theme),
		tabs:     newTabBar(theme),
		panes:    newPaneView(theme),
		views:    map[*session.Session]*TerminalView{},
		logger:   o.Logger,
		posts:    make(chan func(), 256),
		quit:     make(chan struct{}),
		pumpDone: make(chan struct{}),
		redraw:   make(chan struct{}, 1),
		sessUI:   map[*session.Session]*sessionUI{},
		prefix:   parsePrefix(o.Config.Keybindings.Focus),

		reconnectBase: reconnectBase,

		history:       palette.NewHistory(0),
		healthReports: map[string]health.Report{},
		compatShown:   map[string]bool{},
		monitor:       health.NewMonitor(o.Client, o.HealthInterval),
		transfers:     transfer.NewLog(statePath(o.StateDir, "transfers.log")),
		cache:         tsh.ServerCache{Dir: statePath(o.StateDir, "servers")},
		audit:         multiexec.NewAudit(statePath(o.StateDir, "audit.log")),
		home:          o.Home,
	}
	a.pf = portfwd.NewManager(o.Client, portfwd.Options{
		Logger: o.Logger,
		// OnChange runs on the manager's goroutines: never block there.
		OnChange: func() { a.postNoWait(a.pfChanged) },
	})
	a.loadFavorites()
	a.cmdLine = a.newCmdLine()
	a.info = headerInfo{Version: o.Version, Proxy: o.Creds.Proxy, User: o.Creds.User, Checking: true}
	a.sessions = session.NewManager(o.Spawner, session.Options{
		Cols: 80, Rows: 24,
		Scrollback: o.Config.Terminal.Scrollback,
		OnChange:   a.sessionChanged,
		Logger:     o.Logger,
	})
	a.panes.SetBackgroundColor(theme.Bg)
	a.panes.tab = a.sessions.ActiveTab
	a.panes.view = a.viewFor
	a.panes.focused = func() bool { return a.focus == focusTerm }
	a.panes.onSize = a.sessions.SetSize
	a.panes.badge = a.paneBadge
	a.panes.empty.onFocus = func() { a.setFocus(focusTerm) }
	a.panes.empty.SetFocusFunc(func() { a.focusChanged(focusTerm) })
	a.panes.empty.SetPlaceholder("No session open.\nPick a server and press Enter.")

	a.right = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.tabs.view, 1, 0, false).
		AddItem(a.panes, 0, 1, false)
	a.body = tview.NewFlex().
		AddItem(a.list.table, 0, 1, true).
		AddItem(a.right, 0, 0, false)
	main := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.header.view, headerHeight, 0, false).
		AddItem(a.cmdLine, 0, 0, false). // the palette; shown by openPalette
		AddItem(a.body, 0, 1, true).
		AddItem(a.footer.view, 1, 0, false)
	a.mainFlex = main
	a.pages = tview.NewPages().AddPage("main", newToastLayer(main, a.currentToast, a.toastTop), true, true)
	a.tv.SetRoot(&safeRoot{Primitive: a.pages, a: a}, true)
	a.tv.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		defer a.recoverPanic()
		return a.onKey(ev)
	})
	a.tv.EnablePaste(true)
	if o.Config.Terminal.MouseEnabled() {
		a.tv.EnableMouse(true)
	}
	a.list.table.SetSelectedFunc(func(int, int) { a.connect(modeReuse) })
	a.list.table.SetFocusFunc(func() { a.focusChanged(focusList) })
	a.setFocus(focusList)
	a.tabs.render(a.sessions)
	a.renderHeader()
	return a
}

// Run shows the UI until the user quits or ctx is done.
func (a *App) Run(ctx context.Context) error {
	a.ctx, a.cancel = context.WithCancel(ctx)
	defer a.cancel()
	stopOnCancel := context.AfterFunc(ctx, a.shutdown)
	defer stopOnCancel()

	go a.pump()
	a.bgDone.Add(1)
	a.goSafe(a.redrawLoop)
	a.goSafe(a.bootstrap)
	a.goSafe(a.tick)
	a.bgDone.Add(1)
	a.goSafe(a.runHealth)
	if a.o.DemoShell {
		a.openSession(session.Spec{Proxy: "demo", Host: "local", Login: os.Getenv("USER"), Name: "demo"}, modeNewTab)
	}
	uiPanicked, err := a.runEventLoop()

	// Clean up even after a panic: child processes must not outlive tstui,
	// and secrets are wiped.
	a.quitOnce.Do(func() { close(a.quit) })
	a.cancel()
	if a.modal != nil {
		a.modal.Close()
	}
	for s := range a.sessUI {
		a.forget(s)
	}
	a.stopFlash()
	a.sessions.CloseAll()
	a.pf.StopAll() // port forwards end with tstui
	a.o.Creds.Wipe()
	if uiPanicked {
		// The event loop is gone, so pump may wait forever on an update
		// it queued; do not wait for it, or for goroutines posting.
		waitTimeout(a.bgDone.Wait, time.Second)
	} else {
		a.bgDone.Wait()
		<-a.pumpDone
	}
	if p := a.crash.Load(); p != nil {
		return p
	}
	if err != nil {
		return fmt.Errorf("run UI: %w", err)
	}
	return nil
}

// LoginResult reports, after Run, whether the proxy ended up logged in
// (for --login-only). err is auth.ErrCancelled when the user gave up.
func (a *App) LoginResult() (proxy string, st tsh.Status, err error) {
	if a.loggedIn {
		return a.info.Proxy, a.status, nil
	}
	if a.loginErr != nil {
		return a.info.Proxy, a.status, a.loginErr
	}
	return a.info.Proxy, a.status, auth.ErrCancelled
}

// post runs f on the UI goroutine, in order, without ever blocking the
// caller once the app has stopped.
func (a *App) post(f func()) {
	select {
	case a.posts <- f:
	case <-a.quit:
	}
}

// postNoWait is post for callers that must not wait, such as timers: f is
// dropped only if the UI is stopping or hopelessly behind.
func (a *App) postNoWait(f func()) {
	select {
	case a.posts <- f:
	case <-a.quit:
	default:
		a.goSafe(func() { a.post(f) })
	}
}

// pump hands posted functions to tview. QueueUpdateDraw waits until the
// UI goroutine has run f, so the event loop must keep running until pump
// has returned: see shutdown.
func (a *App) pump() {
	defer close(a.pumpDone)
	for {
		select {
		case f := <-a.posts:
			a.tv.QueueUpdateDraw(func() {
				defer a.recoverPanic()
				f()
			})
		case <-a.quit:
			return
		}
	}
}

// shutdown stops the application from any goroutine. It first stops pump
// and lets the event loop run the update pump may be waiting on; stopping
// tview at once could leave pump blocked forever.
func (a *App) shutdown() {
	a.quitOnce.Do(func() { close(a.quit) })
	go func() {
		<-a.pumpDone
		a.tv.Stop()
	}()
}

// sessionChanged is the sessions' OnChange: it only signals redrawLoop,
// so it never blocks a session goroutine.
func (a *App) sessionChanged(*session.Session) {
	select {
	case a.redraw <- struct{}{}:
	default:
	}
}

// redrawLoop turns session changes into at most one redraw per frame.
func (a *App) redrawLoop() {
	defer a.bgDone.Done()
	const frame = 16 * time.Millisecond
	for {
		select {
		case <-a.redraw:
			a.post(a.sessionsChanged)
			select {
			case <-time.After(frame):
			case <-a.quit:
				return
			}
		case <-a.quit:
			return
		}
	}
}

// tick redraws the header every second for the certificate countdown.
func (a *App) tick() {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			select {
			case a.posts <- a.everySecond:
			default: // the UI is busy; skip this tick
			}
		case <-a.quit:
			return
		}
	}
}

// everySecond updates the countdown and raises expiry notices.
func (a *App) everySecond() {
	a.checkExpiry(time.Now())
	a.checkCompat()
	a.renderHeader()
}

// pfChanged redraws the port forward view after a forward changed.
func (a *App) pfChanged() {
	if a.pfView != nil {
		a.pfView.render()
	}
}

// statePath is name in dir, or "" when dir is "".
func statePath(dir, name string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// bootstrap finds the tsh version, the profiles and the proxy to use.
func (a *App) bootstrap() {
	ctx := a.ctx
	v, verr := a.o.Client.Version(ctx)
	profiles, perr := a.o.Client.Profiles()
	if perr != nil {
		a.logger.Warn("read tsh profiles", "err", perr)
	}
	proxy, user := resolveProxy(a.o.Creds.Proxy, a.o.Creds.User, profiles)
	a.post(func() {
		if verr != nil {
			a.info.ClientVersion = "not found"
			a.list.setMessage("tsh is required: "+verr.Error()+". Install it from https://goteleport.com/download/", a.theme.Err)
		} else {
			a.info.ClientVersion = v.Version
			if c := health.CheckClient(v.Version); c.Level == health.CompatWarn {
				a.showToast("tsh-version", "⚠ "+c.Message, tcell.ColorYellow, toastFor)
			}
		}
		a.profiles = profiles
		a.switchProxy(proxy, user)
	})
}

// resolveProxy picks the proxy and user: the given ones first, then the
// current tsh profile (or the profile for the given proxy, for the user).
func resolveProxy(proxy, user string, profiles []tsh.Profile) (string, string) {
	if proxy == "" {
		for _, p := range profiles {
			if p.Current {
				return p.WebProxyAddr, firstNonEmpty(user, p.User)
			}
		}
		if len(profiles) == 1 {
			return profiles[0].WebProxyAddr, firstNonEmpty(user, profiles[0].User)
		}
		return "", user
	}
	if user == "" {
		if p, ok := profileFor(proxy, profiles); ok {
			user = p.User
		}
	}
	return proxy, user
}

func profileFor(proxy string, profiles []tsh.Profile) (tsh.Profile, bool) {
	want, err := tsh.WebAddr(proxy)
	if err != nil {
		return tsh.Profile{}, false
	}
	for _, p := range profiles {
		if got, err := tsh.WebAddr(p.WebProxyAddr); err == nil && got == want {
			return p, true
		}
	}
	return tsh.Profile{}, false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// switchProxy makes proxy current and starts watching its login state;
// the first watch event decides between the server list and the login
// dialog.
func (a *App) switchProxy(proxy, user string) {
	if a.watcherStop != nil {
		a.watcherStop()
		a.watcherStop, a.watcher = nil, nil
	}
	a.proxyGen++
	gen := a.proxyGen
	a.info.Proxy, a.info.User = proxy, user
	a.info.Cluster, a.info.Roles = "", nil
	a.info.ServerVersion, a.info.MFA, a.info.Latency, a.info.PingErr = "", "", 0, nil
	a.info.MinClientVersion, a.info.HealthChecked = "", time.Time{}
	if r, ok := a.healthReports[proxyKey(proxy)]; ok && r.Reachable {
		a.info.Latency, a.info.ServerVersion, a.info.MinClientVersion = r.Latency, r.ServerVersion, r.MinClientVersion
		a.info.HealthChecked = r.Checked
	}
	a.info.Checking = proxy != ""
	a.serversLoaded, a.serversLoading = false, false
	a.clusters, a.clusterErrs = nil, nil
	a.list.view = ""
	a.list.setClusters(nil)
	a.list.marks = map[string]bool{}
	a.list.favs = a.favoritesFor(proxy)
	a.dismissToast("expiry")
	a.renderHeader()

	if proxy == "" {
		a.list.setMessage("No proxy yet: log in to one.", a.theme.DialogPlaceholder)
		a.showLogin("No Teleport proxy found in ~/.tsh or the config. Enter one.")
		return
	}
	a.list.setMessage("Checking the login for "+proxy+"…", a.theme.DialogPlaceholder)
	a.logger.Info("proxy selected", "proxy", proxy, "user", user)

	a.goSafe(func() { a.ping(gen, proxy) })
	ctx, stop := context.WithCancel(a.ctx)
	w := auth.NewWatcher(a.o.Client, proxy, auth.WatchOptions{Interval: a.o.WatchInterval, Logger: a.logger})
	a.watcherStop, a.watcher = stop, w
	events := w.Run(ctx)
	a.goSafe(func() {
		for ev := range events {
			a.post(func() {
				if gen == a.proxyGen {
					a.onWatch(ev)
				}
			})
		}
	})
}

func (a *App) ping(gen uint64, proxy string) {
	p, err := a.o.Client.ServerPing(a.ctx, proxy)
	a.post(func() {
		if gen != a.proxyGen {
			return
		}
		a.info.PingErr = err
		a.info.HealthChecked = time.Now()
		if err == nil {
			a.info.ServerVersion, a.info.Latency = p.ServerVersion, p.Latency
			a.info.MinClientVersion = p.MinClientVersion
			a.info.MFA = p.SecondFactor
			if p.PreferredLocalMFA != "" && p.PreferredLocalMFA != p.SecondFactor {
				a.info.MFA += "/" + p.PreferredLocalMFA
			}
			if a.info.Cluster == "" {
				a.info.Cluster = p.ClusterName
			}
		}
		a.renderHeader()
	})
}

func (a *App) onWatch(ev auth.WatchEvent) {
	if ev.Proxy != a.info.Proxy {
		return
	}
	a.info.Checking = false
	a.info.State, a.info.StatusErr = ev.State, ev.Err
	if ev.Status.LoggedIn {
		a.status = ev.Status
		a.info.ValidUntil = ev.Status.ValidUntil
		a.info.Cluster, a.info.Roles = ev.Status.Cluster, ev.Status.Roles
		if ev.Status.User != "" {
			a.info.User = ev.Status.User
		}
	}
	a.renderHeader()

	switch ev.State {
	case auth.CertValid, auth.CertExpiring:
		if a.o.ExitAfterLogin {
			a.loggedIn = true
			a.shutdown()
			return
		}
		if !a.serversLoaded && !a.serversLoading {
			a.loadServers()
		}
		if f := a.pendingConnect; f != nil {
			a.pendingConnect = nil
			f()
		}
	case auth.CertExpired:
		a.serversLoaded = false
		a.loginNeeded("Your certificate has expired. Press l to log in.",
			fmt.Sprintf("Session for %s expired. Log in again.", a.info.User))
	case auth.CertLoggedOut:
		a.serversLoaded = false
		a.loginNeeded("Not logged in. Press l to log in.", "Not logged in to "+a.info.Proxy+".")
	case auth.CertUnknown:
		msg := "tsh status failed"
		if ev.Err != nil {
			msg += ": " + ev.Err.Error()
		}
		a.list.setMessage(msg, a.theme.Err)
	}
}

// loginNeeded reacts to a missing or expired login. With sessions open,
// nothing is killed and no dialog pops up over the user's terminal: the
// header warns, the server list stays, and l (or prefix l) logs in.
// Otherwise the dialog opens.
func (a *App) loginNeeded(listMsg, dialogMsg string) {
	if a.sessions.Len() > 0 {
		if a.list.servers == nil {
			a.list.setMessage(listMsg, a.theme.Warn)
		}
		a.flash("Login needed: open sessions keep running. Press l (or "+a.prefix.label+" l) to log in.", a.theme.Warn)
		return
	}
	a.list.setMessage(listMsg, a.theme.Warn)
	a.showLogin(dialogMsg)
}

// showLogin opens the login dialog unless it is already open.
func (a *App) showLogin(reason string) {
	if a.modal != nil || a.dialog != nil {
		return
	}
	m := newLoginModal(a.tv, a.o.Auth, a.theme, auth.LoginRequest{
		Proxy:         a.info.Proxy,
		AuthConnector: a.o.Creds.AuthConnector,
		Insecure:      a.o.Insecure,
	}, a.loginDone, a.goSafe)
	a.modal = m
	m.SetMessage(reason)
	m.SetUser(a.info.User)
	if !a.o.Creds.OTP.Empty() {
		m.SetOTP(a.o.Creds.OTP)
		a.o.Creds.OTP = nil
	}
	a.pages.AddPage("login", m.Primitive(), false, true)
	m.FocusFirstEmpty()
	if !a.o.Creds.Password.Empty() && a.info.Proxy != "" {
		// The command line gave the password: use it once, at once.
		m.Start(a.info.User, a.o.Creds.Password, nil)
		a.o.Creds.Password = nil
	}
}

func (a *App) loginDone(res LoginResult, err error) {
	a.removePage("login")
	a.modal = nil
	a.setFocus(a.focus)
	if err != nil {
		a.pendingConnect = nil
		a.logger.Info("login dialog closed", "result", err.Error())
		if a.o.ExitAfterLogin {
			a.loginErr = err
			a.shutdown()
		}
		return
	}
	a.logger.Info("logged in", "proxy", res.Proxy, "user", res.User)
	if res.Proxy != a.info.Proxy || a.watcher == nil {
		a.goSafe(a.reloadProfiles)
		a.switchProxy(res.Proxy, res.User)
		return
	}
	a.info.User = firstNonEmpty(res.User, a.info.User)
	a.info.Checking = true
	a.serversLoaded = false
	a.renderHeader()
	a.watcher.Refresh()
}

func (a *App) reloadProfiles() {
	profiles, err := a.o.Client.Profiles()
	if err != nil {
		return
	}
	a.post(func() {
		a.profiles = profiles
		a.renderHeader()
	})
}

func (a *App) loadServers() {
	proxy, gen := a.info.Proxy, a.proxyGen
	a.serversLoading = true
	a.list.setMessage("Loading servers…", a.theme.DialogPlaceholder)
	a.renderHeader()
	a.goSafe(func() {
		listing, err := tsh.ListAll(a.ctx, a.o.Client, proxy)
		listedAt := time.Now()
		// Disk I/O stays off the UI goroutine: save a good listing, or
		// fetch the last one when the proxy cannot give a new one.
		var cached tsh.Listing
		var cachedAt time.Time
		cacheErr := tsh.ErrNoCache
		switch {
		case err == nil:
			if serr := a.cache.Save(proxy, listing, listedAt); serr != nil {
				a.logger.Warn("server cache", "err", serr)
			}
		case !errors.Is(err, tsh.ErrNotLoggedIn) && a.ctx.Err() == nil:
			cached, cachedAt, cacheErr = a.cache.Load(proxy)
		}
		a.post(func() {
			if gen != a.proxyGen {
				return
			}
			a.serversLoading = false
			a.clusters, a.clusterErrs = listing.Clusters, listing.Errs
			a.list.setClusters(listing.Clusters)
			defer a.renderHeader()
			if err != nil && cacheErr == nil {
				// Degraded mode: what was there last time, clearly
				// marked; r (or the proxy coming back) lists again.
				a.clusters = cached.Clusters
				a.list.setClusters(cached.Clusters)
				a.list.setStale(cached.Servers, cachedAt, err.Error())
				a.flash("Cannot list servers ("+err.Error()+"): showing the list from "+
					cachedAt.Local().Format(time.DateTime)+". r retries.", a.theme.Warn)
				return
			}
			if err != nil {
				a.list.setMessage("Cannot list servers: "+err.Error(), a.theme.Err)
				if errors.Is(err, tsh.ErrNotLoggedIn) && a.watcher != nil {
					a.watcher.Refresh()
				}
				return
			}
			a.serversLoaded = true
			a.list.setServers(listing.Servers, listedAt)
			a.reportClusterErrors(listing)
		})
	})
}

func (a *App) logout() {
	proxy, user := a.info.Proxy, a.info.User
	if proxy == "" {
		return
	}
	a.list.setMessage("Logging out…", a.theme.DialogPlaceholder)
	a.goSafe(func() {
		err := a.o.Auth.Logout(a.ctx, proxy, user)
		a.post(func() {
			if err != nil {
				a.list.setMessage(err.Error(), a.theme.Err)
				return
			}
			a.goSafe(a.reloadProfiles)
			if a.watcher != nil {
				a.watcher.Refresh() // reports logged-out, which opens the dialog
			}
		})
	})
}

func (a *App) onKey(ev *tcell.EventKey) *tcell.EventKey {
	if a.modal != nil || a.dialog != nil {
		if ev.Key() == tcell.KeyCtrlC {
			a.requestQuit() // a second Ctrl+C in the quit dialog quits
			return nil
		}
		return ev // the dialog has every other key
	}
	if a.focus == focusTerm {
		return a.termKey(ev)
	}
	// The server list has focus.
	if a.prefix.match(ev) {
		a.prefixPending = true
		a.updateFooter()
		return nil
	}
	if a.prefixPending {
		a.prefixPending = false
		if ev.Key() == tcell.KeyTab {
			a.setFocus(focusTerm)
		} else if ev.Rune() >= '1' && ev.Rune() <= '9' {
			a.activateTab(int(ev.Rune() - '1'))
		}
		a.updateFooter()
		return nil
	}
	if n, ok := altDigit(ev); ok {
		a.activateTab(n - 1)
		return nil
	}
	switch ev.Key() {
	case tcell.KeyCtrlC:
		a.requestQuit()
		return nil
	case tcell.KeyTab:
		a.setFocus(focusTerm)
		return nil
	case tcell.KeyEnter:
		if ev.Modifiers()&tcell.ModShift != 0 {
			a.connect(modeNewTab)
		} else {
			a.connect(modeReuse)
		}
		return nil
	case tcell.KeyEscape:
		if len(a.list.marks) > 0 {
			a.list.clearMarks()
		} else {
			a.dismissToast("")
		}
		return nil
	}
	switch ev.Rune() {
	case 'q':
		a.requestQuit()
		return nil
	case 'n':
		a.connect(modeNewTab)
		return nil
	case '|':
		a.splitSelected(layout.Horizontal)
		return nil
	case '-':
		a.splitSelected(layout.Vertical)
		return nil
	case 'F':
		a.toggleFullscreen()
		return nil
	case 'l':
		a.showLogin("Log in to Teleport.")
		return nil
	case 'L':
		a.showLogin("Log in again to renew your certificate.")
		return nil
	case 'o':
		a.logout()
		return nil
	case 'r':
		a.refresh()
		return nil
	case ':':
		a.openPalette("")
		return nil
	case '/':
		a.openPalette("filter " + a.list.filter.String())
		return nil
	case '?':
		a.openHelp()
		return nil
	case 'f':
		a.toggleFavorite(a.list.selectedHost())
		return nil
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		a.selectClusterKey(int(ev.Rune() - '0'))
		return nil
	case ' ':
		a.list.toggleMark()
		return nil
	case 'a':
		a.list.markAll()
		return nil
	case 'u':
		a.openSCP(transfer.Upload)
		return nil
	case 'd':
		a.openSCP(transfer.Download)
		return nil
	case 'p':
		a.openProxyPicker()
		return nil
	case 'N':
		a.sortList(sortName)
		return nil
	case 'A':
		a.sortList(sortAddr)
		return nil
	case 'C':
		a.sortList(sortCluster)
		return nil
	}
	return ev
}

// sortList sorts the server list by key (again: reverses it) and says so
// in the footer.
func (a *App) sortList(key sortKey) {
	a.flash(a.list.setSort(key), a.theme.Info)
}

func (a *App) renderHeader() {
	a.setHealthTargets(a.proxyEntries())
	a.info.Clusters = a.clusterEntries()
	a.info.Sessions = a.sessions.Len()
	a.header.render(a.info, time.Now())
}

// proxyEntries lists the configured proxies and the tsh profiles, once
// each, marking the current one.
func (a *App) proxyEntries() []proxyEntry {
	var out []proxyEntry
	seen := map[string]bool{}
	add := func(name, addr string) {
		key, err := tsh.WebAddr(addr)
		if err != nil || seen[key] {
			return
		}
		seen[key] = true
		cur, _ := tsh.WebAddr(a.info.Proxy)
		out = append(out, proxyEntry{Name: name, Addr: addr, Current: key == cur, Health: a.healthReports[key]})
	}
	for _, p := range a.o.Config.Proxies {
		add(p.Name, p.Addr)
	}
	for _, p := range a.profiles {
		add(p.Name, p.WebProxyAddr)
	}
	if a.info.Proxy != "" {
		add(a.info.Proxy, a.info.Proxy)
	}
	return out
}
