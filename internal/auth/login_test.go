package auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// Distinctive values so that a leak cannot hide among pids or durations.
const (
	goodPassword = "Sup3r-Secret-PW"
	badPassword  = "WrongPW-xyz-77"
	goodOTP      = "918273"
	badOTP       = "864209"
	goodPIN      = "7z7z"
	envPassword  = "env-only-Secret-99"
)

var allSecrets = []string{goodPassword, badPassword, goodOTP, badOTP, goodPIN, envPassword}

// block is an answer that makes the test prompter wait until its context
// is cancelled.
const block = "\x00block"

// testPrompter answers from a map and records what it was asked.
type testPrompter struct {
	t       *testing.T
	answers map[PromptKind]string
	// errs makes Ask fail for a kind.
	errs map[PromptKind]error

	mu        sync.Mutex
	asked     []Prompt
	events    []Event
	cancelled []PromptKind // asks whose context ended before an answer
}

func (p *testPrompter) Ask(ctx context.Context, q Prompt) (*Secret, error) {
	p.mu.Lock()
	p.asked = append(p.asked, q)
	p.mu.Unlock()
	if err := p.errs[q.Kind]; err != nil {
		return nil, err
	}
	a, ok := p.answers[q.Kind]
	if !ok {
		p.t.Errorf("unexpected question %v %q", q.Kind, q.Text)
		return nil, ErrCancelled
	}
	if a == block {
		<-ctx.Done()
		p.mu.Lock()
		p.cancelled = append(p.cancelled, q.Kind)
		p.mu.Unlock()
		return nil, ctx.Err()
	}
	return NewSecret([]byte(a)), nil
}

func (p *testPrompter) Notify(e Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, e)
}

func (p *testPrompter) askedKinds() []PromptKind {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []PromptKind
	for _, q := range p.asked {
		out = append(out, q.Kind)
	}
	return out
}

func (p *testPrompter) eventKinds() []EventKind {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []EventKind
	for _, e := range p.events {
		if e.Kind != EventAnswered {
			out = append(out, e.Kind)
		}
	}
	return out
}

func fakeTshPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the fake tsh is a bash script run in a pseudo-terminal")
	}
	p, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake-tsh", "tsh"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// loginEnv is a fake tsh environment with its own TELEPORT_HOME.
