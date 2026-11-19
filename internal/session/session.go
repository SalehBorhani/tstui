// Package session runs SSH sessions (`tsh ssh` in a pseudo-terminal) for
// the embedded terminal, and keeps them as tabs (Manager).
//
// A Session owns one virtual terminal (vterm.Terminal) for its whole life;
// reconnecting starts a new process on the same terminal, so the
// scrollback survives. Output is parsed in the session's own goroutines
// whether or not its tab is shown, so background sessions keep running and
// keep their screen up to date.
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/root-ali/tstui/internal/tsh"
	"github.com/root-ali/tstui/internal/vterm"
)

// State is where a session is in its life.
type State int

// Session states.
const (
	// Connecting: the process runs but has not written to the terminal
	// yet. tsh writes nothing to stdout before the remote shell starts;
	// its own prompts (per-session MFA) go to stderr.
	Connecting State = iota
	// Connected: the remote side has written output.
	Connected
	// Disconnected: the process ended without a tsh error.
	Disconnected
	// Failed: the process could not start, or tsh reported an error.
	Failed
)

func (s State) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Connected:
		return "connected"
	case Disconnected:
		return "disconnected"
	case Failed:
		return "error"
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// Running reports whether a process is attached in this state.
func (s State) Running() bool { return s == Connecting || s == Connected }

// Options configures a session.
type Options struct {
	// Cols and Rows are the initial terminal size; the view resizes it.
	Cols, Rows int
	// Scrollback is the number of lines kept; 0 means vterm's default.
	Scrollback int
	// OnChange is called after output or a state change, from the
	// session's goroutines. It must not block and must not call back into
	// the session synchronously; the UI uses it to schedule a redraw.
	OnChange func(*Session)
	Logger   *slog.Logger
}

// inputQueue is the number of pending writes to the process; more are
// dropped rather than blocking the caller (the UI).
const inputQueue = 512

// stderrTail is how much of tsh's stderr is kept to explain a failure.
const stderrTail = 8 << 10

// drainTimeout bounds how long output is still read after the process
// exited, in case a background job keeps the terminal open.
const drainTimeout = 500 * time.Millisecond

// Session is one program in a pseudo-terminal with its virtual terminal.
// Its methods are safe for concurrent use.
type Session struct {
	id      int
	spec    Spec
	name    string
	spawner Spawner
	term    *vterm.Terminal
	o       Options
	logger  *slog.Logger

	mu        sync.Mutex
	state     State
	exitCode  int
	err       error
	stderr    []byte
	proc      tsh.Process
	in        chan []byte
	stop      context.CancelFunc
	gen       int
	activity  bool
	closed    bool
	restart   bool      // start again when the current process has ended
	connected time.Time // when the current process first wrote output
	wg        sync.WaitGroup
}

// New creates a session; Start runs its program.
func New(id int, spec Spec, sp Spawner, o Options) *Session {
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	s := &Session{id: id, spec: spec, name: spec.label(), spawner: sp, o: o, logger: o.Logger, state: Disconnected, exitCode: -1}
	s.term = vterm.New(vterm.Options{Cols: o.Cols, Rows: o.Rows, Scrollback: o.Scrollback, Reply: func(b []byte) { s.Send(b) }})
	return s
}

// ID is unique within a Manager.
func (s *Session) ID() int { return s.id }

// Spec is what the session connects to.
func (s *Session) Spec() Spec { return s.spec }

// Name is the tab label.
func (s *Session) Name() string { return s.name }

// Terminal is the session's virtual terminal.
func (s *Session) Terminal() *vterm.Terminal { return s.term }

// State returns the state, the exit code of the last process (-1 while it
// runs or when it was killed), and the failure for Failed.
func (s *Session) State() (State, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, s.exitCode, s.err
}

// Activity reports whether output arrived since ClearActivity.
func (s *Session) Activity() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activity
}

// ClearActivity resets the activity marker (the tab is being looked at).
func (s *Session) ClearActivity() {
	s.mu.Lock()
	s.activity = false
	s.mu.Unlock()
}

