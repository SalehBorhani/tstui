package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

const (
	e2ePassword = "E2e-Secret-PW-41"
	e2eOTP      = "552718"
	e2eEnvPW    = "env-Secret-PW-19"
)

// simTerminal hands out a simulation screen and lets the test type into
// it once tstui has created it.
type simTerminal struct {
	ready  chan tcell.SimulationScreen
	screen tcell.SimulationScreen
}

func newSimTerminal() *simTerminal {
	return &simTerminal{ready: make(chan tcell.SimulationScreen, 1)}
}

func (s *simTerminal) factory() (tcell.Screen, error) {
	return &signalingScreen{SimulationScreen: tcell.NewSimulationScreen("UTF-8"), ready: s.ready}, nil
}

// signalingScreen hands itself to the test once tview has initialised
// it; touching a simulation screen during Init is a data race.
type signalingScreen struct {
	tcell.SimulationScreen
	ready chan tcell.SimulationScreen
}

func (s *signalingScreen) Init() error {
	if err := s.SimulationScreen.Init(); err != nil {
		return err
	}
	s.SetSize(100, 30)
	s.ready <- s.SimulationScreen
	return nil
}

func (s *simTerminal) wait(t *testing.T) {
	t.Helper()
	select {
	case s.screen = <-s.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("tstui did not open a screen")
	}
}

