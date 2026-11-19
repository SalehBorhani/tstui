package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
)

// harness runs a LoginModal in a tview app on a simulated screen.
type harness struct {
	t      *testing.T
	app    *tview.Application
	screen tcell.SimulationScreen
	modal  *LoginModal

	mu       sync.Mutex
	done     []error
	results  []LoginResult
	attempts []attempt
}

type attempt struct {
	proxy, user, password, otp string
}

func newHarness(t *testing.T, login func(ctx context.Context, req auth.LoginRequest) error) *harness {
	return newHarnessProxy(t, "tp.afr.example:443", login)
}

func newHarnessProxy(t *testing.T, proxy string, login func(ctx context.Context, req auth.LoginRequest) error) *harness {
	t.Helper()
	h := &harness{t: t}
	h.screen = tcell.NewSimulationScreen("UTF-8")
	if err := h.screen.Init(); err != nil {
		t.Fatal(err)
	}
	h.screen.SetSize(100, 30)
	h.app = tview.NewApplication().SetScreen(h.screen)

	svc := LoginerFunc(func(ctx context.Context, req auth.LoginRequest) error {
		h.mu.Lock()
		h.attempts = append(h.attempts, attempt{req.Proxy, req.User, string(req.Password.Bytes()), string(req.OTP.Bytes())})
		h.mu.Unlock()
		defer req.Wipe()
		return login(ctx, req)
	})
	h.modal = NewLoginModal(h.app, svc, DefaultTheme(), auth.LoginRequest{Proxy: proxy}, func(res LoginResult, err error) {
		h.mu.Lock()
		h.done = append(h.done, err)
		h.results = append(h.results, res)
		h.mu.Unlock()
	})
	h.app.SetRoot(h.modal.Primitive(), true)
	h.modal.FocusFirstEmpty()

	runErr := make(chan error, 1)
	go func() { runErr <- h.app.Run() }()
	t.Cleanup(func() {
		h.modal.Close()
		h.app.Stop()
		if err := <-runErr; err != nil {
			t.Error(err)
		}
	})
	return h
}

func (h *harness) typeText(s string) {
	for _, r := range s {
		h.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func (h *harness) key(k tcell.Key) { h.screen.InjectKey(k, 0, tcell.ModNone) }

// eventually polls cond on the UI goroutine until it holds.
func (h *harness) eventually(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := false
		h.app.QueueUpdate(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	var status string
	h.app.QueueUpdate(func() { status = h.modal.status.GetText(true) })
	h.t.Fatalf("timed out waiting for %s; status: %q", what, status)
}

func (h *harness) statusContains(s string) func() bool {
	return func() bool { return strings.Contains(h.modal.status.GetText(true), s) }
}

func (h *harness) doneWith() []error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]error(nil), h.done...)
}

func (h *harness) attemptList() []attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]attempt(nil), h.attempts...)
}

func TestLoginModalSubmit(t *testing.T) {
	h := newHarness(t, func(context.Context, auth.LoginRequest) error { return nil })
	h.typeText("ali")
	h.key(tcell.KeyTab)
	h.typeText("pw1")
	h.key(tcell.KeyEnter)
	h.eventually("onDone", func() bool { return len(h.doneWith()) == 1 })
	if err := h.doneWith()[0]; err != nil {
		t.Fatalf("onDone(%v)", err)
	}
	if got := h.attemptList(); len(got) != 1 || got[0] != (attempt{"tp.afr.example:443", "ali", "pw1", ""}) {
		t.Fatalf("attempts = %+v", got)
	}
	h.mu.Lock()
	res := h.results[0]
	h.mu.Unlock()
	if res != (LoginResult{Proxy: "tp.afr.example:443", User: "ali"}) {
		t.Fatalf("result = %+v", res)
	}
	h.eventually("password field cleared", func() bool { return h.modal.password.GetText() == "" })
}

