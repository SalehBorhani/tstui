package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
)

// Loginer is the part of auth.Service the login modal uses.
type Loginer interface {
	Login(ctx context.Context, req auth.LoginRequest) error
}

// LoginerFunc adapts a function to Loginer.
type LoginerFunc func(ctx context.Context, req auth.LoginRequest) error

// Login implements Loginer.
func (f LoginerFunc) Login(ctx context.Context, req auth.LoginRequest) error { return f(ctx, req) }

// MaxLoginFailures is the number of failed attempts after which the modal
// warns that the account may get locked.
const MaxLoginFailures = 3

const spinnerFrames = "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏"

// LoginResult says where a successful login went.
type LoginResult struct {
	Proxy string
	User  string
}

// LoginModal is the login dialog, drawn like a k9s dialog: a centred
// bordered box titled <Login> with Proxy, User, masked Password and MFA
// code fields, Login and Cancel buttons, and a status line for errors,
// the spinner and the "touch your security key" state. It runs the login
// in a goroutine and answers tsh's questions (auth.Prompter) through its
// fields, so the UI thread never blocks.
//
// Build one per login with NewLoginModal and show Primitive() on top of
// the main layout. onDone gets the proxy and user after a successful
// login, or auth.ErrCancelled when the user gave up (Esc or Cancel).
// Failed attempts stay in the dialog.
//
// Except for Ask and Notify, its methods must run on the UI goroutine.
type LoginModal struct {
	app    *tview.Application
	svc    Loginer
	theme  Theme
	base   auth.LoginRequest
	onDone func(LoginResult, error)
	// spawn starts the dialog's goroutines: plain go, or App.goSafe.
	spawn func(func())

	root     *modalLayout
	message  *tview.TextView
	form     *tview.Form
	proxy    *tview.InputField
	user     *tview.InputField
	password *tview.InputField
	code     *tview.InputField
	status   *tview.TextView

	// posts carries updates from login goroutines to the UI goroutine in
	// order (see post).
	posts chan func()
	quit  chan struct{}

	cancelLogin context.CancelFunc // set while a login runs
	running     LoginResult        // proxy and user of the running login
	cancelled   bool               // the user pressed Esc during a login
	question    chan askReply      // set while tsh waits for an answer
	questionFor auth.PromptKind
	failures    int
	done        bool
	pendingOTP  *auth.Secret // from the command line, for the first attempt

	busyText  string
	stopSpin  chan struct{}
	spinFrame int
}

type askReply struct {
	secret *auth.Secret
	err    error
}

// Field indexes in the form.
const (
	fieldProxy = iota
	fieldUser
	fieldPassword
	fieldCode
)

const codePlaceholder = "if asked"

// NewLoginModal builds the dialog. base supplies the initial Proxy, and
// AuthConnector, MFAMode and Insecure for every attempt; its secrets are
// ignored.
func NewLoginModal(app *tview.Application, svc Loginer, theme Theme, base auth.LoginRequest, onDone func(LoginResult, error)) *LoginModal {
	return newLoginModal(app, svc, theme, base, onDone, func(f func()) { go f() })
}

