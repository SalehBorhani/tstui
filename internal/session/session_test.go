package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/root-ali/tstui/internal/tsh"
	"github.com/root-ali/tstui/internal/vterm"
)

type vtScreen = vterm.Screen

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// shSpawner runs a /bin/sh script in a real pseudo-terminal: the fake
// session process.
type shSpawner struct {
	script   string
	separate bool
	err      error
	mu       sync.Mutex
	opts     []tsh.PTYOptions
}

func (s *shSpawner) Spawn(ctx context.Context, _ Spec, o tsh.PTYOptions) (tsh.Process, error) {
	s.mu.Lock()
	s.opts = append(s.opts, o)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return tsh.StartCommandPTY(ctx, "/bin/sh", []string{"-c", s.script}, os.Environ(), o)
}

func (s *shSpawner) SeparateStderr() bool { return s.separate }

func newShSession(t *testing.T, sp *shSpawner, o Options) *Session {
	t.Helper()
	if o.Cols == 0 {
		o.Cols, o.Rows = 80, 24
	}
	s := New(1, Spec{Proxy: "p:443", Host: "web-01", Login: "ali"}, sp, o)
	t.Cleanup(s.Close)
	return s
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitState(t *testing.T, s *Session, want State) (int, error) {
	t.Helper()
	var code int
	var err error
	waitFor(t, "state "+want.String(), func() bool {
		var st State
		st, code, err = s.State()
		return st == want
	})
	return code, err
}

func waitText(t *testing.T, s *Session, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.Terminal().Text(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", want, s.Terminal().Text())
}

func TestSessionEcho(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `printf 'hello from %s\n' "$TERM"`}, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	code, err := waitState(t, s, Disconnected)
	if code != 0 || err != nil {
		t.Fatalf("exit %d err %v", code, err)
	}
	if got := s.Terminal().Text(); !strings.Contains(got, "hello from xterm-256color") {
		t.Fatalf("screen = %q", got)
	}
	if !s.Activity() {
		t.Error("output did not set the activity marker")
	}
}

func TestSessionExitCodes(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		separate bool
		state    State
		code     int
		kind     error
	}{
		{"clean exit", "exit 0", false, Disconnected, 0, nil},
		{"remote exit status", "echo bye; exit 3", false, Disconnected, 3, nil},
		{"killed by a signal", "kill -9 $$", false, Disconnected, -1, nil},
		{"access denied", `printf '\033[31mERROR: \033[0maccess denied to ali connecting to web-01\n' >&2; exit 1`, true, Failed, 1, ErrAccessDenied},
		{"cert expired", `echo 'ERROR: ssh: cert has expired' >&2; exit 1`, true, Failed, 1, ErrCertExpired},
		{"host unreachable", `echo 'ERROR: failed to dial: dial tcp 10.0.0.1:3022: connect: no route to host' >&2; exit 1`, true, Failed, 1, ErrUnreachable},
		{"mfa required", `echo 'ERROR: MFA is required to access this resource' >&2; exit 1`, true, Failed, 1, ErrMFARequired},
		{"unknown tsh error", `echo 'ERROR: something new' >&2; exit 1`, true, Failed, 1, ErrFailed},
		// Without a separate stderr (the demo shell) nothing is classified.
		{"merged stderr", `echo 'ERROR: access denied' >&2; exit 1`, false, Disconnected, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newShSession(t, &shSpawner{script: tt.script, separate: tt.separate}, Options{})
			if err := s.Start(); err != nil {
				t.Fatal(err)
			}
			code, err := waitState(t, s, tt.state)
			if code != tt.code {
				t.Errorf("exit code = %d, want %d", code, tt.code)
			}
			if tt.kind == nil && err != nil {
				t.Errorf("err = %v, want none", err)
			}
			if tt.kind != nil && !errors.Is(err, tt.kind) {
				t.Errorf("err = %v, want %v", err, tt.kind)
			}
		})
	}
}

// tsh's stderr is shown in the terminal too, with CRLF line ends.
func TestSessionStderrShown(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `echo 'ERROR: one' >&2; echo 'second line' >&2; exit 1`, separate: true}, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Failed)
	lines := strings.Split(s.Terminal().Text(), "\n")
	if lines[0] != "ERROR: one" || lines[1] != "second line" {
		t.Fatalf("screen = %q", lines[:3])
	}
}

func TestSessionInput(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `read line; echo "got:$line"`}, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if !s.Send([]byte("abc\r")) {
		t.Fatal("input refused")
	}
	waitText(t, s, "got:abc")
	waitState(t, s, Disconnected)
	if s.Send([]byte("x")) {
		t.Error("input accepted with nothing running")
	}
}

func TestSessionConnectingThenConnected(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `sleep 0.3; echo hi; sleep 30`}, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := s.State(); st != Connecting {
		t.Fatalf("state = %v, want connecting", st)
	}
	waitState(t, s, Connected)
	if s.ConnectedFor() <= 0 {
		t.Error("ConnectedFor not set")
	}
}

func TestSessionLargeOutput(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `i=0; while [ $i -lt 20000 ]; do echo "line $i"; i=$((i+1)); done`}, Options{Scrollback: 30000})
	start := time.Now()
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Disconnected)
	t.Logf("20000 lines in %s", time.Since(start).Round(time.Millisecond))
	var first, last string
	s.Terminal().View(func(v *vtScreen) {
		first = v.LineText(0, 0, -1)
		for i := v.Lines() - 1; i >= 0; i-- {
			if last = v.LineText(i, 0, -1); last != "" {
				break
			}
		}
	})
	if first != "line 0" || last != "line 19999" {
		t.Fatalf("first %q last %q", first, last)
	}
}

