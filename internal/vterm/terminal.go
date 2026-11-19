// Package vterm is tstui's virtual terminal: a VT emulator
// (charmbracelet/x/vt, see docs/terminal-decision.md) behind a mutex, plus
// the encoders that turn tcell key, mouse and paste events into the bytes
// a program in a terminal expects.
//
// The emulator's screen is read through View, which holds the lock for a
// whole frame, so a draw never sees half an update.
package vterm

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// DefaultScrollback is the number of lines kept above the screen.
const DefaultScrollback = 10000

// Modes are the terminal modes the program has set that change what input
// must look like, or how the view behaves.
type Modes struct {
	AppCursor      bool // DECCKM: cursor keys send SS3 instead of CSI
	BracketedPaste bool // 2004
	AltScreen      bool // 1049/1047/47
	// Mouse reporting: X10 (9), normal (1000), button-event (1002) and
	// any-event (1003) tracking; SGR (1006) changes the encoding.
	MouseX10, MouseNormal, MouseButton, MouseAny, MouseSGR bool
	FocusEvents                                            bool // 1004
}

// MouseReporting reports whether the program wants mouse events.
func (m Modes) MouseReporting() bool {
	return m.MouseX10 || m.MouseNormal || m.MouseButton || m.MouseAny
}

// Options configures New.
type Options struct {
	Cols, Rows int
	// Scrollback is the number of lines kept; 0 means DefaultScrollback.
	Scrollback int
	// Reply receives what the terminal answers to the program's queries
	// (cursor position, device attributes, colours). It is called from a
	// goroutine of the Terminal and must not block for long: the program's
	// output is not parsed while it does. The slice is not reused.
	Reply func([]byte)
}

// ErrEmulator means the emulator panicked on the program's output (a bug
// in it, or a sequence it cannot handle). The terminal is then broken:
// Write and Resize do nothing more, and View shows a blank screen, so one
// session cannot take the whole application down.
var ErrEmulator = errors.New("terminal emulator failed")

// Terminal is a virtual terminal. It is safe for concurrent use.
type Terminal struct {
	mu     sync.Mutex
	emu    *vt.Emulator
	modes  Modes
	title  string
	broken error // set once the emulator has panicked; see ErrEmulator

	replies   *io.PipeWriter // the emulator's reply pipe, closed by Close
	pumpDone  chan struct{}
	closeOnce sync.Once
}

// New returns a terminal of the given size; zero sizes mean 80x24.
func New(o Options) *Terminal {
	if o.Cols <= 0 || o.Rows <= 0 {
		o.Cols, o.Rows = 80, 24
	}
	if o.Scrollback <= 0 {
		o.Scrollback = DefaultScrollback
	}
	t := &Terminal{emu: vt.NewEmulator(o.Cols, o.Rows), pumpDone: make(chan struct{})}
	t.emu.SetScrollbackSize(o.Scrollback)
	// The callbacks run inside emu.Write, so under t.mu.
	t.emu.SetCallbacks(vt.Callbacks{
		EnableMode:  func(m ansi.Mode) { t.setMode(m, true) },
		DisableMode: func(m ansi.Mode) { t.setMode(m, false) },
		AltScreen:   func(on bool) { t.modes.AltScreen = on },
		Title:       func(s string) { t.title = s },
	})
	// InputPipe is the write end of the pipe Read drains. Closing it
	// directly, rather than through emu.Close, ends the pump without a
	// data race on the emulator's closed flag.
	t.replies, _ = t.emu.InputPipe().(*io.PipeWriter)
	go t.pump(o.Reply)
	return t
}

// pump forwards the emulator's replies. Without a reader, the emulator
// would block in Write on the first query.
func (t *Terminal) pump(reply func([]byte)) {
	defer close(t.pumpDone)
	buf := make([]byte, 4096)
	for {
		n, err := t.emu.Read(buf)
		if n > 0 && reply != nil {
			reply(append([]byte(nil), buf[:n]...))
		}
		if err != nil {
			return
		}
	}
}

func (t *Terminal) setMode(m ansi.Mode, on bool) {
	switch m {
	case ansi.ModeCursorKeys:
		t.modes.AppCursor = on
	case ansi.ModeBracketedPaste:
		t.modes.BracketedPaste = on
	case ansi.ModeMouseX10:
		t.modes.MouseX10 = on
	case ansi.ModeMouseNormal:
		t.modes.MouseNormal = on
	case ansi.ModeMouseButtonEvent:
		t.modes.MouseButton = on
	case ansi.ModeMouseAnyEvent:
		t.modes.MouseAny = on
	case ansi.ModeMouseExtSgr:
		t.modes.MouseSGR = on
	case ansi.ModeFocusEvent:
		t.modes.FocusEvents = on
	case ansi.ModeAltScreenSaveCursor, ansi.ModeAltScreen:
		t.modes.AltScreen = on
	}
}