func (s *simTerminal) text() string {
	// GetContents returns tcell's live cell buffer; hold the screen's lock
	// while reading it, or the next draw races with us.
	cells, w, _ := s.screen.GetContents()
	if l, ok := s.screen.(interface {
		Lock()
		Unlock()
	}); ok {
		l.Lock()
		defer l.Unlock()
	}
	var b strings.Builder
	for i, c := range cells {
		if i > 0 && i%w == 0 {
			b.WriteByte('\n')
		}
		if len(c.Runes) > 0 {
			b.WriteRune(c.Runes[0])
		} else {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func (s *simTerminal) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.text(), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", want, s.text())
}

func (s *simTerminal) typeText(text string) {
	for _, r := range text {
		s.screen.InjectKey(tcell.KeyRune, r, tcell.ModNone)
	}
}

func (s *simTerminal) key(k tcell.Key) { s.screen.InjectKey(k, 0, tcell.ModNone) }

// loginOnlyEnv runs `tstui --login-only` against the fake tsh.
type loginOnlyEnv struct {
	environ          []string
	getenv           func(string) string
	argvLog, logFile string
}

func newLoginOnlyEnv(t *testing.T, scenario string) *loginOnlyEnv {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("the fake tsh is a bash script run in a pseudo-terminal")
	}
	fakeDir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fake-tsh"))
	if err != nil {
		t.Fatal(err)
	}
	// exec looks tsh up in this process's PATH, not in the child's.
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	e := &loginOnlyEnv{argvLog: filepath.Join(home, "argv.log"), logFile: filepath.Join(home, "tstui.log")}
	e.environ = []string{
		"PATH=" + fakeDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"HOME=" + home,
		"TELEPORT_HOME=" + home,
		"FAKE_TSH_STATE=logged-out",
		"FAKE_TSH_LOGIN=" + scenario,
		"FAKE_TSH_PASSWORD=" + e2ePassword,
		"FAKE_TSH_OTP=" + e2eOTP,
		"FAKE_TSH_ARGV_LOG=" + e.argvLog,
		// Start like the real tsh v18: terminal queries, then a wait.
		"FAKE_TSH_TERM_QUERY=5",
		"TSTUI_PASSWORD=" + e2eEnvPW,
	}
	e.getenv = envFunc(map[string]string{"HOME": home})
	return e
}

func (e *loginOnlyEnv) assertNoSecrets(t *testing.T, outputs ...string) {
	t.Helper()
	argv, _ := os.ReadFile(e.argvLog)
	logs, _ := os.ReadFile(e.logFile)
	if len(logs) == 0 {
		t.Error("log file is empty")
	}
	all := append([]string{string(argv), string(logs)}, outputs...)
	for _, text := range all {
		for _, s := range []string{e2ePassword, e2eOTP, e2eEnvPW} {
			if strings.Contains(text, s) {
				t.Errorf("secret %q leaked into:\n%s", s, text)
			}
		}
	}
	if strings.Contains(string(argv), "TSTUI_") {
		t.Errorf("TSTUI_ env vars reached tsh: %s", argv)
	}
}

// TestLoginOnlyFromCommandLine: password from stdin and OTP from a flag
// log in without typing anything.
func TestLoginOnlyFromCommandLine(t *testing.T) {
	e := newLoginOnlyEnv(t, "otp")
	term := newSimTerminal()
	var out, errOut bytes.Buffer
	codeCh := make(chan int, 1)
	start := time.Now()
	go func() {
		codeCh <- runWithScreen(term.factory,
			[]string{"--login-only", "--proxy=tp.afr.example:443", "--user=ali", "--password-stdin",
				"--mfa-token=" + e2eOTP, "--log-file=" + e.logFile},
			strings.NewReader(e2ePassword+"\n"), &out, &errOut, e.getenv, e.environ)
	}()
	term.wait(t)
	select {
	case code := <-codeCh:
		if code != exitOK {
			t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("tstui did not finish; screen:\n%s", term.text())
	}
	if !strings.Contains(out.String(), "logged in to tp.afr.example:443 as ali") {
		t.Errorf("stdout = %q", out.String())
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("login took %s: terminal queries not answered?", elapsed)
	}
	e.assertNoSecrets(t, out.String(), errOut.String())
}

// TestLoginOnlyPopup: nothing secret on the command line; the password
// and the OTP are typed into the popup.
func TestLoginOnlyPopup(t *testing.T) {
	e := newLoginOnlyEnv(t, "otp")
	term := newSimTerminal()
	var out, errOut bytes.Buffer
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- runWithScreen(term.factory,
			[]string{"--login-only", "--proxy=tp.afr.example:443", "--user=ali", "--log-file=" + e.logFile},
			nil, &out, &errOut, e.getenv, e.environ)
	}()
	term.wait(t)
	term.waitFor(t, "<Login>")
	term.typeText(e2ePassword) // the cursor starts in the password field
	term.key(tcell.KeyEnter)
	term.waitFor(t, "authenticator app")
	term.typeText(e2eOTP)
	term.key(tcell.KeyEnter)

	select {
	case code := <-codeCh:
		if code != exitOK {
			t.Fatalf("exit %d\nstderr: %s", code, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("tstui did not finish; screen:\n%s", term.text())
	}
	if !strings.Contains(out.String(), "logged in to tp.afr.example:443 as ali") {
		t.Errorf("stdout = %q", out.String())
	}
	e.assertNoSecrets(t, out.String(), errOut.String())
}

func TestLoginOnlyCancel(t *testing.T) {
	e := newLoginOnlyEnv(t, "otp")
	term := newSimTerminal()
	var out, errOut bytes.Buffer
	codeCh := make(chan int, 1)
	go func() {
		codeCh <- runWithScreen(term.factory,
			[]string{"--login-only", "--proxy=tp.afr.example:443", "--log-file=" + e.logFile},
			nil, &out, &errOut, e.getenv, e.environ)
	}()
	term.wait(t)
	term.waitFor(t, "<Login>")
	term.key(tcell.KeyEscape)
	select {
	case code := <-codeCh:
		if code != exitError || !strings.Contains(errOut.String(), "login cancelled") {
			t.Fatalf("exit %d, stderr %q", code, errOut.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tstui did not exit on Esc")
	}
}

func TestHiddenFlagsNotInUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, nil, &out, &errOut, envFunc(nil), nil); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(errOut.String(), "login-only") || strings.Contains(errOut.String(), "log-file") || strings.Contains(errOut.String(), "-report") {
		t.Errorf("usage shows hidden flags:\n%s", errOut.String())
	}
	if !strings.Contains(errOut.String(), "-password-stdin") {
		t.Errorf("usage lost the visible flags:\n%s", errOut.String())
	}
}

// TestNoSecretsInCapturedLog logs in through every way a secret can come
// in on the command line, flag spellings included, and fails if the
// password or the OTP shows up in the debug log, tsh's argv or
// environment, or anything tstui prints.
func TestNoSecretsInCapturedLog(t *testing.T) {
	pwFile := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(pwFile, []byte(e2ePassword+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"stdin, otp as next arg", []string{"--password-stdin", "--mfa-token", e2eOTP}, e2ePassword + "\n"},
		{"stdin, otp with =", []string{"--password-stdin", "--mfa-token=" + e2eOTP}, e2ePassword + "\n"},
		{"password flag as next arg", []string{"--password", e2ePassword, "--mfa-token", e2eOTP}, ""},
		{"password flag single dash", []string{"-password=" + e2ePassword, "-mfa-token", e2eOTP}, ""},
		{"password file", []string{"--password-file", pwFile, "--mfa-token", e2eOTP}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newLoginOnlyEnv(t, "otp")
			term := newSimTerminal()
			var out, errOut bytes.Buffer
			args := append([]string{"--login-only", "--proxy=tp.afr.example:443", "--user=ali", "--log-file=" + e.logFile}, tt.args...)
			codeCh := make(chan int, 1)
			go func() {
				codeCh <- runWithScreen(term.factory, args, strings.NewReader(tt.stdin), &out, &errOut, e.getenv, e.environ)
			}()
			term.wait(t)
			select {
			case code := <-codeCh:
				if code != exitOK {
					t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, out.String(), errOut.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("tstui did not finish; screen:\n%s", term.text())
			}
			e.assertNoSecrets(t, out.String(), errOut.String())
		})
	}
}