func TestSessionResize(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `stty size; read x; stty size; read x`}, Options{Cols: 80, Rows: 24})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitText(t, s, "24 80")
	s.Resize(100, 30)
	if c, r := s.Terminal().Size(); c != 100 || r != 30 {
		t.Fatalf("terminal size %dx%d", c, r)
	}
	s.Send([]byte("\r"))
	waitText(t, s, "30 100")
}

// Close kills a running process group, children included.
func TestSessionCloseKills(t *testing.T) {
	s := newShSession(t, &shSpawner{script: `sleep 1000 & sleep 1000`}, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close hung")
	}
	if err := s.Start(); !errors.Is(err, ErrClosed) {
		t.Errorf("Start after Close = %v", err)
	}
}

func TestSessionRestart(t *testing.T) {
	sp := &shSpawner{script: `echo run`}
	s := newShSession(t, sp, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Disconnected)
	if err := s.Restart(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second run", func() bool { return strings.Count(s.Terminal().Text(), "run") >= 2 })
	waitState(t, s, Disconnected)
	if !strings.Contains(s.Terminal().Text(), "reconnecting") {
		t.Error("no reconnect separator")
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if len(sp.opts) != 2 {
		t.Fatalf("spawned %d times", len(sp.opts))
	}
	env := strings.Join(sp.opts[0].Env, " ")
	if !strings.Contains(env, "TERM=xterm-256color") {
		t.Errorf("child env extras = %q", env)
	}
}

// Reconnect on a running (hung) session kills it and starts it again.
func TestSessionReconnectRunning(t *testing.T) {
	sp := &shSpawner{script: `echo up; sleep 1000`}
	s := newShSession(t, sp, Options{})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Connected)
	if err := s.Reconnect(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "second process", func() bool { return strings.Count(s.Terminal().Text(), "up") >= 2 })
	if st, _, _ := s.State(); !st.Running() {
		t.Fatalf("state %v after reconnect", st)
	}
}

func TestSessionSpawnFailure(t *testing.T) {
	s := newShSession(t, &shSpawner{err: errors.New("tsh binary not found")}, Options{})
	if err := s.Start(); err == nil {
		t.Fatal("no error")
	}
	st, _, err := s.State()
	if st != Failed || !strings.Contains(err.Error(), "tsh binary not found") {
		t.Fatalf("state %v err %v", st, err)
	}
}

func TestSessionOnChange(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	s := newShSession(t, &shSpawner{script: `echo x`}, Options{OnChange: func(*Session) {
		mu.Lock()
		calls++
		mu.Unlock()
	}})
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitState(t, s, Disconnected)
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 { // start, output, exit
		t.Errorf("OnChange called %d times", calls)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		stderr string
		kind   error
		detail string
	}{
		{"", nil, ""},
		{"Tap any security key\n", nil, ""},
		{"\x1b[31mERROR: \x1b[0maccess denied to ali connecting to db-03 on cluster afr\n\n", ErrAccessDenied, "access denied to ali connecting to db-03 on cluster afr"},
		{"ERROR: ssh: cert has expired\n", ErrCertExpired, "ssh: cert has expired"},
		{"ERROR: Not logged in.\n", ErrCertExpired, "Not logged in."},
		{"ERROR: dial tcp 10.0.0.1:3022: i/o timeout\n", ErrUnreachable, ""},
		{"ERROR: node \"web-99\" not found\n", ErrNotFound, ""},
		{"ERROR: first\nnoise\nERROR: access denied\n", ErrAccessDenied, "access denied"},
		{"ERROR: weird\n", ErrFailed, "weird"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.stderr), func(t *testing.T) {
			err := Classify([]byte(tt.stderr))
			if tt.kind == nil {
				if err != nil {
					t.Fatalf("got %v", err)
				}
				return
			}
			if !errors.Is(err, tt.kind) {
				t.Fatalf("got %v, want %v", err, tt.kind)
			}
			var e *Error
			if tt.detail != "" && (!errors.As(err, &e) || e.Detail != tt.detail) {
				t.Errorf("detail = %q, want %q", e.Detail, tt.detail)
			}
		})
	}
}

func TestSSHArgs(t *testing.T) {
	tests := []struct {
		spec     Spec
		insecure bool
		want     string
	}{
		{Spec{Proxy: "tp.afr.example:443", Host: "web-01", Login: "root"}, false, "ssh --proxy=tp.afr.example:443 root@web-01"},
		{Spec{Proxy: "p", Host: "h", Login: "ali"}, true, "ssh --proxy=p --insecure ali@h"},
		{Spec{Host: "h"}, false, "ssh h"},
		// A leaf cluster's node; the name is one argument, never a flag.
		{Spec{Proxy: "p", Cluster: "afr.example", Host: "h", Login: "ali"}, true, "ssh --proxy=p --cluster=afr.example --insecure ali@h"},
		{Spec{Proxy: "p", Cluster: "-oProxyCommand=x", Host: "h", Login: "ali"}, false, "ssh --proxy=p --cluster=-oProxyCommand=x ali@h"},
	}
	for _, tt := range tests {
		if got := strings.Join(SSHArgs(tt.spec, tt.insecure), " "); got != tt.want {
			t.Errorf("SSHArgs(%+v) = %q, want %q", tt.spec, got, tt.want)
		}
	}
}

func TestTshSpawnerRejectsFlags(t *testing.T) {
	fake := &tsh.Fake{}
	for _, spec := range []Spec{{Host: "-oProxyCommand=x", Login: "a"}, {Host: "h", Login: "--proxy=evil"}, {Login: "a"}} {
		if _, err := (TshSpawner{Client: fake}).Spawn(context.Background(), spec, tsh.PTYOptions{}); err == nil {
			t.Errorf("spec %+v accepted", spec)
		}
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("tsh was called: %v", calls)
	}
}
