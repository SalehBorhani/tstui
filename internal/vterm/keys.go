package vterm

import (
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// EncodeKey returns the bytes an xterm-compatible terminal sends for ev,
// honouring the program's cursor key mode, or nil for a key that has no
// encoding.
//
// Alt sends ESC before the key (xterm's metaSendsEscape). For cursor,
// editing and function keys the modifiers go into the xterm parameter
// instead: Ctrl+Up is CSI 1;5A.
func EncodeKey(ev *tcell.EventKey, m Modes) []byte {
	mods := ev.Modifiers()
	alt := mods&tcell.ModAlt != 0
	k := ev.Key()

	switch k {
	case tcell.KeyRune:
		r := ev.Rune()
		if mods&tcell.ModCtrl != 0 {
			if b, ok := ctrlByte(r); ok {
				return meta(alt, b)
			}
		}
		return meta(alt, []byte(string(r))...)
	case tcell.KeyEnter:
		return meta(alt, '\r')
	case tcell.KeyTab:
		return meta(alt, '\t')
	case tcell.KeyBacktab:
		return []byte("\x1b[Z")
	case tcell.KeyBackspace:
		// tcell reports both DEL and BS as Backspace; send DEL, as xterm
		// does for the Backspace key. Ctrl+Backspace sends BS.
		if mods&tcell.ModCtrl != 0 {
			return meta(alt, 0x08)
		}
		return meta(alt, 0x7f)
	case tcell.KeyEsc:
		return meta(alt, 0x1b)
	}

	if k >= tcell.KeyCtrlSpace && k <= tcell.KeyCtrlUnderscore {
		return meta(alt, byte(k-tcell.KeyCtrlSpace))
	}
	if k >= 0 && k < 0x20 {
		return meta(alt, byte(k))
	}
	if k == 0x7f {
		return meta(alt, 0x7f)
	}

	param := modParam(mods)
	if final, ok := cursorKeys[k]; ok {
		switch {
		case param > 1:
			return []byte("\x1b[1;" + strconv.Itoa(param) + string(final))
		case m.AppCursor:
			return []byte{0x1b, 'O', final}
		default:
			return []byte{0x1b, '[', final}
		}
	}
	if n, ok := tildeKeys[k]; ok {
		return tilde(n, param)
	}
	if k >= tcell.KeyF1 && k <= tcell.KeyF64 {
		return functionKey(int(k-tcell.KeyF1), mods)
	}
	return nil
}

// meta prefixes b with ESC when alt is set.
func meta(alt bool, b ...byte) []byte {
	if alt {
		return append([]byte{0x1b}, b...)
	}
	return b
}

// modParam is xterm's modifier parameter: 1 + shift + 2*alt + 4*ctrl.
func modParam(mods tcell.ModMask) int {
	p := 1
	if mods&tcell.ModShift != 0 {
		p++
	}
	if mods&tcell.ModAlt != 0 {
		p += 2
	}
	if mods&tcell.ModCtrl != 0 {
		p += 4
	}
	return p
}

// ctrlByte is the control code for Ctrl plus a printable key.
func ctrlByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r - 'a' + 1), true
	case r >= 'A' && r <= 'Z':
		return byte(r - 'A' + 1), true
	}
	switch r {
	case '@', ' ', '2':
		return 0x00, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6', '~':
		return 0x1e, true
	case '_', '7', '/':
		return 0x1f, true
	case '?', '8':
		return 0x7f, true
	}
	return 0, false
}

var cursorKeys = map[tcell.Key]byte{
	tcell.KeyUp: 'A', tcell.KeyDown: 'B', tcell.KeyRight: 'C', tcell.KeyLeft: 'D',
	tcell.KeyHome: 'H', tcell.KeyEnd: 'F',
}

var tildeKeys = map[tcell.Key]int{
	tcell.KeyInsert: 2, tcell.KeyDelete: 3, tcell.KeyPgUp: 5, tcell.KeyPgDn: 6,
}

func tilde(n, param int) []byte {
	s := "\x1b[" + strconv.Itoa(n)
	if param > 1 {
		s += ";" + strconv.Itoa(param)
	}
	return []byte(s + "~")
}

