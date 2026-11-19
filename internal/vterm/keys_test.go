package vterm

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestEncodeKey(t *testing.T) {
	const (
		none  = tcell.ModNone
		shift = tcell.ModShift
		ctrl  = tcell.ModCtrl
		alt   = tcell.ModAlt
	)
	app := Modes{AppCursor: true}
	tests := []struct {
		name  string
		key   tcell.Key
		r     rune
		mod   tcell.ModMask
		modes Modes
		want  string
	}{
		{"letter", tcell.KeyRune, 'a', none, Modes{}, "a"},
		{"utf-8", tcell.KeyRune, 'λ', none, Modes{}, "λ"},
		{"wide rune", tcell.KeyRune, '日', none, Modes{}, "日"},
		{"alt letter", tcell.KeyRune, 'x', alt, Modes{}, "\x1bx"},
		{"ctrl-a", tcell.KeyCtrlA, 0, ctrl, Modes{}, "\x01"},
		{"ctrl-b", tcell.KeyCtrlB, 0, ctrl, Modes{}, "\x02"},
		{"ctrl-c", tcell.KeyCtrlC, 0, ctrl, Modes{}, "\x03"},
		{"ctrl-d", tcell.KeyCtrlD, 0, ctrl, Modes{}, "\x04"},
		{"ctrl-z", tcell.KeyCtrlZ, 0, ctrl, Modes{}, "\x1a"},
		{"ctrl-space", tcell.KeyCtrlSpace, 0, ctrl, Modes{}, "\x00"},
		{"ctrl-backslash", tcell.KeyCtrlBackslash, 0, ctrl, Modes{}, "\x1c"},
		{"ctrl-]", tcell.KeyCtrlRightSq, 0, ctrl, Modes{}, "\x1d"},
		{"ctrl-alt-a", tcell.KeyCtrlA, 0, ctrl | alt, Modes{}, "\x1b\x01"},
		{"ctrl rune /", tcell.KeyRune, '/', ctrl, Modes{}, "\x1f"},
		{"ctrl rune @", tcell.KeyRune, '@', ctrl, Modes{}, "\x00"},
		{"enter", tcell.KeyEnter, 0, none, Modes{}, "\r"},
		{"alt enter", tcell.KeyEnter, 0, alt, Modes{}, "\x1b\r"},
		{"tab", tcell.KeyTab, 0, none, Modes{}, "\t"},
		{"shift-tab", tcell.KeyBacktab, 0, none, Modes{}, "\x1b[Z"},
		{"backspace", tcell.KeyBackspace, 0, none, Modes{}, "\x7f"},
		{"ctrl-backspace", tcell.KeyBackspace, 0, ctrl, Modes{}, "\x08"},
		{"alt-backspace", tcell.KeyBackspace, 0, alt, Modes{}, "\x1b\x7f"},
		{"esc", tcell.KeyEsc, 0, none, Modes{}, "\x1b"},
		{"up", tcell.KeyUp, 0, none, Modes{}, "\x1b[A"},
		{"down app mode", tcell.KeyDown, 0, none, app, "\x1bOB"},
		{"right", tcell.KeyRight, 0, none, Modes{}, "\x1b[C"},
		{"left app mode", tcell.KeyLeft, 0, none, app, "\x1bOD"},
		{"ctrl-up", tcell.KeyUp, 0, ctrl, Modes{}, "\x1b[1;5A"},
		{"ctrl-up app mode keeps CSI", tcell.KeyUp, 0, ctrl, app, "\x1b[1;5A"},
		{"shift-right", tcell.KeyRight, 0, shift, Modes{}, "\x1b[1;2C"},
		{"alt-left", tcell.KeyLeft, 0, alt, Modes{}, "\x1b[1;3D"},
		{"ctrl-shift-left", tcell.KeyLeft, 0, ctrl | shift, Modes{}, "\x1b[1;6D"},
		{"home", tcell.KeyHome, 0, none, Modes{}, "\x1b[H"},
		{"end app mode", tcell.KeyEnd, 0, none, app, "\x1bOF"},
		{"insert", tcell.KeyInsert, 0, none, Modes{}, "\x1b[2~"},
		{"delete", tcell.KeyDelete, 0, none, Modes{}, "\x1b[3~"},
		{"ctrl-delete", tcell.KeyDelete, 0, ctrl, Modes{}, "\x1b[3;5~"},
		{"page up", tcell.KeyPgUp, 0, none, Modes{}, "\x1b[5~"},
		{"page down", tcell.KeyPgDn, 0, none, Modes{}, "\x1b[6~"},
		{"shift page up", tcell.KeyPgUp, 0, shift, Modes{}, "\x1b[5;2~"},
		{"F1", tcell.KeyF1, 0, none, Modes{}, "\x1bOP"},
		{"F4", tcell.KeyF4, 0, none, Modes{}, "\x1bOS"},
		{"ctrl-F1", tcell.KeyF1, 0, ctrl, Modes{}, "\x1b[1;5P"},
		{"F5", tcell.KeyF5, 0, none, Modes{}, "\x1b[15~"},
		{"F6", tcell.KeyF6, 0, none, Modes{}, "\x1b[17~"},
		{"F10", tcell.KeyF10, 0, none, Modes{}, "\x1b[21~"},
		{"F11", tcell.KeyF11, 0, none, Modes{}, "\x1b[23~"},
		{"F12", tcell.KeyF12, 0, none, Modes{}, "\x1b[24~"},
		{"F13 is shift-F1", tcell.KeyF13, 0, none, Modes{}, "\x1b[1;2P"},
		{"F17 is shift-F5", tcell.KeyF17, 0, none, Modes{}, "\x1b[15;2~"},
		{"F29 is ctrl-F5", tcell.KeyF29, 0, none, Modes{}, "\x1b[15;5~"},
		{"no encoding", tcell.KeyHelp, 0, none, Modes{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := tcell.NewEventKey(tt.key, tt.r, tt.mod)
			if got := string(EncodeKey(ev, tt.modes)); got != tt.want {
				t.Errorf("EncodeKey(%v) = %q, want %q", ev.Name(), got, tt.want)
			}
		})
	}
}

