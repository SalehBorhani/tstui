package vterm

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

type replies struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (r *replies) add(b []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf.Write(b)
}

func (r *replies) wait(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		got := r.buf.String()
		r.mu.Unlock()
		if strings.Contains(got, want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no reply %q", want)
}

func newTerm(t *testing.T, cols, rows, sb int) (*Terminal, *replies) {
	t.Helper()
	r := &replies{}
	term := New(Options{Cols: cols, Rows: rows, Scrollback: sb, Reply: r.add})
	t.Cleanup(term.Close)
	return term, r
}

func write(t *testing.T, term *Terminal, s string) {
	t.Helper()
	if _, err := term.Write([]byte(s)); err != nil {
		t.Fatal(err)
	}
}

// tsh v18 asks for the cursor position and background colour at start
// and waits five seconds for an answer (docs/tsh-notes.md).
func TestTerminalAnswersQueries(t *testing.T) {
	term, r := newTerm(t, 80, 24, 0)
	write(t, term, "abc\x1b[6n\x1b]11;?\x1b\\")
	r.wait(t, "\x1b[1;4R")
	r.wait(t, "\x1b]11;rgb:")
}

func TestTerminalModes(t *testing.T) {
	term, _ := newTerm(t, 80, 24, 0)
	write(t, term, "\x1b[?1h\x1b[?2004h\x1b[?1002h\x1b[?1006h\x1b[?1049h")
	m := term.Modes()
	if !m.AppCursor || !m.BracketedPaste || !m.MouseButton || !m.MouseSGR || !m.AltScreen || !m.MouseReporting() {
		t.Fatalf("modes not set: %+v", m)
	}
	write(t, term, "\x1b[?1l\x1b[?2004l\x1b[?1002l\x1b[?1049l")
	m = term.Modes()
	if m.AppCursor || m.BracketedPaste || m.MouseButton || m.AltScreen || m.MouseReporting() {
		t.Fatalf("modes not cleared: %+v", m)
	}
}

func TestTerminalWideAndCombining(t *testing.T) {
	term, _ := newTerm(t, 20, 3, 0)
	write(t, term, "日本éx")
	term.View(func(s *Screen) {
		want := []struct {
			content string
			width   int
		}{{"日", 2}, {"", 0}, {"本", 2}, {"", 0}, {"é", 1}, {"x", 1}}
		for x, w := range want {
			c := s.Cell(s.ScrollbackLen(), x)
			if c.Content != w.content || c.Width != w.width {
				t.Errorf("cell %d = %q/%d, want %q/%d", x, c.Content, c.Width, w.content, w.width)
			}
		}
	})
	if got := strings.Split(term.Text(), "\n")[0]; got != "日本éx" {
		t.Errorf("line text %q", got)
	}
}

func TestTerminalUTF8SplitAcrossWrites(t *testing.T) {
	term, _ := newTerm(t, 20, 3, 0)
	b := []byte("ok 日")
	write(t, term, string(b[:len(b)-1]))
	write(t, term, string(b[len(b)-1:]))
	if got := strings.Split(term.Text(), "\n")[0]; got != "ok 日" {
		t.Errorf("got %q", got)
	}
}

func TestTerminalColours(t *testing.T) {
	term, _ := newTerm(t, 20, 3, 0)
	write(t, term, "\x1b[31mA\x1b[38;5;196mB\x1b[38;2;0;0;5mC\x1b[1;4;48;2;10;20;30mD\x1b[0mE")
	term.View(func(s *Screen) {
		row := s.ScrollbackLen()
		fg := func(x int) tcell.Color { f, _, _ := s.Cell(row, x).Style.Decompose(); return f }
		if got := fg(0); got != tcell.PaletteColor(1) {
			t.Errorf("basic red = %v", got)
		}
		if got := fg(1); got != tcell.PaletteColor(196) {
			t.Errorf("indexed 196 = %v", got)
		}
		// Truecolor stays truecolor: vt10x would have made this palette 5.
		if got := fg(2); got != tcell.NewRGBColor(0, 0, 5) {
			t.Errorf("rgb(0,0,5) = %v", got)
		}
		_, bg, attrs := s.Cell(row, 3).Style.Decompose()
		if bg != tcell.NewRGBColor(10, 20, 30) || attrs&tcell.AttrBold == 0 {
			t.Errorf("bold on rgb bg: bg=%v attrs=%v", bg, attrs)
		}
		if s.Cell(row, 3).Style.GetUnderlineStyle() != tcell.UnderlineStyleSolid {
			t.Error("underline lost")
		}
		if f, b, a := s.Cell(row, 4).Style.Decompose(); f != tcell.ColorDefault || b != tcell.ColorDefault || a != 0 {
			t.Errorf("reset cell = %v %v %v", f, b, a)
		}
	})
}

// The scrollback is a ring in the patched emulator: check order once it
// has wrapped around, and that the screen is still right.
func TestTerminalScrollbackRing(t *testing.T) {
	term, _ := newTerm(t, 10, 3, 5)
	for i := range 20 {
		write(t, term, fmt.Sprintf("line %d\r\n", i))
	}
	term.View(func(s *Screen) {
		if s.ScrollbackLen() != 5 {
			t.Fatalf("scrollback len = %d, want 5", s.ScrollbackLen())
		}
		var got []string
		for i := range s.Lines() {
			got = append(got, s.LineText(i, 0, -1))
		}
		want := []string{"line 13", "line 14", "line 15", "line 16", "line 17", "line 18", "line 19", ""}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("lines = %q\nwant %q", got, want)
		}
	})
}