func TestLoginModalAnswersOTPQuestion(t *testing.T) {
	answered := make(chan string, 1)
	h := newHarness(t, func(ctx context.Context, req auth.LoginRequest) error {
		s, err := req.Prompter.Ask(ctx, auth.Prompt{Kind: auth.PromptOTP, Text: "Enter an OTP code from a device:"})
		if err != nil {
			return err
		}
		answered <- string(s.Bytes())
		s.Wipe()
		return nil
	})
	h.typeText("ali")
	h.key(tcell.KeyTab)
	h.typeText("pw")
	h.key(tcell.KeyEnter)
	h.eventually("OTP question", h.statusContains("authenticator"))
	h.eventually("focus on the code field", func() bool { return h.app.GetFocus() == h.modal.code })
	h.typeText("123456")
	h.key(tcell.KeyEnter)
	select {
	case got := <-answered:
		if got != "123456" {
			t.Fatalf("answer = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("question not answered")
	}
	h.eventually("onDone(nil)", func() bool { d := h.doneWith(); return len(d) == 1 && d[0] == nil })
}

func TestLoginModalTapKey(t *testing.T) {
	release := make(chan struct{})
	h := newHarness(t, func(ctx context.Context, req auth.LoginRequest) error {
		req.Prompter.Notify(auth.Event{Kind: auth.EventTapKey, Text: "Tap any security key"})
		select {
		case <-release:
		case <-ctx.Done():
			return auth.ErrCancelled
		}
		req.Prompter.Notify(auth.Event{Kind: auth.EventKeyTapped})
		return nil
	})
	h.modal.Start("ali", auth.NewSecret([]byte("pw")), nil)
	h.eventually("touch-your-key state", h.statusContains("Touch your security key"))
	close(release)
	h.eventually("onDone(nil)", func() bool { d := h.doneWith(); return len(d) == 1 && d[0] == nil })
}

func TestLoginModalFailures(t *testing.T) {
	h := newHarness(t, func(context.Context, auth.LoginRequest) error {
		return fmt.Errorf("%w: invalid username or password", auth.ErrWrongPassword)
	})
	h.typeText("ali")
	h.key(tcell.KeyTab)
	for i := 1; i <= MaxLoginFailures; i++ {
		h.typeText("bad")
		h.key(tcell.KeyEnter)
		h.eventually(fmt.Sprintf("attempt %d", i), func() bool {
			return len(h.attemptList()) == i && h.modal.cancelLogin == nil && h.modal.failures == i
		})
	}
	h.eventually("inline error", h.statusContains("invalid username or password"))
	h.eventually("failure warning", h.statusContains("3 failed attempts"))
	if d := h.doneWith(); len(d) != 0 {
		t.Fatalf("modal closed after failures: %v", d)
	}
	if got := h.attemptList(); got[2].password != "bad" || got[2].user != "ali" {
		t.Fatalf("third attempt %+v", got[2])
	}
}

func TestLoginModalEscWhenIdle(t *testing.T) {
	h := newHarness(t, func(context.Context, auth.LoginRequest) error { return nil })
	h.key(tcell.KeyEscape)
	h.eventually("onDone(ErrCancelled)", func() bool {
		d := h.doneWith()
		return len(d) == 1 && errors.Is(d[0], auth.ErrCancelled)
	})
	if n := len(h.attemptList()); n != 0 {
		t.Fatalf("%d login attempts", n)
	}
}

func TestLoginModalEscDuringQuestion(t *testing.T) {
	askErr := make(chan error, 1)
	h := newHarness(t, func(ctx context.Context, req auth.LoginRequest) error {
		_, err := req.Prompter.Ask(ctx, auth.Prompt{Kind: auth.PromptOTP})
		askErr <- err
		<-ctx.Done() // Esc also cancels the login itself
		return fmt.Errorf("%w: %w", auth.ErrCancelled, ctx.Err())
	})
	h.modal.Start("ali", auth.NewSecret([]byte("pw")), nil)
	h.eventually("OTP question", h.statusContains("authenticator"))
	h.key(tcell.KeyEscape)
	select {
	case err := <-askErr:
		if !errors.Is(err, auth.ErrCancelled) {
			t.Fatalf("Ask returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask not cancelled")
	}
	h.eventually("onDone(ErrCancelled)", func() bool {
		d := h.doneWith()
		return len(d) == 1 && errors.Is(d[0], auth.ErrCancelled)
	})
}

// TestLoginModalAsksForProxy: with no proxy known, the dialog starts in
// the proxy field and refuses to log in without one.
func TestLoginModalAsksForProxy(t *testing.T) {
	h := newHarnessProxy(t, "", func(context.Context, auth.LoginRequest) error { return nil })
	h.eventually("focus on the proxy field", func() bool { return h.app.GetFocus() == h.modal.proxy })
	h.key(tcell.KeyTab)
	h.typeText("ali")
	h.key(tcell.KeyTab)
	h.typeText("pw")
	h.key(tcell.KeyEnter)
	h.eventually("proxy required", h.statusContains("Enter the proxy address"))
	h.eventually("back in the proxy field", func() bool { return h.app.GetFocus() == h.modal.proxy })
	if n := len(h.attemptList()); n != 0 {
		t.Fatalf("logged in without a proxy: %d attempts", n)
	}

	// The typed password is kept after the refusal.
	h.typeText("tp.new.example")
	h.key(tcell.KeyTab)
	h.key(tcell.KeyTab)
	h.key(tcell.KeyEnter)
	h.eventually("onDone", func() bool { return len(h.doneWith()) == 1 })
	if got := h.attemptList(); len(got) != 1 || got[0] != (attempt{"tp.new.example", "ali", "pw", ""}) {
		t.Fatalf("attempts = %+v", got)
	}
}
