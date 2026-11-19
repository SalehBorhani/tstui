package portfwd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/root-ali/tstui/internal/tsh"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestParseForward(t *testing.T) {
	tests := []struct {
		in    string
		bind  string
		lport int
		rhost string
		rport int
		err   bool
	}{
		{"8080:localhost:80", "127.0.0.1", 8080, "localhost", 80, false},
		{"0.0.0.0:5432:db.internal:5432", "0.0.0.0", 5432, "db.internal", 5432, false},
		{"9000:9001", "127.0.0.1", 9000, "localhost", 9001, false},
		{"80", "", 0, "", 0, true},
		{"0:host:80", "", 0, "", 0, true},
		{"8080:host:70000", "", 0, "", 0, true},
		{"a:b:c:d:e", "", 0, "", 0, true},
		{"x:host:80", "", 0, "", 0, true},
	}
	for _, tt := range tests {
		bind, lport, rhost, rport, err := ParseForward(tt.in)
		if (err != nil) != tt.err || bind != tt.bind || lport != tt.lport || rhost != tt.rhost || rport != tt.rport {
			t.Errorf("ParseForward(%q) = %q %d %q %d %v", tt.in, bind, lport, rhost, rport, err)
		}
	}
}

func TestArgs(t *testing.T) {
	base := Spec{Proxy: "tp:443", Login: "ali", Host: "web-01", LocalPort: 8080, RemotePort: 80}
	got, err := Args(base.Normalize())
	want := []string{"ssh", "--proxy=tp:443", "-N", "--forward=127.0.0.1:8080:localhost:80", "ali@web-01"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Args = %q, %v; want %q", got, err, want)
	}
	s := base
	s.Insecure, s.BindAddr, s.RemoteHost = true, "0.0.0.0", "db.internal"
	got, _ = Args(s.Normalize())
	if strings.Join(got, " ") != "ssh --proxy=tp:443 --insecure -N --forward=0.0.0.0:8080:db.internal:80 ali@web-01" {
		t.Fatalf("Args = %q", got)
	}
	s = base
	s.Cluster = "hwb.example"
	got, _ = Args(s.Normalize())
	if strings.Join(got, " ") != "ssh --proxy=tp:443 --cluster=hwb.example -N --forward=127.0.0.1:8080:localhost:80 ali@web-01" {
		t.Fatalf("Args = %q", got)
	}

	bad := []struct {
		name string
		f    func(*Spec)
		err  string
	}{
		{"dash host", func(s *Spec) { s.Host = "-oProxyCommand=sh" }, "invalid server"},
		{"host with at", func(s *Spec) { s.Host = "root@x" }, "invalid server"},
		{"dash login", func(s *Spec) { s.Login = "-L1:2:3" }, "invalid login"},
		{"no login", func(s *Spec) { s.Login = "" }, "no login"},
		{"remote host colon", func(s *Spec) { s.RemoteHost = "a:1" }, "invalid remote host"},
		{"remote host dash", func(s *Spec) { s.RemoteHost = "-x" }, "invalid remote host"},
		{"remote host space", func(s *Spec) { s.RemoteHost = "a b" }, "invalid remote host"},
		{"bad bind", func(s *Spec) { s.BindAddr = "::1" }, "IPv4"},
		{"bad bind 2", func(s *Spec) { s.BindAddr = "$(id)" }, "IPv4"},
		{"local port", func(s *Spec) { s.LocalPort = 0 }, "local port"},
		{"remote port", func(s *Spec) { s.RemotePort = 99999 }, "remote port"},
	}
	for _, tt := range bad {
		s := base
		tt.f(&s)
		if _, err := Args(s.Normalize()); err == nil || !strings.Contains(err.Error(), tt.err) {
			t.Errorf("%s: err = %v, want %q", tt.name, err, tt.err)
		}
	}
}

type fakeProber struct {
	mu    sync.Mutex
	taken map[string]bool
}

func (p *fakeProber) Free(bind string, port int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.taken[fmt.Sprintf("%s:%d", bind, port)] {
		return fmt.Errorf("%w: %s:%d", ErrConflict, bind, port)
	}
	return nil
}

func (p *fakeProber) take(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.taken == nil {
		p.taken = map[string]bool{}
	}
	p.taken[addr] = true
}

func TestConflict(t *testing.T) {
	running := []Spec{
		{Login: "a", Host: "h", BindAddr: "127.0.0.1", LocalPort: 8080, RemoteHost: "localhost", RemotePort: 80},
		{Login: "a", Host: "h", BindAddr: "0.0.0.0", LocalPort: 9000, RemoteHost: "localhost", RemotePort: 90},
	}
	p := &fakeProber{}
	p.take("127.0.0.1:3000")
	tests := []struct {
		bind string
		port int
		err  bool
	}{
		{"127.0.0.1", 8080, true},  // same as a running forward
		{"localhost", 8080, true},  // localhost is 127.0.0.1
		{"0.0.0.0", 8080, true},    // wildcard overlaps
		{"127.0.0.2", 8080, false}, // another loopback address
		{"127.0.0.1", 9000, true},  // running one is on the wildcard
		{"127.0.0.1", 3000, true},  // something else holds it
		{"127.0.0.1", 3001, false},
	}
	for _, tt := range tests {
		err := Conflict(running, Spec{BindAddr: tt.bind, LocalPort: tt.port}, p)
		if (err != nil) != tt.err || (err != nil && !errors.Is(err, ErrConflict)) {
			t.Errorf("Conflict(%s:%d) = %v, want conflict %v", tt.bind, tt.port, err, tt.err)
		}
	}
}