// Scroll regions (tmux panes, vim splits) still move only their lines,
// and scrolling with a coloured background fills new lines with it.
func TestTerminalScrollRegion(t *testing.T) {
	term, _ := newTerm(t, 10, 5, 0)
	write(t, term, "\x1b[2J\x1b[HTOP\x1b[5;1HBOTTOM\x1b[2;4r\x1b[4;1Ha\r\nb\r\nc\x1b[r")
	lines := strings.Split(term.Text(), "\n")
	want := []string{"TOP", "a", "b", "c", "BOTTOM"}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Fatalf("screen = %q, want %q", lines, want)
	}
	term.View(func(s *Screen) {
		if s.ScrollbackLen() != 0 {
			t.Errorf("a region scroll must not feed the scrollback, got %d", s.ScrollbackLen())
		}
	})

	write(t, term, "\x1b[2J\x1b[5;1H\x1b[44m\r\n\x1b[0m")
	term.View(func(s *Screen) {
		_, bg, _ := s.Cell(s.ScrollbackLen()+4, 0).Style.Decompose()
		if bg != tcell.PaletteColor(4) {
			t.Errorf("new line background = %v, want blue", bg)
		}
	})
}

func TestTerminalAltScreenHasNoScrollback(t *testing.T) {
	term, _ := newTerm(t, 10, 3, 0)
	for i := range 6 {
		write(t, term, fmt.Sprintf("%d\r\n", i))
	}
	write(t, term, "\x1b[?1049h\x1b[2J\x1b[Hvim")
	term.View(func(s *Screen) {
		if s.ScrollbackLen() != 0 || s.LineText(0, 0, -1) != "vim" {
			t.Errorf("alt screen: sb=%d line0=%q", s.ScrollbackLen(), s.LineText(0, 0, -1))
		}
	})
	write(t, term, "\x1b[?1049l")
	term.View(func(s *Screen) {
		if s.ScrollbackLen() == 0 {
			t.Error("main screen scrollback gone after leaving the alt screen")
		}
	})
}

func TestTerminalResize(t *testing.T) {
	term, _ := newTerm(t, 10, 3, 0)
	write(t, term, "hello")
	if !term.Resize(4, 2) {
		t.Fatal("resize not reported")
	}
	if term.Resize(4, 2) {
		t.Fatal("same size reported as a change")
	}
	if c, r := term.Size(); c != 4 || r != 2 {
		t.Fatalf("size = %dx%d", c, r)
	}
	if got := strings.Split(term.Text(), "\n")[0]; got != "hell" {
		t.Errorf("after shrink %q", got)
	}
}

// A query after Close must not block the writer.
func TestTerminalWriteAfterClose(t *testing.T) {
	term := New(Options{Cols: 10, Rows: 3})
	term.Close()
	done := make(chan struct{})
	go func() {
		_, _ = term.Write([]byte("\x1b[6n\x1b[c"))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Write blocked after Close")
	}
}

func BenchmarkTerminalScroll(b *testing.B) {
	term := New(Options{Cols: 120, Rows: 40})
	defer term.Close()
	data := []byte(strings.Repeat("y\r\n", 4096))
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		_, _ = term.Write(data)
	}
}