type loginEnv struct {
	client  *tsh.CLI
	logs    *syncBuffer
	logger  *slog.Logger
	argvLog string
	home    string
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newLoginEnv(t *testing.T, knobs map[string]string) *loginEnv {
	t.Helper()
	home := t.TempDir()
	e := &loginEnv{logs: &syncBuffer{}, argvLog: filepath.Join(home, "argv.log"), home: home}
	e.logger = slog.New(slog.NewTextHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	environ := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TELEPORT_HOME=" + home,
		"FAKE_TSH_ARGV_LOG=" + e.argvLog,
		"FAKE_TSH_PASSWORD=" + goodPassword,
		"FAKE_TSH_OTP=" + goodOTP,
		"FAKE_TSH_PIN=" + goodPIN,
		"FAKE_TSH_STATE=logged-out",
		// Must never reach tsh: SafeEnv strips it.
		"TSTUI_PASSWORD=" + envPassword,
		"TSTUI_MFA=" + goodOTP,
	}
	for k, v := range knobs {
		environ = append(environ, k+"="+v)
	}
	e.client = tsh.NewCLI(tsh.Options{Bin: fakeTshPath(t), Environ: environ, Logger: e.logger})
	return e
}

// assertNoLeaks checks logs, the argv tsh saw and the error for secrets.
func (e *loginEnv) assertNoLeaks(t *testing.T, err error) {
	t.Helper()
	argv, _ := os.ReadFile(e.argvLog)
	places := map[string]string{"logs": e.logs.String(), "tsh argv/env": string(argv)}
	if err != nil {
		places["error"] = err.Error()
	}
	for where, text := range places {
		for _, s := range allSecrets {
			if strings.Contains(text, s) {
				t.Errorf("secret %q found in %s:\n%s", s, where, text)
			}
		}
	}
	if strings.Contains(string(argv), "TSTUI_PASSWORD") || strings.Contains(string(argv), "TSTUI_MFA") {
		t.Errorf("secret env vars reached tsh: %s", argv)
	}
	if !strings.Contains(e.logs.String(), "login finished") {
		t.Errorf("login was not logged:\n%s", e.logs.String())
	}
}

func secret(s string) *Secret {
	if s == "" {
		return nil
	}
	return NewSecret([]byte(s))
}

func TestLogin(t *testing.T) {
	tests := []struct {
		name          string
		knobs         map[string]string
		password, otp string
		connector     string
		answers       map[PromptKind]string
		errs          map[PromptKind]error
		noPrompter    bool
		timeout       time.Duration
		maxDuration   time.Duration

		wantErr    error
		wantAsked  []PromptKind
		wantEvents []EventKind
		check      func(t *testing.T, p *testPrompter)
	}{
		{name: "password only",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "password"}, password: goodPassword},
		{name: "prompt without a newline",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "password", "FAKE_TSH_PROMPT_NONL": "1"}, password: goodPassword},
		// tsh v18 first asks the terminal for its colours and waits ~5 s
		// for a reply; tstui must reply like a terminal.
		{name: "terminal queries are answered",
			knobs:    map[string]string{"FAKE_TSH_LOGIN": "otp", "FAKE_TSH_TERM_QUERY": "5"},
			password: goodPassword, otp: goodOTP, maxDuration: 3 * time.Second},
		// The hang seen with the real tsh: the queries (no newline) settle
		// as a partial line, then the prompt arrives on the same line.
		{name: "prompt on the same line as earlier output",
			knobs:     map[string]string{"FAKE_TSH_LOGIN": "otp", "FAKE_TSH_TERM_QUERY": "5", "FAKE_TSH_PROMPT_DELAY": "0.5"},
			answers:   map[PromptKind]string{PromptPassword: goodPassword, PromptOTP: goodOTP},
			wantAsked: []PromptKind{PromptPassword, PromptOTP}, maxDuration: 4 * time.Second, timeout: 10 * time.Second},
		{name: "password and otp from the request",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: goodPassword, otp: goodOTP},
		{name: "otp asked through the prompter",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: goodPassword,
			answers:   map[PromptKind]string{PromptOTP: goodOTP},
			wantAsked: []PromptKind{PromptOTP}},
		{name: "everything asked through the prompter",
			knobs:     map[string]string{"FAKE_TSH_LOGIN": "otp"},
			answers:   map[PromptKind]string{PromptPassword: goodPassword, PromptOTP: goodOTP},
			wantAsked: []PromptKind{PromptPassword, PromptOTP}},
		{name: "wrong password",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: badPassword, otp: goodOTP,
			wantErr: ErrWrongPassword},
		{name: "wrong otp",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: goodPassword, otp: badOTP,
			wantErr: ErrWrongOTP},
		{name: "wrong otp from the prompter",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: goodPassword,
			answers: map[PromptKind]string{PromptOTP: badOTP},
			wantErr: ErrWrongOTP, wantAsked: []PromptKind{PromptOTP}},
		{name: "re-prompt after a wrong password is asked as a retry",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "reprompt"}, password: badPassword,
			answers:   map[PromptKind]string{PromptPassword: goodPassword},
			wantAsked: []PromptKind{PromptPassword},
			check: func(t *testing.T, p *testPrompter) {
				if !p.asked[0].Retry {
					t.Error("second password prompt not marked as a retry")
				}
			}},
		{name: "security key tap",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "webauthn"}, password: goodPassword,
			wantEvents: []EventKind{EventTapKey, EventKeyTapped}},
		{name: "tap or otp, otp from the request",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp-or-tap", "FAKE_TSH_TAP_DELAY": "10"}, password: goodPassword, otp: goodOTP},
		{name: "tap or otp, key touched while the question is open",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp-or-tap", "FAKE_TSH_TAP_DELAY": "0.5"}, password: goodPassword,
			answers:    map[PromptKind]string{PromptOTPOrTap: block},
			wantAsked:  []PromptKind{PromptOTPOrTap},
			wantEvents: []EventKind{EventTapKey, EventKeyTapped},
			check: func(t *testing.T, p *testPrompter) {
				deadline := time.Now().Add(2 * time.Second)
				for {
					p.mu.Lock()
					n := len(p.cancelled)
					p.mu.Unlock()
					if n == 1 || time.Now().After(deadline) {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if len(p.cancelled) != 1 {
					t.Error("the open OTP question was not withdrawn after the key tap")
				}
			}},
		{name: "security key PIN",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "pin"}, password: goodPassword,
			answers:    map[PromptKind]string{PromptPIN: goodPIN},
			wantAsked:  []PromptKind{PromptPIN},
			wantEvents: []EventKind{EventTapKey, EventKeyTapped}},
		{name: "choice prompt",
			knobs:   map[string]string{"FAKE_TSH_LOGIN": "choice"},
			answers: map[PromptKind]string{PromptChoice: "1"}, wantAsked: []PromptKind{PromptChoice},
			check: func(t *testing.T, p *testPrompter) {
				if got := strings.Join(p.asked[0].Options, "|"); got != "1: ali|2: bob" {
					t.Errorf("options = %q", got)
				}
			}},
		{name: "locked user",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "locked"}, password: goodPassword, wantErr: ErrLocked},
		{name: "network error",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "network"}, wantErr: ErrNetwork},
		{name: "tls error",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "tls"}, wantErr: ErrTLS},
		{name: "connector not found",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "connector"}, connector: "okta", wantErr: ErrConnectorNotFound},
		{name: "otp needed without a prompter",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: goodPassword, noPrompter: true,
			wantErr: ErrInputNeeded},
		{name: "timeout",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "hang"}, password: goodPassword, timeout: time.Second,
			wantErr: ErrTimeout},
		{name: "cancel from the prompter",
			knobs: map[string]string{"FAKE_TSH_LOGIN": "otp"}, password: goodPassword,
			errs:    map[PromptKind]error{PromptOTP: ErrCancelled},
			wantErr: ErrCancelled, wantAsked: []PromptKind{PromptOTP}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := newLoginEnv(t, tt.knobs)
			svc := NewService(env.client, Options{Logger: env.logger, Timeout: tt.timeout})
			p := &testPrompter{t: t, answers: tt.answers, errs: tt.errs}
			req := LoginRequest{
				Proxy: "tp.afr.example:443", User: "ali", AuthConnector: tt.connector,
				Password: secret(tt.password), OTP: secret(tt.otp), Prompter: p,
			}
			if tt.noPrompter {
				req.Prompter = nil
			}
			pw, otp := req.Password, req.OTP

			start := time.Now()
			err := svc.Login(context.Background(), req)
			elapsed := time.Since(start)

			if tt.wantErr == nil && err != nil || !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v\nlogs:\n%s", err, tt.wantErr, env.logs.String())
			}
			if tt.timeout > 0 && elapsed > tt.timeout+3*time.Second {
				t.Errorf("took %s with a %s timeout", elapsed, tt.timeout)
			}
			if tt.maxDuration > 0 && elapsed > tt.maxDuration {
				t.Errorf("took %s, want under %s", elapsed, tt.maxDuration)
			}
			if !pw.Empty() || !otp.Empty() {
				t.Error("request secrets were not wiped")
			}
			if got, want := kindsString(p.askedKinds()), kindsString(tt.wantAsked); got != want {
				t.Errorf("asked %s, want %s", got, want)
			}
			for _, want := range tt.wantEvents {
				if !containsEvent(p.eventKinds(), want) {
					t.Errorf("events %v lack %v", p.eventKinds(), want)
				}
			}
			if tt.check != nil {
				tt.check(t, p)
			}
			env.assertNoLeaks(t, err)

			st, serr := env.client.Status(context.Background(), req.Proxy)
			if serr != nil {
				t.Fatal(serr)
			}
			if st.LoggedIn != (err == nil) {
				t.Errorf("after login: logged in = %v, login err = %v", st.LoggedIn, err)
			}
		})
	}
}

