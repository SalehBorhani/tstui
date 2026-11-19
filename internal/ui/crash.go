package ui

import (
	"fmt"
	"runtime/debug"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// Panics. A panic anywhere in tstui must not leave the user's terminal in
// raw mode on the alternate screen, nor leave tsh processes behind. So
// the app's goroutines start through goSafe, and everything tview runs on
// the UI goroutine (posted functions, the input capture, and the root's
// draw and input handlers) recovers too. A recovered panic is recorded
// and the app shuts down the normal way: sessions and port forwards are
// closed, secrets wiped, the screen finalised, and only then does Run
// return a *PanicError for the caller to print.
//
// tview's own recovery (it finalises the screen and re-panics) is the
// last resort, for a panic inside tview itself; see runEventLoop.

// PanicError is what Run returns after a panic, on the UI goroutine or in
// one of the app's goroutines. Value is the panic value; Secret values
// print as [REDACTED]. Stack holds addresses and names, never memory
// contents.
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }

// goSafe runs f in a new goroutine whose panics stop the app cleanly.
func (a *App) goSafe(f func()) {
	go func() {
		defer a.recoverPanic()
		f()
	}()
}

// recoverPanic, deferred, records a panic (the first one wins) and stops
// the app.
func (a *App) recoverPanic() {
	if p := recover(); p != nil {
		a.crash.CompareAndSwap(nil, &PanicError{Value: p, Stack: debug.Stack()})
		a.shutdown()
	}
}

// runEventLoop runs tview and reports whether a panic escaped into it.
// tview has then finalised the screen, and its event loop is gone.
func (a *App) runEventLoop() (panicked bool, err error) {
	defer func() {
		if p := recover(); p != nil {
			a.crash.CompareAndSwap(nil, &PanicError{Value: p, Stack: debug.Stack()})
			panicked = true
			a.tv.Stop() // releases tview's event poller
		}
	}()
	return false, a.tv.Run()
}

// waitTimeout calls wait and gives up on it after d.
func waitTimeout(wait func(), d time.Duration) {
	done := make(chan struct{})
	go func() {
		wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// safeRoot is the root primitive: it recovers panics in drawing and in
// input handling, which tview calls straight from its event loop.
type safeRoot struct {
	tview.Primitive
	a *App
}

func (r *safeRoot) Draw(screen tcell.Screen) {
	defer r.a.recoverPanic()
	r.Primitive.Draw(screen)
}

func (r *safeRoot) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	h := r.Primitive.InputHandler()
	if h == nil {
		return nil
	}
	return func(ev *tcell.EventKey, setFocus func(tview.Primitive)) {
		defer r.a.recoverPanic()
		h(ev, setFocus)
	}
}

func (r *safeRoot) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	h := r.Primitive.MouseHandler()
	if h == nil {
		return nil
	}
	return func(action tview.MouseAction, ev *tcell.EventMouse, setFocus func(tview.Primitive)) (consumed bool, capture tview.Primitive) {
		defer r.a.recoverPanic()
		return h(action, ev, setFocus)
	}
}

func (r *safeRoot) PasteHandler() func(string, func(tview.Primitive)) {
	h := r.Primitive.PasteHandler()
	if h == nil {
		return nil
	}
	return func(text string, setFocus func(tview.Primitive)) {
		defer r.a.recoverPanic()
		h(text, setFocus)
	}
}