// ConnectedFor is how long the current or last process has been connected;
// zero if it never was.
func (s *Session) ConnectedFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connected.IsZero() {
		return 0
	}
	return time.Since(s.connected)
}

// ErrClosed is returned by Start on a closed session.
var ErrClosed = errors.New("session closed")

// ErrRunning is returned by Start while a process is attached.
var ErrRunning = errors.New("session is running")

// Start runs the session's program at the terminal's current size. A
// spawn failure leaves the session Failed and is also returned.
func (s *Session) Start() error {
	err := s.start()
	s.changed()
	return err
}

func (s *Session) start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.proc != nil {
		return ErrRunning
	}
	cols, rows := s.term.Size()
	ctx, stop := context.WithCancel(context.Background())
	o := tsh.PTYOptions{Rows: dim(rows), Cols: dim(cols), Env: TermEnv}
	s.gen++
	gen := s.gen
	if s.spawner.SeparateStderr() {
		o.Stderr = stderrWriter{s: s, gen: gen}
	}
	s.stderr = nil
	s.err = nil
	s.exitCode = -1
	s.connected = time.Time{}

	p, err := s.spawner.Spawn(ctx, s.spec, o)
	if err != nil {
		stop()
		s.state, s.err = Failed, &Error{Kind: ErrFailed, Detail: err.Error()}
		s.logger.Info("session start failed", "id", s.id, "host", s.spec.Host, "err", err)
		return s.err
	}
	in := make(chan []byte, inputQueue)
	s.proc, s.in, s.stop, s.state = p, in, stop, Connecting
	s.logger.Info("session started", "id", s.id, "host", s.spec.Host, "login", s.spec.Login)

	readerDone := make(chan struct{})
	s.wg.Add(3)
	go s.read(gen, p, readerDone)
	go s.write(ctx, p, in)
	go s.monitor(gen, p, readerDone, stop)
	return nil
}

// read copies the process output into the terminal.
func (s *Session) read(gen int, p tsh.Process, done chan<- struct{}) {
	defer s.wg.Done()
	defer close(done)
	// 16 KiB: big enough for throughput, small enough that one Write does
	// not hold the terminal lock (and the next frame) for long.
	buf := make([]byte, 16<<10)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			if _, werr := s.term.Write(buf[:n]); werr != nil {
				// The emulator broke on this output: nothing more can be
				// shown, so end the program; monitor reports the error.
				s.logger.Warn("terminal emulator failed", "id", s.id, "host", s.spec.Host, "err", werr)
				_ = p.Kill()
				return
			}
			s.mu.Lock()
			if gen == s.gen {
				if s.state == Connecting {
					s.state = Connected
					s.connected = time.Now()
				}
				s.activity = true
			}
			s.mu.Unlock()
			s.changed()
		}
		if err != nil {
			return
		}
	}
}