func kindsString(kinds []PromptKind) string {
	parts := make([]string, len(kinds))
	for i, k := range kinds {
		parts[i] = k.String()
	}
	return "[" + strings.Join(parts, " ") + "]"
}

func containsEvent(events []EventKind, want EventKind) bool {
	for _, e := range events {
		if e == want {
			return true
		}
	}
	return false
}

// TestLoginCancelContext cancels the caller's context while a question
// is open, as Esc in the login modal does.
func TestLoginCancelContext(t *testing.T) {
	env := newLoginEnv(t, map[string]string{"FAKE_TSH_LOGIN": "otp"})
	svc := NewService(env.client, Options{Logger: env.logger})
	ctx, cancel := context.WithCancel(context.Background())
	asked := make(chan struct{})
	p := PrompterFuncs{AskFunc: func(ctx context.Context, _ Prompt) (*Secret, error) {
		close(asked)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	go func() {
		<-asked
		cancel()
	}()
	start := time.Now()
	err := svc.Login(ctx, LoginRequest{Proxy: "tp.afr.example:443", User: "ali", Password: secret(goodPassword), Prompter: p})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("cancel took %s", time.Since(start))
	}
	env.assertNoLeaks(t, err)
}

func TestLoginArgv(t *testing.T) {
	env := newLoginEnv(t, map[string]string{"FAKE_TSH_LOGIN": "password"})
	svc := NewService(env.client, Options{Logger: env.logger})
	err := svc.Login(context.Background(), LoginRequest{
		Proxy: "tp.afr.example:443", User: "ali", AuthConnector: "local", MFAMode: "otp", Insecure: true,
		Password: secret(goodPassword),
	})
	if err != nil {
		t.Fatal(err)
	}
	argv, err := os.ReadFile(env.argvLog)
	if err != nil {
		t.Fatal(err)
	}
	want := "argv: login --proxy=tp.afr.example:443 --user=ali --auth=local --mfa-mode=otp --insecure | env: \n"
	if string(argv) != want {
		t.Errorf("argv log = %q, want %q", argv, want)
	}
}

func TestLogout(t *testing.T) {
	env := newLoginEnv(t, map[string]string{"FAKE_TSH_LOGIN": "password", "FAKE_TSH_STATE": "valid"})
	svc := NewService(env.client, Options{Logger: env.logger})
	ctx := context.Background()
	if err := svc.Logout(ctx, "tp.afr.example:443", "ali"); err != nil {
		t.Fatal(err)
	}
	st, err := env.client.Status(ctx, "tp.afr.example:443")
	if err != nil || st.LoggedIn {
		t.Fatalf("after logout: %+v, %v", st, err)
	}
	// Logging out again is not an error.
	if err := svc.Logout(ctx, "tp.afr.example:443", "ali"); err != nil {
		t.Fatalf("second logout: %v", err)
	}
}

func TestLoginNeedsProxy(t *testing.T) {
	svc := NewService(&tsh.Fake{}, Options{})
	pw := secret(goodPassword)
	if err := svc.Login(context.Background(), LoginRequest{Password: pw}); !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("err = %v", err)
	}
	if !pw.Empty() {
		t.Error("password not wiped on early return")
	}
}