// Keys as tcell's input parser builds them from raw terminal bytes.
func TestEncodeKeyFromParsedInput(t *testing.T) {
	tests := []struct {
		name string
		ev   *tcell.EventKey
		want string
	}{
		// tcell turns 0x01..0x1a into KeyCtrlA.. with ModCtrl.
		{"raw ctrl-a", tcell.NewEventKey(tcell.KeyCtrlSpace+1, 0, tcell.ModCtrl), "\x01"},
		{"raw ctrl-l", tcell.NewEventKey(tcell.KeyCtrlSpace+12, 0, tcell.ModCtrl), "\x0c"},
		// A control character given as a rune is converted by NewEventKey.
		{"control rune", tcell.NewEventKey(tcell.KeyRune, 0x05, tcell.ModNone), "\x05"},
		{"DEL rune", tcell.NewEventKey(tcell.KeyRune, 0x7f, tcell.ModNone), "\x7f"},
		{"shift letter", tcell.NewEventKey(tcell.KeyRune, 'A', tcell.ModShift), "A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(EncodeKey(tt.ev, Modes{})); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodePaste(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		bracket bool
		want    string
	}{
		{"plain", "ls -l\n", false, "ls -l\r"},
		{"crlf", "a\r\nb", false, "a\rb"},
		{"bracketed", "echo hi\n", true, "\x1b[200~echo hi\r\x1b[201~"},
		{"marker injection removed", "x\x1b[201~rm -rf /\n", true, "\x1b[200~xrm -rf /\r\x1b[201~"},
		{"utf-8 kept", "日本\n", true, "\x1b[200~日本\r\x1b[201~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := string(EncodePaste(tt.text, Modes{BracketedPaste: tt.bracket}))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEncodeMouse(t *testing.T) {
	normal := Modes{MouseNormal: true}
	sgr := Modes{MouseNormal: true, MouseSGR: true}
	tests := []struct {
		name  string
		ev    MouseEvent
		modes Modes
		want  string
	}{
		{"off", MouseEvent{X: 1, Y: 1, Button: MouseLeft}, Modes{}, ""},
		{"legacy press", MouseEvent{X: 0, Y: 0, Button: MouseLeft}, normal, "\x1b[M !!"},
		{"legacy release", MouseEvent{X: 2, Y: 3, Button: MouseLeft, Action: MouseRelease}, normal, "\x1b[M##$"},
		{"legacy too far", MouseEvent{X: 300, Y: 0, Button: MouseLeft}, normal, ""},
		{"sgr press", MouseEvent{X: 9, Y: 4, Button: MouseRight}, sgr, "\x1b[<2;10;5M"},
		{"sgr release", MouseEvent{X: 9, Y: 4, Button: MouseRight, Action: MouseRelease}, sgr, "\x1b[<2;10;5m"},
		{"sgr wheel", MouseEvent{X: 0, Y: 0, Button: MouseWheelDown}, sgr, "\x1b[<65;1;1M"},
		{"sgr ctrl click", MouseEvent{X: 0, Y: 0, Button: MouseLeft, Ctrl: true}, sgr, "\x1b[<16;1;1M"},
		{"motion needs 1002", MouseEvent{X: 0, Y: 0, Button: MouseLeft, Action: MouseMotion}, sgr, ""},
		{"drag with 1002", MouseEvent{X: 1, Y: 0, Button: MouseLeft, Action: MouseMotion},
			Modes{MouseButton: true, MouseSGR: true}, "\x1b[<32;2;1M"},
		{"hover needs 1003", MouseEvent{X: 1, Y: 0, Button: MouseNone, Action: MouseMotion},
			Modes{MouseButton: true, MouseSGR: true}, ""},
		{"hover with 1003", MouseEvent{X: 1, Y: 0, Button: MouseNone, Action: MouseMotion},
			Modes{MouseAny: true, MouseSGR: true}, "\x1b[<35;2;1M"},
		{"x10 ignores release", MouseEvent{X: 0, Y: 0, Button: MouseLeft, Action: MouseRelease}, Modes{MouseX10: true}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(EncodeMouse(tt.ev, tt.modes)); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