// newLoginModal is NewLoginModal with the function that starts its
// goroutines, so the App can catch their panics.
func newLoginModal(app *tview.Application, svc Loginer, theme Theme, base auth.LoginRequest,
	onDone func(LoginResult, error), spawn func(func())) *LoginModal {
	base.Password, base.OTP, base.Prompter = nil, nil, nil
	m := &LoginModal{
		app: app, svc: svc, theme: theme, base: base, onDone: onDone, spawn: spawn,
		posts: make(chan func(), 64),
		quit:  make(chan struct{}),
	}

	placeholder := tcell.StyleDefault.Background(theme.Bg).Foreground(theme.DialogPlaceholder)
	field := func(label, hint string) *tview.InputField {
		return tview.NewInputField().SetLabel(label).SetFieldWidth(0).
			SetPlaceholder(hint).SetPlaceholderStyle(placeholder)
	}
	m.proxy = field("Proxy:", "host[:port], e.g. teleport.example.com:443").SetText(base.Proxy)
	m.user = field("User:", "Teleport user")
	m.password = field("Password:", "").SetMaskCharacter('*')
	m.code = field("MFA code:", codePlaceholder)

	m.form = tview.NewForm().
		AddFormItem(m.proxy).
		AddFormItem(m.user).
		AddFormItem(m.password).
		AddFormItem(m.code).
		AddButton("Login", m.submit).
		AddButton("Cancel", m.cancel)
	m.form.SetBorderPadding(0, 0, 0, 0) // tview's default is 1, which hides the buttons
	m.form.SetItemPadding(0).
		SetButtonsAlign(tview.AlignCenter).
		SetLabelColor(theme.DialogLabel).
		SetFieldTextColor(theme.DialogField).
		SetFieldBackgroundColor(theme.Bg).
		SetButtonStyle(tcell.StyleDefault.Foreground(theme.ButtonFg).Background(theme.ButtonBg)).
		SetButtonActivatedStyle(tcell.StyleDefault.Foreground(theme.ButtonFocusFg).Background(theme.ButtonFocusBg)).
		SetBackgroundColor(theme.Bg)
	m.form.SetCancelFunc(m.cancel)
	m.form.SetInputCapture(func(ev *tcell.EventKey) *tcell.EventKey {
		// Enter in the password or code field logs in; in the proxy and
		// user fields it moves on, as Tab does.
		if ev.Key() == tcell.KeyEnter {
			if item, button := m.form.GetFocusedItemIndex(); button < 0 && (item == fieldPassword || item == fieldCode) {
				m.submit()
				return nil
			}
		}
		return ev
	})

	m.message = tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter).SetTextColor(theme.DialogFg)
	m.status = tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true).SetTextColor(theme.DialogFg)

	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(m.message, 1, 0, false).
		AddItem(nil, 1, 0, false).
		AddItem(m.form, 6, 0, true). // 4 fields, a gap, the buttons
		AddItem(nil, 1, 0, false).
		AddItem(m.status, 0, 1, false)
	body.SetBorder(true).
		SetBorderColor(theme.Border).
		SetTitle(" <Login> ").
		SetTitleColor(theme.Title).
		SetBorderPadding(1, 1, 2, 2).
		SetBackgroundColor(theme.Bg)
	// Rows: border 2, padding 2, message 1, gap 1, form 6, gap 1, status 3.
	m.root = newModalLayout(body, theme.Bg, 60, 16)

	m.SetMessage("Log in to Teleport")
	m.setStatus(fmt.Sprintf("[%s]Tab next field · Enter log in · Esc cancel", tag(theme.DialogPlaceholder)))
	m.spawn(m.pump)
	return m
}

// Primitive is the dialog; put it on top of the main layout
// (tview.Pages, not resized) and focus it.
func (m *LoginModal) Primitive() tview.Primitive { return m.root }

// SetMessage sets the line under the title, e.g. why a login is needed.
func (m *LoginModal) SetMessage(text string) { m.message.SetText(text) }

// SetUser pre-fills the user field.
func (m *LoginModal) SetUser(user string) {
	m.user.SetText(user)
}

// FocusFirstEmpty moves the cursor to the first field that still needs a
// value: the proxy, the user, or else the password.
func (m *LoginModal) FocusFirstEmpty() {
	switch {
	case strings.TrimSpace(m.proxy.GetText()) == "":
		m.focusField(fieldProxy)
	case strings.TrimSpace(m.user.GetText()) == "":
		m.focusField(fieldUser)
	default:
		m.focusField(fieldPassword)
	}
}

// SetOTP keeps an OTP code given on the command line for the first
// attempt that has no code typed in. The modal owns otp from now on.
func (m *LoginModal) SetOTP(otp *auth.Secret) {
	m.pendingOTP.Wipe()
	m.pendingOTP = otp
}

// Start logs in right away with the given values, as if typed into the
// form; used when the command line already had the credentials. The
// modal owns password and otp from now on.
func (m *LoginModal) Start(user string, password, otp *auth.Secret) {
	m.user.SetText(user)
	m.startLogin(strings.TrimSpace(m.proxy.GetText()), user, password, otp)
}

