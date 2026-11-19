package portfwd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
)

// State is a forward's state.
type State int

// Forward states.
const (
	// Starting: tsh runs but the local port is not listening yet (it may
	// be waiting for a security key tap).
	Starting State = iota
	// Active: the local port is listening.
	Active
	// Failed: tsh exited on its own; Info.Err says why.
	Failed
	// Stopped: stopped by the user.
	Stopped
)

func (s State) String() string {
	switch s {
	case Starting:
		return "starting"
	case Active:
		return "active"
	case Failed:
		return "failed"
	}
	return "stopped"
}

// Starter runs tsh in a pseudo-terminal (tsh.Client.StartPTY). A terminal
// lets tsh show a per-session MFA prompt instead of failing outright.
type Starter interface {
	StartPTY(ctx context.Context, args []string, o tsh.PTYOptions) (tsh.Process, error)
}

// Options configures a Manager.
type Options struct {
	// Prober checks local ports; default NetProber.
	Prober Prober
	// OnChange is called after any state change, from the manager's
	// goroutines. It must not block or call back into the manager.
	OnChange func()
	// PollEvery is how often a starting forward checks its port; default
	// 250ms.
	PollEvery time.Duration
	Logger    *slog.Logger
}

// Info is a snapshot of one forward.
type Info struct {
	ID      int
	Spec    Spec
	State   State
	Err     error
	Started time.Time
	// LastLine is the last line tsh printed, e.g. an MFA prompt.
	LastLine string
}

type forward struct {
	id      int
	spec    Spec
	started time.Time
	proc    tsh.Process
	cancel  context.CancelFunc

	state    State
	err      error
	tail     []byte
	stopping bool
}

// tailMax is how much of tsh's output a forward keeps.
const tailMax = 4 << 10

// Manager runs forwards. It is safe for concurrent use.
type Manager struct {
	starter Starter
	o       Options

	mu     sync.Mutex
	fwds   []*forward
	nextID int
	closed bool
	wg     sync.WaitGroup
}

// NewManager returns an empty manager.
func NewManager(s Starter, o Options) *Manager {
	if o.Prober == nil {
		o.Prober = NetProber{}
	}
	if o.PollEvery <= 0 {
		o.PollEvery = 250 * time.Millisecond
	}
	if o.OnChange == nil {
		o.OnChange = func() {}
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Manager{starter: s, o: o, nextID: 1}
}

// ErrClosed is returned by Start after StopAll.
var ErrClosed = errors.New("port forward manager closed")

// Start checks s for conflicts and starts it.
func (m *Manager) Start(s Spec) (int, error) {
	s = s.Normalize()
	args, err := Args(s)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, ErrClosed
	}
	var running []Spec
	for _, f := range m.fwds {
		if f.state == Starting || f.state == Active {
			running = append(running, f.spec)
		}
	}
	if err := Conflict(running, s, m.o.Prober); err != nil {
		m.mu.Unlock()
		return 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	proc, err := m.starter.StartPTY(ctx, args, tsh.PTYOptions{Rows: 24, Cols: 200})
	if err != nil {
		m.mu.Unlock()
		cancel()
		return 0, fmt.Errorf("start forward %s: %w", s.ForwardSpec(), err)
	}
	f := &forward{id: m.nextID, spec: s, started: time.Now(), proc: proc, cancel: cancel}
	m.nextID++
	m.fwds = append(m.fwds, f)
	m.wg.Add(2)
	m.mu.Unlock()

	m.o.Logger.Info("port forward started", "id", f.id, "forward", s.ForwardSpec(), "host", s.Host)
	readerDone := make(chan struct{})
	go m.read(f, readerDone)
	go m.watch(ctx, f, readerDone)
	m.o.OnChange()
	return f.id, nil
}

// read drains tsh's output into the forward's tail.
func (m *Manager) read(f *forward, done chan<- struct{}) {
	defer m.wg.Done()
	defer close(done)
	buf := make([]byte, 1024)
	for {
		n, err := f.proc.Read(buf)
		if n > 0 {
			m.mu.Lock()
			f.tail = append(f.tail, buf[:n]...)
			if len(f.tail) > tailMax {
				f.tail = f.tail[len(f.tail)-tailMax:]
			}
			m.mu.Unlock()
			m.o.OnChange()
		}
		if err != nil {
			return
		}
	}
}

// watch marks the forward active once its port listens, and failed or
// stopped when tsh exits.
func (m *Manager) watch(ctx context.Context, f *forward, readerDone <-chan struct{}) {
	defer m.wg.Done()
	tick := time.NewTicker(m.o.PollEvery)
	defer tick.Stop()
	for {
		select {
		case <-f.proc.Done():
			select {
			case <-readerDone:
			case <-time.After(500 * time.Millisecond):
			}
			_ = f.proc.Close()
			m.mu.Lock()
			if f.stopping {
				f.state = Stopped
			} else {
				f.state = Failed
				f.err = exitError(f.tail, f.proc.ExitCode())
			}
			m.mu.Unlock()
			m.o.Logger.Info("port forward ended", "id", f.id, "exit", f.proc.ExitCode())
			m.o.OnChange()
			return
		case <-tick.C:
			m.mu.Lock()
			starting := f.state == Starting
			m.mu.Unlock()
			if starting && m.o.Prober.Free(f.spec.BindAddr, f.spec.LocalPort) != nil {
				m.mu.Lock()
				if f.state == Starting {
					f.state = Active
				}
				m.mu.Unlock()
				m.o.OnChange()
			}
		case <-ctx.Done():
			// Stop was called: kill and wait for Done above.
			_ = f.proc.Kill()
			ctx = context.Background() // do not select this case again
		}
	}
}

// exitError explains why tsh exited, from the last ERROR line it printed.
func exitError(out []byte, code int) error {
	lines := strings.Split(tsh.CleanOutput(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); strings.HasPrefix(l, "ERROR:") {
			return errors.New(strings.TrimSpace(strings.TrimPrefix(l, "ERROR:")))
		}
	}
	return fmt.Errorf("tsh exited with status %d", code)
}

func lastLine(out []byte) string {
	lines := strings.Split(tsh.CleanOutput(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// Stop stops forward id. Stopping one that already ended does nothing.
func (m *Manager) Stop(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range m.fwds {
		if f.id == id && (f.state == Starting || f.state == Active) {
			f.stopping = true
			f.cancel()
		}
	}
}

// Remove forgets a forward that has ended.
func (m *Manager) Remove(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, f := range m.fwds {
		if f.id == id && (f.state == Failed || f.state == Stopped) {
			f.cancel()
			m.fwds = append(m.fwds[:i], m.fwds[i+1:]...)
			return
		}
	}
}

// List returns a snapshot of every forward, oldest first.
func (m *Manager) List() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, len(m.fwds))
	for i, f := range m.fwds {
		out[i] = Info{ID: f.id, Spec: f.spec, State: f.state, Err: f.err, Started: f.started, LastLine: lastLine(f.tail)}
	}
	return out
}

// Running is the number of forwards starting or active.
func (m *Manager) Running() int {
	n := 0
	for _, f := range m.List() {
		if f.State == Starting || f.State == Active {
			n++
		}
	}
	return n
}

// StopAll stops every forward and waits until their tsh processes have
// exited (at most a few seconds). The manager accepts no new forwards
// afterwards. tstui calls it on quit.
func (m *Manager) StopAll() {
	m.mu.Lock()
	m.closed = true
	for _, f := range m.fwds {
		if f.state == Starting || f.state == Active {
			f.stopping = true
		}
		f.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}
