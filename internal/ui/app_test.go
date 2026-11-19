package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/auth"
	"github.com/root-ali/tstui/internal/tsh"
)

const (
	profileProxy = "ast-teleport.example:443"
	profileUser  = "a.soltani"
)

// appHarness runs the whole App on a simulated screen against tsh.Fake.
type appHarness struct {
	t      *testing.T
	fake   *tsh.Fake
	app    *App
	screen tcell.SimulationScreen
	runErr chan error

	mu     sync.Mutex
	logins []auth.LoginRequest // secrets copied out as strings
	pwds   []string
}

func validStatus(user string) tsh.Status {
	return tsh.Status{LoggedIn: true, User: user, Cluster: "ast", Roles: []string{"access"}, ValidUntil: time.Now().Add(8 * time.Hour)}
}

func expiredStatus(user string) tsh.Status {
	return tsh.Status{LoggedIn: true, User: user, Cluster: "ast", ValidUntil: time.Now().Add(-time.Hour)}
}

func newAppHarness(t *testing.T, fake *tsh.Fake, o Options) *appHarness {
	t.Helper()
	h := &appHarness{t: t, fake: fake, runErr: make(chan error, 1)}
	if fake.VersionInfo.Version == "" {
		fake.VersionInfo = tsh.VersionInfo{Version: "18.11.3"}
	}
	h.screen = tcell.NewSimulationScreen("UTF-8")
	if err := h.screen.Init(); err != nil {
		t.Fatal(err)
	}
	h.screen.SetSize(140, 40)
	o.Client = fake
	o.Auth = h
	o.Version = "test"
	h.app = NewApp(h.screen, o)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { h.runErr <- h.app.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-h.runErr:
		case <-time.After(5 * time.Second):
			t.Error("app did not stop")
		}
	})
	return h
}

// Login implements Authenticator: it accepts any password and makes
// the proxy's status valid.
func (h *appHarness) Login(_ context.Context, req auth.LoginRequest) error {
	defer req.Wipe()
	h.mu.Lock()
	h.logins = append(h.logins, auth.LoginRequest{Proxy: req.Proxy, User: req.User})
	h.pwds = append(h.pwds, string(req.Password.Bytes()))
	h.mu.Unlock()
	st := validStatus(req.User)
	if prev, err := h.fake.Status(context.Background(), req.Proxy); err == nil {
		st.Logins = prev.Logins // a re-login keeps the user's logins
	}
	h.fake.SetStatus(req.Proxy, st)
	return nil
}

// Logout implements Authenticator.
func (h *appHarness) Logout(ctx context.Context, proxy, user string) error {
	return h.fake.Logout(ctx, proxy, user)
}

func (h *appHarness) ui(f func()) {
	done := make(chan struct{})
	go func() {
		h.app.tv.QueueUpdate(f)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		h.t.Fatal("UI goroutine not responding")
	}
}

func (h *appHarness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		h.ui(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s; screen:\n%s", what, h.text())
}

func (h *appHarness) text() string {
	var out string
	h.ui(func() { out = h.textUnlocked() })
	return out
}

// textUnlocked reads the screen from the UI goroutine (inside ui()).
func (h *appHarness) textUnlocked() string {
	cells, w, _ := h.screen.GetContents()
	var b strings.Builder
	for i, c := range cells {
		if i > 0 && i%w == 0 {
			b.WriteByte('\n')
		}
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		}
	}
	return b.String()
}