func TestNetProber(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skip("no loopback:", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := (NetProber{}).Free("127.0.0.1", port); !errors.Is(err, ErrConflict) {
		t.Fatalf("busy port: %v", err)
	}
	l.Close()
	if err := (NetProber{}).Free("127.0.0.1", port); err != nil {
		t.Fatalf("freed port: %v", err)
	}
}

// fakeProc is an in-memory tsh process.
type fakeProc struct {
	r        *io.PipeReader
	w        *io.PipeWriter
	done     chan struct{}
	once     sync.Once
	exitCode int
}

func newFakeProc() *fakeProc {
	r, w := io.Pipe()
	return &fakeProc{r: r, w: w, done: make(chan struct{}), exitCode: -1}
}

func (p *fakeProc) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *fakeProc) Write(b []byte) (int, error) { return len(b), nil }
func (p *fakeProc) Resize(_, _ uint16) error    { return nil }
func (p *fakeProc) Done() <-chan struct{}       { return p.done }
func (p *fakeProc) ExitCode() int               { return p.exitCode }
func (p *fakeProc) Kill() error                 { p.exit(-1, ""); return nil }
func (p *fakeProc) Close() error                { p.exit(-1, ""); return p.r.Close() }
func (p *fakeProc) exit(code int, out string) {
	p.once.Do(func() {
		if out != "" {
			_, _ = p.w.Write([]byte(out))
		}
		p.exitCode = code
		_ = p.w.Close()
		close(p.done)
	})
}

type fakeStarter struct {
	mu    sync.Mutex
	procs []*fakeProc
	args  [][]string
}

func (s *fakeStarter) StartPTY(_ context.Context, args []string, _ tsh.PTYOptions) (tsh.Process, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := newFakeProc()
	s.procs = append(s.procs, p)
	s.args = append(s.args, args)
	return p, nil
}

func (s *fakeStarter) proc(i int) *fakeProc {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.procs[i]
}

func waitState(t *testing.T, m *Manager, id int, want State) Info {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, f := range m.List() {
			if f.ID == id && f.State == want {
				return f
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("forward %d never reached %s: %+v", id, want, m.List())
	return Info{}
}

func TestManager(t *testing.T) {
	st := &fakeStarter{}
	p := &fakeProber{}
	m := NewManager(st, Options{Prober: p, PollEvery: 5 * time.Millisecond})
	defer m.StopAll()

	spec := Spec{Login: "ali", Host: "web-01", LocalPort: 8080, RemotePort: 80}
	id, err := m.Start(spec)
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, m, id, Starting)
	if _, err := m.Start(spec); !errors.Is(err, ErrConflict) {
		t.Fatalf("second forward on the same port: %v", err)
	}
	p.take("127.0.0.1:8080") // tsh listens now
	waitState(t, m, id, Active)
	if m.Running() != 1 {
		t.Fatalf("Running = %d", m.Running())
	}

	// A forward whose tsh dies reports tsh's error.
	id2, err := m.Start(Spec{Login: "ali", Host: "db-03", LocalPort: 5432, RemotePort: 5432})
	if err != nil {
		t.Fatal(err)
	}
	st.proc(1).exit(1, "\x1b[31mERROR: \x1b[0maccess denied to ali connecting to db-03\r\n")
	if f := waitState(t, m, id2, Failed); f.Err == nil || f.Err.Error() != "access denied to ali connecting to db-03" {
		t.Fatalf("failed forward error = %v", f.Err)
	}
	m.Remove(id2)
	if len(m.List()) != 1 {
		t.Fatalf("Remove left %+v", m.List())
	}

	m.Stop(id)
	waitState(t, m, id, Stopped)
	if m.Running() != 0 {
		t.Fatal("stopped forward still counted")
	}
}

func TestManagerStopAll(t *testing.T) {
	st := &fakeStarter{}
	m := NewManager(st, Options{Prober: &fakeProber{}, PollEvery: 5 * time.Millisecond})
	for port := 7000; port < 7003; port++ {
		if _, err := m.Start(Spec{Login: "a", Host: "h", LocalPort: port, RemotePort: 80}); err != nil {
			t.Fatal(err)
		}
	}
	m.StopAll() // returns only when every process is gone (goleak checks goroutines)
	for i := range 3 {
		select {
		case <-st.proc(i).Done():
		default:
			t.Fatalf("process %d still running after StopAll", i)
		}
	}
	for _, f := range m.List() {
		if f.State != Stopped {
			t.Fatalf("after StopAll: %+v", f)
		}
	}
	if _, err := m.Start(Spec{Login: "a", Host: "h", LocalPort: 7100, RemotePort: 80}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start after StopAll: %v", err)
	}
}