// Close stops the modal's helpers; it is called before onDone.
func (m *LoginModal) Close() {
	select {
	case <-m.quit:
	default:
		close(m.quit)
	}
	m.stopSpinner()
	m.pendingOTP.Wipe()
	if m.cancelLogin != nil {
		m.cancelLogin()
	}
}

// post runs f on the UI goroutine. Calls from one goroutine run in order,
// and post never waits for the UI: a login goroutine must not hang
// because the application has stopped.
func (m *LoginModal) post(f func()) {
	select {
	case m.posts <- f:
	case <-m.quit:
	}
}

func (m *LoginModal) pump() {
	for {
		select {
		case f := <-m.posts:
			m.app.QueueUpdateDraw(f)
		case <-m.quit:
			return
		}
	}
}

func (m *LoginModal) submit() {
	if m.done {
		return
	}
	if m.question != nil {
		m.answer()
		return
	}
	if m.cancelLogin != nil {
		return // a login is running and has asked nothing
	}
	proxy := strings.TrimSpace(m.proxy.GetText())
	if proxy == "" {
		m.setStatus(m.colored(m.theme.Err, "Enter the proxy address, e.g. teleport.example.com:443."))
		m.focusField(fieldProxy)
		return
	}
	m.startLogin(proxy, strings.TrimSpace(m.user.GetText()), takeSecret(m.password), takeSecret(m.code))
}