func (h *appHarness) typeText(s string) {
	for _, r := range s {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func (h *appHarness) key(k tcell.Key) { h.screen.InjectKey(k, 0, tcell.ModNone) }

func (h *appHarness) modalOpen() bool { return h.app.modal != nil }

var servers = map[string][]tsh.Server{profileProxy: {
	{Hostname: "web-01", Addr: "10.0.0.1:3022", Labels: map[string]string{"env": "prod"}},
	{Hostname: "db-03", Labels: map[string]string{"env": "prod"}},
}}

func profiles() []tsh.Profile {
	return []tsh.Profile{{Name: "ast-teleport.example", WebProxyAddr: profileProxy, User: profileUser, Current: true}}
}

// A valid profile in ~/.tsh: straight to the server list, no dialog.
func TestAppValidProfile(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	h := newAppHarness(t, fake, Options{})
	h.eventually("server list", func() bool { return h.app.serversLoaded })
	h.ui(func() {
		if h.modalOpen() {
			t.Error("login dialog opened for a valid profile")
		}
	})
	h.eventually("servers and header on screen", func() bool {
		s := h.textUnlocked()
		return strings.Contains(s, "web-01") && strings.Contains(s, profileUser) && strings.Contains(s, "logged in")
	})
}

// An expired profile: the dialog opens over the UI with the proxy and
// user from the profile, the cursor in the password field.
func TestAppExpiredProfile(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	fake.SetStatus(profileProxy, expiredStatus(profileUser))
	h := newAppHarness(t, fake, Options{})
	h.eventually("login dialog", h.modalOpen)
	h.eventually("dialog prefilled", func() bool {
		m := h.app.modal
		return m != nil && m.proxy.GetText() == profileProxy && m.user.GetText() == profileUser &&
			h.app.tv.GetFocus() == m.password
	})
	h.eventually("dialog drawn over the main UI", func() bool {
		s := h.textUnlocked()
		return strings.Contains(s, "<Login>") && strings.Contains(s, "expired") && strings.Contains(s, "Clusters")
	})

	h.typeText("pw")
	h.key(tcell.KeyEnter)
	h.eventually("dialog closed and servers loaded", func() bool { return !h.modalOpen() && h.app.serversLoaded })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.logins) != 1 || h.logins[0].Proxy != profileProxy || h.logins[0].User != profileUser || h.pwds[0] != "pw" {
		t.Fatalf("logins = %+v %q", h.logins, h.pwds)
	}
}

// No profile and no config: the dialog asks for the proxy too.
func TestAppNoProxy(t *testing.T) {
	fake := &tsh.Fake{Servers: servers}
	h := newAppHarness(t, fake, Options{})
	h.eventually("login dialog in the proxy field", func() bool {
		return h.modalOpen() && h.app.tv.GetFocus() == h.app.modal.proxy
	})
	h.typeText(profileProxy)
	h.key(tcell.KeyTab)
	h.typeText("bob")
	h.key(tcell.KeyTab)
	h.typeText("pw")
	h.key(tcell.KeyEnter)
	h.eventually("logged in to the typed proxy", func() bool {
		return !h.modalOpen() && h.app.info.Proxy == profileProxy && h.app.serversLoaded
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.logins) != 1 || h.logins[0].User != "bob" {
		t.Fatalf("logins = %+v", h.logins)
	}
}

// The command line gave a password: it is used at once, without typing.
func TestAppPasswordFromCommandLine(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	fake.SetStatus(profileProxy, expiredStatus(profileUser))
	pw := auth.NewSecret([]byte("cli-pw"))
	h := newAppHarness(t, fake, Options{Creds: auth.Credentials{Password: pw}})
	h.eventually("logged in", func() bool { return h.app.serversLoaded && !h.modalOpen() })
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pwds) != 1 || h.pwds[0] != "cli-pw" {
		t.Fatalf("passwords = %q", h.pwds)
	}
	if !pw.Empty() {
		t.Error("command-line password not wiped")
	}
}

// The certificate expires while tstui runs: the dialog opens by itself.
func TestAppExpiryOpensDialog(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	h := newAppHarness(t, fake, Options{WatchInterval: 50 * time.Millisecond})
	h.eventually("server list", func() bool { return h.app.serversLoaded })
	fake.SetStatus(profileProxy, expiredStatus(profileUser))
	h.eventually("re-login dialog", h.modalOpen)
}

// Esc closes the dialog and leaves the UI running, not logged in.
func TestAppCancelLogin(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles()}
	fake.SetStatus(profileProxy, expiredStatus(profileUser))
	h := newAppHarness(t, fake, Options{})
	h.eventually("login dialog", h.modalOpen)
	h.key(tcell.KeyEscape)
	h.eventually("dialog closed", func() bool { return !h.modalOpen() })
	h.typeText("l")
	h.eventually("dialog reopened with l", h.modalOpen)
}

func TestAppExitAfterLogin(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles()}
	fake.SetStatus(profileProxy, expiredStatus(profileUser))
	h := newAppHarness(t, fake, Options{ExitAfterLogin: true})
	h.eventually("login dialog", h.modalOpen)
	h.typeText("pw")
	h.key(tcell.KeyEnter)
	select {
	case err := <-h.runErr:
		h.runErr <- err // for Cleanup
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app did not exit after login")
	}
	proxy, st, err := h.app.LoginResult()
	if err != nil || proxy != profileProxy || st.User != profileUser {
		t.Fatalf("LoginResult = %q %+v %v", proxy, st, err)
	}
}

func TestResolveProxy(t *testing.T) {
	ps := profiles()
	tests := []struct {
		proxy, user         string
		profiles            []tsh.Profile
		wantProxy, wantUser string
	}{
		{"", "", ps, profileProxy, profileUser},
		{"", "me", ps, profileProxy, "me"},
		{"ast-teleport.example", "", ps, "ast-teleport.example", profileUser}, // port 443 implied
		{"other.example:443", "", ps, "other.example:443", ""},
		{"", "", nil, "", ""},
	}
	for _, tt := range tests {
		p, u := resolveProxy(tt.proxy, tt.user, tt.profiles)
		if p != tt.wantProxy || u != tt.wantUser {
			t.Errorf("resolveProxy(%q, %q) = %q, %q; want %q, %q", tt.proxy, tt.user, p, u, tt.wantProxy, tt.wantUser)
		}
	}
}

func TestCountdown(t *testing.T) {
	for d, want := range map[time.Duration]string{
		7*time.Hour + 42*time.Minute:   "07h42m",
		4*time.Minute + 12*time.Second: "04m12s",
	} {
		if got := countdown(d); got != want {
			t.Errorf("countdown(%s) = %q, want %q", d, got, want)
		}
	}
}

// A tsh that tstui does not support gets a warning notice.
func TestAppUnsupportedTsh(t *testing.T) {
	fake := &tsh.Fake{ProfileList: profiles(), Servers: servers, VersionInfo: tsh.VersionInfo{Version: "15.1.0"}}
	fake.SetStatus(profileProxy, validStatus(profileUser))
	h := newAppHarness(t, fake, Options{})
	h.eventually("version warning", func() bool { return strings.Contains(h.textUnlocked(), "tsh v15.1.0 is not supported") })
}