// Write feeds program output to the terminal. It fails only once the
// emulator is broken (ErrEmulator).
func (t *Terminal) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken != nil {
		return 0, t.broken
	}
	defer t.catch("write", &err)
	return t.emu.Write(p)
}

// catch, deferred under t.mu, turns an emulator panic into ErrEmulator.
func (t *Terminal) catch(op string, err *error) {
	if r := recover(); r != nil {
		t.broken = fmt.Errorf("%w: %s: %v", ErrEmulator, op, r)
		if err != nil {
			*err = t.broken
		}
	}
}

// Err is ErrEmulator (wrapped, with the panic) once the emulator is
// broken, else nil.
func (t *Terminal) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.broken
}

// Resize changes the screen size and reports whether it changed.
func (t *Terminal) Resize(cols, rows int) (changed bool) {
	if cols <= 0 || rows <= 0 {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken != nil || t.emu.Width() == cols && t.emu.Height() == rows {
		return false
	}
	defer t.catch("resize", nil)
	t.emu.Resize(cols, rows)
	return true
}

// Size returns the screen size.
func (t *Terminal) Size() (cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.emu.Width(), t.emu.Height()
}

// Modes returns the current input-relevant modes.
func (t *Terminal) Modes() Modes {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.modes
}

// Title is the window title the program set (OSC 0/2).
func (t *Terminal) Title() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.title
}

// View calls f with the screen locked. f must not call other Terminal
// methods. On a broken terminal f is not called; a panic in f breaks it.
func (t *Terminal) View(f func(s *Screen)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.broken != nil {
		return
	}
	defer t.catch("draw", nil)
	f(&Screen{t: t})
}

// Text returns the visible screen as text, one line per row, trailing
// blanks trimmed.
func (t *Terminal) Text() string {
	var out string
	t.View(func(s *Screen) {
		_, rows := s.Size()
		lines := make([]string, rows)
		for y := range rows {
			lines[y] = s.LineText(s.ScrollbackLen()+y, 0, -1)
		}
		out = strings.Join(lines, "\n")
	})
	return out
}

// Close stops the reply goroutine. Writes after Close are parsed but no
// longer answered.
func (t *Terminal) Close() {
	t.closeOnce.Do(func() {
		if t.replies != nil {
			_ = t.replies.Close()
		}
		<-t.pumpDone
	})
}

// Screen reads the terminal while View holds its lock.
//
// Lines are numbered in one space: 0 is the oldest scrollback line,
// ScrollbackLen() is the top row of the screen, and Lines()-1 the bottom.
type Screen struct{ t *Terminal }

// Size is the screen size.
func (s *Screen) Size() (cols, rows int) { return s.t.emu.Width(), s.t.emu.Height() }

// Modes are the terminal modes.
func (s *Screen) Modes() Modes { return s.t.modes }

// ScrollbackLen is the number of scrollback lines. The alternate screen
// has none.
func (s *Screen) ScrollbackLen() int {
	if s.t.modes.AltScreen {
		return 0
	}
	return s.t.emu.ScrollbackLen()
}

// Lines is the number of scrollback lines plus screen rows.
func (s *Screen) Lines() int { return s.ScrollbackLen() + s.t.emu.Height() }

// Cursor returns the cursor position on the screen and whether the
// program wants it shown.
func (s *Screen) Cursor() (x, y int, visible bool) {
	p := s.t.emu.CursorPosition()
	return p.X, p.Y, !s.t.emu.CursorHidden()
}

// CursorStyle is the cursor shape the program asked for.
func (s *Screen) CursorStyle() (style vt.CursorStyle, blink bool) { return s.t.emu.CursorStyle() }

// Cell returns the cell at column x of line (see Screen). Cells past the
// end of a scrollback line are blank; Width is 0 for the cell after a
// wide character.
func (s *Screen) Cell(line, x int) Cell {
	var c *uv.Cell
	if sb := s.ScrollbackLen(); line < sb {
		c = s.t.emu.ScrollbackCellAt(x, line)
	} else {
		c = s.t.emu.CellAt(x, line-sb)
	}
	return convertCell(c)
}

// LineText returns the text of line from column x0 up to, not including,
// x1 (x1 < 0 means the end), with trailing blanks trimmed.
func (s *Screen) LineText(line, x0, x1 int) string {
	cols, _ := s.Size()
	if x1 < 0 || x1 > cols {
		x1 = cols
	}
	var b strings.Builder
	for x := max(x0, 0); x < x1; x++ {
		switch c := s.Cell(line, x); {
		case c.Width == 0: // the second half of a wide character
		case c.Content == "":
			b.WriteByte(' ')
		default:
			b.WriteString(c.Content)
		}
	}
	return strings.TrimRight(b.String(), " ")
}