// functionKey encodes F1..F64. tcell reports Shift+F1 as F13, Ctrl+F1 as
// F25 and so on (xterm's terminfo), so F13 and up fold back into F1..F12
// with a modifier.
func functionKey(n int, mods tcell.ModMask) []byte {
	switch n / 12 {
	case 1:
		mods |= tcell.ModShift
	case 2:
		mods |= tcell.ModCtrl
	case 3:
		mods |= tcell.ModCtrl | tcell.ModShift
	case 4:
		mods |= tcell.ModAlt
	case 5:
		mods |= tcell.ModAlt | tcell.ModShift
	}
	n %= 12
	param := modParam(mods)
	if n < 4 { // F1-F4 are SS3 P..S
		final := byte('P' + n)
		if param > 1 {
			return []byte("\x1b[1;" + strconv.Itoa(param) + string(final))
		}
		return []byte{0x1b, 'O', final}
	}
	codes := [...]int{15, 17, 18, 19, 20, 21, 23, 24} // F5..F12
	return tilde(codes[n-4], param)
}

// Bracketed paste markers.
const (
	PasteStart = "\x1b[200~"
	PasteEnd   = "\x1b[201~"
)

// EncodePaste returns pasted text as a terminal sends it: newlines become
// carriage returns, as when typed, and with bracketed paste on the text is
// wrapped in the paste markers. Markers inside the text are removed so a
// paste cannot end the bracket early and type commands.
func EncodePaste(text string, m Modes) []byte {
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")
	if !m.BracketedPaste {
		return []byte(text)
	}
	text = strings.ReplaceAll(text, PasteEnd, "")
	text = strings.ReplaceAll(text, PasteStart, "")
	return []byte(PasteStart + text + PasteEnd)
}

// MouseAction says what happened to the mouse.
type MouseAction int

// Mouse actions.
const (
	MousePress MouseAction = iota
	MouseRelease
	MouseMotion
)

// MouseButton is an X11 mouse button.
type MouseButton int

// Mouse buttons, numbered as in the xterm protocol.
const (
	MouseLeft      MouseButton = 0
	MouseMiddle    MouseButton = 1
	MouseRight     MouseButton = 2
	MouseNone      MouseButton = 3 // motion without a button
	MouseWheelUp   MouseButton = 64
	MouseWheelDown MouseButton = 65
)

// MouseEvent is a mouse event in terminal cell coordinates (0-based).
type MouseEvent struct {
	X, Y             int
	Button           MouseButton
	Action           MouseAction
	Shift, Alt, Ctrl bool
}

// EncodeMouse returns the mouse report for ev in the protocol the program
// asked for, or nil when it does not want this event.
func EncodeMouse(ev MouseEvent, m Modes) []byte {
	if !m.MouseReporting() || ev.X < 0 || ev.Y < 0 {
		return nil
	}
	wheel := ev.Button >= MouseWheelUp
	switch ev.Action {
	case MouseMotion:
		dragging := m.MouseButton && ev.Button != MouseNone
		if !m.MouseAny && !dragging {
			return nil
		}
	case MouseRelease:
		if x10Only(m) {
			return nil // X10 reports presses only
		}
		if wheel {
			return nil
		}
	}

	code := int(ev.Button)
	if ev.Action == MouseMotion {
		code += 32
	}
	if !x10Only(m) {
		if ev.Shift {
			code += 4
		}
		if ev.Alt {
			code += 8
		}
		if ev.Ctrl {
			code += 16
		}
	}

	if m.MouseSGR {
		final := byte('M')
		if ev.Action == MouseRelease {
			final = 'm'
		}
		return []byte("\x1b[<" + strconv.Itoa(code) + ";" + strconv.Itoa(ev.X+1) + ";" + strconv.Itoa(ev.Y+1) + string(final))
	}
	// Legacy encoding: a release is button 3, and coordinates past 222
	// cannot be sent.
	if ev.Action == MouseRelease {
		code = code&^3 | 3
	}
	b, x, y := 32+code, 33+ev.X, 33+ev.Y
	if b < 0 || b > 255 || x < 0 || x > 255 || y < 0 || y > 255 {
		return nil
	}
	return []byte{0x1b, '[', 'M', byte(b), byte(x), byte(y)}
}

// x10Only is the old X10 protocol: presses only, no modifiers.
func x10Only(m Modes) bool {
	return m.MouseX10 && !m.MouseNormal && !m.MouseButton && !m.MouseAny
}
