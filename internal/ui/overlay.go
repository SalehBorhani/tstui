package ui

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// toastLayer is the main UI with the current toast drawn over its top
// right corner, just under the header. The toast never takes focus, so the
// terminal keeps every key while one is shown.
type toastLayer struct {
	*tview.Flex
	box   *tview.TextView
	toast func() (text string, color tcell.Color, ok bool)
	top   func() int // rows above the toast (the header, unless fullscreen)
}

func newToastLayer(main *tview.Flex, toast func() (string, tcell.Color, bool), top func() int) *toastLayer {
	box := tview.NewTextView().SetDynamicColors(true).SetWrap(false)
	box.SetBorder(true).SetBorderPadding(0, 0, 1, 1)
	return &toastLayer{Flex: main, box: box, toast: toast, top: top}
}

// Draw implements tview.Primitive.
func (l *toastLayer) Draw(screen tcell.Screen) {
	l.Flex.Draw(screen)
	text, color, ok := l.toast()
	if !ok {
		return
	}
	x, y, w, _ := l.GetRect()
	width := min(tview.TaggedStringWidth(text)+4, w-2)
	if width < 10 {
		return
	}
	l.box.SetBorderColor(color).SetTitle(" Notice ").SetTitleColor(color)
	l.box.SetText(text)
	l.box.SetRect(x+w-width-1, y+l.top(), width, 3)
	// Paint the background so the list below does not show through.
	bx, by, bw, bh := l.box.GetRect()
	for row := by; row < by+bh; row++ {
		for col := bx; col < bx+bw; col++ {
			screen.SetContent(col, row, ' ', nil, tcell.StyleDefault.Background(l.box.GetBackgroundColor()))
		}
	}
	l.box.Draw(screen)
}

// viewLayout centres a larger view: pct percent of the screen in each
// direction, at least minW x minH (as far as the screen allows).
type viewLayout struct {
	*tview.Box
	inner      tview.Primitive
	bg         tcell.Color
	pct        int
	minW, minH int
}

func newViewLayout(inner tview.Primitive, bg tcell.Color, pct, minW, minH int) *viewLayout {
	return &viewLayout{Box: tview.NewBox(), inner: inner, bg: bg, pct: pct, minW: minW, minH: minH}
}

// Draw implements tview.Primitive.
func (l *viewLayout) Draw(screen tcell.Screen) {
	sw, sh := screen.Size()
	w := min(max(sw*l.pct/100, l.minW), sw)
	h := min(max(sh*l.pct/100, l.minH), sh)
	x, y := (sw-w)/2, (sh-h)/2
	l.SetRect(x, y, w, h)
	style := tcell.StyleDefault.Background(l.bg)
	for row := y; row < y+h; row++ {
		for col := x; col < x+w; col++ {
			screen.SetContent(col, row, ' ', nil, style)
		}
	}
	l.inner.SetRect(x, y, w, h)
	l.inner.Draw(screen)
}

// Focus implements tview.Primitive.
func (l *viewLayout) Focus(delegate func(p tview.Primitive)) { delegate(l.inner) }

// HasFocus implements tview.Primitive.
func (l *viewLayout) HasFocus() bool { return l.inner.HasFocus() }

// InputHandler implements tview.Primitive: tview hands keys down from the
// root, so the view's own handler must be reached through this one.
func (l *viewLayout) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return l.inner.InputHandler()
}

// PasteHandler implements tview.Primitive.
func (l *viewLayout) PasteHandler() func(string, func(tview.Primitive)) {
	return l.inner.PasteHandler()
}

// MouseHandler forwards to the view and swallows clicks outside it, so
// nothing behind the view reacts.
func (l *viewLayout) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	return func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		_, capture := l.inner.MouseHandler()(action, ev, setFocus)
		return true, capture
	}
}

// --- toasts ---

// toastFor is how long a passing toast stays.
const toastFor = 20 * time.Second

// showToast shows text until d has passed; d == 0 keeps it until
// dismissToast. kind lets a later event replace or drop its own toast.
func (a *App) showToast(kind, text string, color tcell.Color, d time.Duration) {
	a.toastKind, a.toastText, a.toastColor = kind, text, color
	a.toastUntil = time.Time{}
	if d > 0 {
		a.toastUntil = time.Now().Add(d)
	}
}

func (a *App) dismissToast(kind string) {
	if kind == "" || a.toastKind == kind {
		a.toastKind, a.toastText = "", ""
	}
}

// currentToast is what toastLayer draws; it runs on the UI goroutine.
func (a *App) currentToast() (string, tcell.Color, bool) {
	if a.toastText == "" || (!a.toastUntil.IsZero() && time.Now().After(a.toastUntil)) {
		return "", 0, false
	}
	return fmt.Sprintf("[%s::b]%s", tag(a.toastColor), tview.Escape(a.toastText)), a.toastColor, true
}