func (m *LoginModal) startLogin(proxy, user string, password, otp *auth.Secret) {
	if m.cancelLogin != nil || proxy == "" {
		password.Wipe()
		otp.Wipe()
		if proxy == "" {
			m.FocusFirstEmpty()
		}
		return
	}
	if otp.Empty() && !m.pendingOTP.Empty() {
		otp, m.pendingOTP = m.pendingOTP, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelLogin = cancel
	m.cancelled = false
	m.running = LoginResult{Proxy: proxy, User: user}
	m.startSpinner("Logging in to " + tview.Escape(proxy) + "…")

	req := m.base
	req.Proxy, req.User, req.Password, req.OTP, req.Prompter = proxy, user, password, otp, m
	m.spawn(func() {
		err := m.svc.Login(ctx, req)
		cancel()
		m.post(func() { m.finished(err) })
	})
}

func (m *LoginModal) finished(err error) {
	m.cancelLogin = nil
	m.question = nil
	m.resetCodeField()
	m.stopSpinner()
	switch {
	case err == nil:
		m.failures = 0
		m.finish(m.running, nil)
		return
	case m.cancelled || errors.Is(err, auth.ErrCancelled):
		m.finish(LoginResult{}, auth.ErrCancelled)
		return
	}

	focus := fieldPassword
	switch {
	case errors.Is(err, auth.ErrWrongOTP):
		m.failures++
		focus = fieldCode
	case errors.Is(err, auth.ErrWrongPassword), errors.Is(err, auth.ErrBadCredentials), errors.Is(err, auth.ErrLocked):
		m.failures++
	case errors.Is(err, auth.ErrNetwork), errors.Is(err, auth.ErrTLS), errors.Is(err, auth.ErrConnectorNotFound):
		focus = fieldProxy
	}
	msg := m.colored(m.theme.Err, loginErrorText(err))
	if m.failures >= MaxLoginFailures {
		msg += "\n" + m.colored(m.theme.Warn, fmt.Sprintf("%d failed attempts.", m.failures)) +
			" Check the user name, password and code before trying again: more failures may lock the account."
	}
	m.setStatus(msg)
	m.focusField(focus)
}

func (m *LoginModal) finish(res LoginResult, err error) {
	if m.done {
		return
	}
	m.done = true
	m.Close()
	if m.onDone != nil {
		m.onDone(res, err)
	}
}

// cancel handles Esc and the Cancel button.
func (m *LoginModal) cancel() {
	if m.done {
		return
	}
	if m.cancelLogin == nil {
		m.finish(LoginResult{}, auth.ErrCancelled)
		return
	}
	m.cancelled = true
	m.setStatus("Cancelling…")
	if m.question != nil {
		m.question <- askReply{err: auth.ErrCancelled}
		m.question = nil
	}
	m.cancelLogin()
	// finished() reports the result when the login goroutine returns.
}

// Ask implements auth.Prompter. It runs on the login goroutine.
func (m *LoginModal) Ask(ctx context.Context, p auth.Prompt) (*auth.Secret, error) {
	reply := make(chan askReply, 1)
	m.post(func() { m.showQuestion(p, reply) })
	select {
	case r := <-reply:
		return r.secret, r.err
	case <-ctx.Done():
		m.post(func() { m.withdrawQuestion(reply) })
		return nil, ctx.Err()
	case <-m.quit:
		return nil, auth.ErrCancelled
	}
}

// Notify implements auth.Prompter. It runs on the login goroutine.
func (m *LoginModal) Notify(e auth.Event) {
	m.post(func() {
		if m.done {
			return
		}
		switch e.Kind {
		case auth.EventTapKey:
			m.startSpinner(m.colored(m.theme.Warn, "Touch your security key now"))
		case auth.EventKeyTapped:
			m.startSpinner("Security key touched, logging in…")
		case auth.EventSSOURL:
			m.startSpinner("Finish the login in your browser:\n" + tview.Escape(e.URL))
		case auth.EventAnswered:
			if m.question == nil {
				m.startSpinner("Logging in…")
			}
		}
	})
}

func (m *LoginModal) showQuestion(p auth.Prompt, reply chan askReply) {
	if m.done || m.cancelled {
		reply <- askReply{err: auth.ErrCancelled}
		return
	}
	m.question, m.questionFor = reply, p.Kind
	m.stopSpinner()
	m.resetCodeField()
	switch p.Kind {
	case auth.PromptPassword:
		if p.Retry {
			m.setStatus(m.colored(m.theme.Err, "Wrong password.") + " Enter it again.")
		} else {
			m.setStatus(m.colored(m.theme.Info, "Enter your password."))
		}
		m.focusField(fieldPassword)
	case auth.PromptOTP:
		if p.Retry {
			m.setStatus(m.colored(m.theme.Err, "Wrong code.") + " Enter a new code from your authenticator app.")
		} else {
			m.setStatus(m.colored(m.theme.Info, "Enter the code from your authenticator app."))
		}
		m.code.SetPlaceholder("6-digit code")
		m.focusField(fieldCode)
	case auth.PromptOTPOrTap:
		m.setStatus(m.colored(m.theme.Warn, "Touch your security key") + ", or enter an OTP code.")
		m.focusField(fieldCode)
	case auth.PromptPIN:
		m.code.SetLabel("Key PIN:").SetMaskCharacter('*').SetPlaceholder("")
		m.setStatus(m.colored(m.theme.Info, "Enter your security key PIN."))
		m.focusField(fieldCode)
	default:
		m.code.SetLabel("Answer:").SetPlaceholder("")
		text := tview.Escape(p.Text)
		if len(p.Options) > 0 {
			text += "\n" + tview.Escape(strings.Join(p.Options, "   "))
		}
		m.setStatus(text)
		m.focusField(fieldCode)
	}
}

func (m *LoginModal) withdrawQuestion(reply chan askReply) {
	// An answer typed after Ask gave up is never read: wipe it.
	select {
	case r := <-reply:
		r.secret.Wipe()
	default:
	}
	if m.question != reply {
		return
	}
	m.question = nil
	m.resetCodeField()
	if m.cancelLogin != nil {
		m.startSpinner("Logging in…")
	}
}

// answer sends the field that the open question is about.
func (m *LoginModal) answer() {
	field := m.code
	if m.questionFor == auth.PromptPassword {
		field = m.password
	}
	m.question <- askReply{secret: takeSecret(field)}
	m.question = nil
	m.resetCodeField()
	m.startSpinner("Logging in…")
}

func (m *LoginModal) resetCodeField() {
	m.code.SetLabel("MFA code:").SetMaskCharacter(0).SetPlaceholder(codePlaceholder)
}

func (m *LoginModal) focusField(i int) {
	m.form.SetFocus(i)
	m.app.SetFocus(m.form)
}

func (m *LoginModal) setStatus(text string) {
	m.status.SetText(text)
}

func (m *LoginModal) colored(c tcell.Color, text string) string {
	return fmt.Sprintf("[%s]%s[-]", tag(c), tview.Escape(text))
}

func (m *LoginModal) startSpinner(text string) {
	m.busyText = text
	m.renderSpinner()
	if m.stopSpin != nil {
		return
	}
	stop := make(chan struct{})
	m.stopSpin = stop
	m.spawn(func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				m.post(func() {
					if m.stopSpin == stop {
						m.spinFrame++
						m.renderSpinner()
					}
				})
			}
		}
	})
}