// write sends queued input to the process.
func (s *Session) write(ctx context.Context, p tsh.Process, in <-chan []byte) {
	defer s.wg.Done()
	for {
		select {
		case b := <-in:
			if _, err := p.Write(b); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// monitor waits for the process to end and records why.
func (s *Session) monitor(gen int, p tsh.Process, readerDone <-chan struct{}, stop context.CancelFunc) {
	defer s.wg.Done()
	<-p.Done()
	select {
	case <-readerDone:
	case <-time.After(drainTimeout):
	}
	_ = p.Close() // ends the reader if a background job holds the terminal
	<-readerDone
	stop()

	s.mu.Lock()
	if gen != s.gen {
		s.mu.Unlock()
		return
	}
	s.exitCode = p.ExitCode()
	s.proc, s.in, s.stop = nil, nil, nil
	if err := s.term.Err(); err != nil {
		s.state, s.err = Failed, &Error{Kind: ErrFailed, Detail: err.Error() + "; close this tab and open a new one"}
	} else if err := Classify(s.stderr); err != nil {
		s.state, s.err = Failed, err
	} else {
		s.state = Disconnected
	}
	state, code := s.state, s.exitCode
	restart := s.restart && !s.closed
	s.restart = false
	s.mu.Unlock()
	s.logger.Info("session ended", "id", s.id, "host", s.spec.Host, "state", state.String(), "exit", code)
	s.changed()
	if restart {
		_ = s.Restart()
	}
}

func (s *Session) changed() {
	if s.o.OnChange != nil {
		s.o.OnChange(s)
	}
}

// stderrWriter receives tsh's stderr: it shows it in the terminal (with
// the newline translation the terminal's line discipline would have done)
// and keeps the tail to explain a failure.
type stderrWriter struct {
	s   *Session
	gen int
}

func (w stderrWriter) Write(b []byte) (int, error) {
	s := w.s
	_, _ = s.term.Write(crlf(b))
	s.mu.Lock()
	if w.gen == s.gen {
		s.stderr = append(s.stderr, b...)
		if over := len(s.stderr) - stderrTail; over > 0 {
			s.stderr = append(s.stderr[:0], s.stderr[over:]...)
		}
		s.activity = true
	}
	s.mu.Unlock()
	s.changed()
	return len(b), nil
}

// crlf turns lone \n into \r\n.
func crlf(b []byte) []byte {
	if !bytes.Contains(b, []byte{'\n'}) {
		return b
	}
	out := make([]byte, 0, len(b)+8)
	for i, c := range b {
		if c == '\n' && (i == 0 || b[i-1] != '\r') {
			out = append(out, '\r')
		}
		out = append(out, c)
	}
	return out
}

// Send queues input for the program. It never blocks: input is dropped
// (false) when nothing runs or the queue is full.
func (s *Session) Send(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	s.mu.Lock()
	in := s.in
	s.mu.Unlock()
	if in == nil {
		return false
	}
	select {
	case in <- b:
		return true
	default:
		s.logger.Warn("session input dropped", "id", s.id, "bytes", len(b))
		return false
	}
}

// Resize sets the terminal size and tells the program (SIGWINCH).
func (s *Session) Resize(cols, rows int) {
	if !s.term.Resize(cols, rows) {
		return
	}
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		_ = p.Resize(dim(rows), dim(cols))
	}
}

// dim converts a terminal dimension for the pty ioctl, clamped.
func dim(n int) uint16 {
	switch {
	case n < 1:
		return 1
	case n > math.MaxUint16:
		return math.MaxUint16
	}
	return uint16(n)
}

// resetModes undoes what a program that died mid-screen may have left on:
// alternate screen, mouse reporting, bracketed paste, cursor key mode,
// hidden cursor, colours.
const resetModes = "\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[?1l\x1b[?25h\x1b[0m"

// Restart starts the program again on the same terminal (reconnect).
func (s *Session) Restart() error {
	s.mu.Lock()
	running := s.proc != nil
	s.mu.Unlock()
	if running {
		return ErrRunning
	}
	if err := s.term.Err(); err != nil {
		return err // a broken terminal cannot show a new connection
	}
	_, _ = s.term.Write([]byte(resetModes + "\r\n\x1b[2m── reconnecting ──\x1b[0m\r\n"))
	return s.Start()
}

// Reconnect restarts the session: at once if its program has ended,
// otherwise it kills the program (a hung connection, say) and starts it
// again once it is gone.
func (s *Session) Reconnect() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return ErrClosed
	}
	p := s.proc
	if p != nil {
		s.restart = true
	}
	s.mu.Unlock()
	if p == nil {
		return s.Restart()
	}
	return p.Kill()
}

// Close kills the program, closes its terminal and waits for the
// session's goroutines. It is safe to call more than once.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.wg.Wait()
		return
	}
	s.closed = true
	p, stop := s.proc, s.stop
	s.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
	if stop != nil {
		stop()
	}
	s.wg.Wait()
	s.term.Close()
}
