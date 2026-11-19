package session

import (
	"context"
	"io"
	"sync"

	"github.com/root-ali/tstui/internal/tsh"
)

// Fake is an in-memory Spawner for tests of packages that run sessions.
// Each Spawn makes a FakeProcess the test drives: it writes the program's
// output, reads what was typed, and ends it.
type Fake struct {
	// Err, if set, is returned by Spawn.
	Err error

	mu    sync.Mutex
	procs []*FakeProcess
	specs []Spec
}

var _ Spawner = (*Fake)(nil)

// Spawn implements Spawner.
func (f *Fake) Spawn(_ context.Context, spec Spec, o tsh.PTYOptions) (tsh.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Err
	}
	r, w := io.Pipe()
	p := &FakeProcess{out: r, outW: w, done: make(chan struct{}), exitCode: -1, rows: o.Rows, cols: o.Cols}
	f.procs = append(f.procs, p)
	f.specs = append(f.specs, spec)
	return p, nil
}

// SeparateStderr implements Spawner.
func (*Fake) SeparateStderr() bool { return false }

// Count is the number of processes spawned.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.procs)
}

// Proc returns the i-th process spawned.
func (f *Fake) Proc(i int) *FakeProcess {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[i]
}

// Spec returns the spec of the i-th spawn.
func (f *Fake) Spec(i int) Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[i]
}

// FakeProcess is a tsh.Process in memory.
type FakeProcess struct {
	out  *io.PipeReader
	outW *io.PipeWriter
	done chan struct{}
	once sync.Once

	mu         sync.Mutex
	input      []byte
	rows, cols uint16
	exitCode   int
}

// Output makes the program write s. It blocks until the session read it.
func (p *FakeProcess) Output(s string) { _, _ = p.outW.Write([]byte(s)) }

// Exit ends the program with code.
func (p *FakeProcess) Exit(code int) {
	p.once.Do(func() {
		p.mu.Lock()
		p.exitCode = code
		p.mu.Unlock()
		_ = p.outW.Close()
		close(p.done)
	})
}

// Input returns everything typed so far.
func (p *FakeProcess) Input() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.input...)
}

// Size is the terminal size the program sees.
func (p *FakeProcess) Size() (cols, rows int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int(p.cols), int(p.rows)
}

// Exited reports whether the process has ended.
func (p *FakeProcess) Exited() bool {
	select {
	case <-p.done:
		return true
	default:
		return false
	}
}

// Read implements tsh.Process.
func (p *FakeProcess) Read(b []byte) (int, error) { return p.out.Read(b) }

// Write implements tsh.Process.
func (p *FakeProcess) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.input = append(p.input, b...)
	return len(b), nil
}

// Resize implements tsh.Process.
func (p *FakeProcess) Resize(rows, cols uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rows, p.cols = rows, cols
	return nil
}

// Done implements tsh.Process.
func (p *FakeProcess) Done() <-chan struct{} { return p.done }

// ExitCode implements tsh.Process.
func (p *FakeProcess) ExitCode() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exitCode
}

// Kill implements tsh.Process: the program ends as if killed (-1).
func (p *FakeProcess) Kill() error { p.Exit(-1); return nil }

// Close implements tsh.Process.
func (p *FakeProcess) Close() error { p.Exit(-1); return nil }