func (m *LoginModal) renderSpinner() {
	frames := []rune(spinnerFrames)
	m.setStatus(fmt.Sprintf("[%s]%c[-] %s", tag(m.theme.Info), frames[m.spinFrame%len(frames)], m.busyText))
}

func (m *LoginModal) stopSpinner() {
	if m.stopSpin != nil {
		close(m.stopSpin)
		m.stopSpin = nil
	}
}

// takeSecret moves an input field's text into a Secret and clears the
// field. tview keeps text as a string, which cannot be wiped; clearing
// the field at least drops the modal's reference to it.
func takeSecret(f *tview.InputField) *auth.Secret {
	text := f.GetText()
	f.SetText("")
	if text == "" {
		return nil
	}
	return auth.NewSecret([]byte(text))
}

// loginErrorText is the inline error for a failed login.
func loginErrorText(err error) string {
	switch {
	case errors.Is(err, auth.ErrLocked):
		return "Account locked: " + err.Error() + ". Wait, or ask a Teleport admin to unlock it."
	case errors.Is(err, auth.ErrTLS):
		return err.Error() + " (--insecure skips this check; use it only for test clusters)"
	case errors.Is(err, auth.ErrNetwork):
		return err.Error() + ". Check the proxy address and your network."
	case errors.Is(err, auth.ErrTimeout):
		return "Login timed out: " + err.Error()
	}
	return err.Error()
}

// modalLayout centres a dialog the way k9s does: a third of the screen
// wide (at least minWidth), a fixed height, drawn over whatever is below.
type modalLayout struct {
	*tview.Box
	inner            tview.Primitive
	bg               tcell.Color
	minWidth, height int
}

func newModalLayout(inner tview.Primitive, bg tcell.Color, minWidth, height int) *modalLayout {
	return &modalLayout{Box: tview.NewBox(), inner: inner, bg: bg, minWidth: minWidth, height: height}
}

// Draw implements tview.Primitive.
func (l *modalLayout) Draw(screen tcell.Screen) {
	sw, sh := screen.Size()
	w := max(sw/3+4, l.minWidth)
	w = min(w, sw)
	h := min(l.height, sh)
	x, y := (sw-w)/2, (sh-h)/2
	l.SetRect(x, y, w, h)
	// tview leaves cells with the default (terminal) background unpainted,
	// so what is below would show through: blank the area first.
	style := tcell.StyleDefault.Background(l.bg)
	for row := y; row < y+h; row++ {
		for col := x; col < x+w; col++ {
			screen.SetContent(col, row, ' ', nil, style)
		}
	}
	l.inner.SetRect(x, y, w, h)
	l.inner.Draw(screen)
}

// Focus implements tview.Primitive.
func (l *modalLayout) Focus(delegate func(p tview.Primitive)) { delegate(l.inner) }

// HasFocus implements tview.Primitive.
func (l *modalLayout) HasFocus() bool { return l.inner.HasFocus() }

// InputHandler implements tview.Primitive.
func (l *modalLayout) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return l.inner.InputHandler()
}

// MouseHandler implements tview.Primitive.
func (l *modalLayout) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return l.inner.MouseHandler()
}

// PasteHandler implements tview.Primitive.
func (l *modalLayout) PasteHandler() func(string, func(tview.Primitive)) {
	return l.inner.PasteHandler()
}
