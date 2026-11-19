package vterm

import (
	"errors"
	"testing"

	"github.com/charmbracelet/x/vt"
)

// FuzzTerminalWrite feeds arbitrary program output to the terminal, then
// resizes and reads it the way a draw does. Nothing may panic: the
// output comes from remote hosts. Run: go test -fuzz=FuzzTerminalWrite ./internal/vterm
func FuzzTerminalWrite(f *testing.F) {
	for _, seed := range []string{
		"hello\r\n", "\x1b[31mred\x1b[0m", "\x1b[?1049h\x1b[2J\x1b[H", "\x1b[999;999H\x1b[6n",
		"\x1b]0;title\x07", "\x1b]11;?\x1b\\", "\x1b[?1000h\x1b[?1006h", "日本語\x1b[1;2r\x1bM",
		"\x1b[38;2;255;0;0m\x1b[48;5;200m", "\x1bP+q544e\x1b\\", "\x1b[3;1000000H\x1b[1000000@",
		"\x1b[?2026h\x1b[?2026l", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "\t\t\t\x1b[3g\x1b[100I",
	} {
		f.Add([]byte(seed), uint8(80), uint8(24))
	}
	f.Fuzz(func(t *testing.T, out []byte, cols, rows uint8) {
		term := New(Options{Cols: 40, Rows: 10, Scrollback: 50})
		defer term.Close()
		if _, err := term.Write(out); err != nil {
			t.Fatalf("Write: %v", err)
		}
		term.Resize(int(cols%200)+1, int(rows%100)+1)
		_, _ = term.Write(out)
		term.View(func(s *Screen) {
			for line := range s.Lines() {
				_ = s.LineText(line, 0, -1)
			}
			s.Cursor()
		})
		if err := term.Err(); err != nil {
			t.Fatalf("emulator broke: %v", err)
		}
	})
}

// A panic in the emulator breaks the terminal instead of the program.
func TestEmulatorPanicBreaksTerminal(t *testing.T) {
	term := New(Options{Cols: 20, Rows: 5})
	defer term.Close()
	term.emu.SetCallbacks(vt.Callbacks{Title: func(string) { panic("boom") }})
	_, err := term.Write([]byte("\x1b]0;x\x07"))
	if !errors.Is(err, ErrEmulator) || !errors.Is(term.Err(), ErrEmulator) {
		t.Fatalf("Write err = %v, Err = %v; want ErrEmulator", err, term.Err())
	}
	if _, err := term.Write([]byte("more")); !errors.Is(err, ErrEmulator) {
		t.Fatalf("Write after the panic: %v", err)
	}
	if term.Resize(30, 6) {
		t.Fatal("a broken terminal resized")
	}
	called := false
	term.View(func(*Screen) { called = true })
	if called {
		t.Fatal("View drew a broken terminal")
	}
}
