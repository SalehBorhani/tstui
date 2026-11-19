package ui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/auth"
)

// waitRun waits for Run to return and puts the result back for the
// harness cleanup.
func (h *appHarness) waitRun() error {
	h.t.Helper()
	select {
	case err := <-h.runErr:
		h.runErr <- err
		return err
	case <-time.After(5 * time.Second):
		h.t.Fatal("app did not stop after the panic")
		return nil
	}
}

// A panic stops the app the same way wherever it happens: Run returns a
// *PanicError, open sessions are killed, the screen is finalised, and a
// Secret in the panic value stays redacted.
func TestAppPanicCleansUp(t *testing.T) {
	secret := auth.NewSecret([]byte("Panic-Secret-77"))
	tests := []struct {
		name  string
		panic func(h *sessionHarness)
	}{
		{"background goroutine", func(h *sessionHarness) {
			h.app.goSafe(func() { panic(secret) })
		}},
		{"posted function", func(h *sessionHarness) {
			h.app.post(func() { panic(secret) })
		}},
		{"drawing", func(h *sessionHarness) {
			h.app.post(func() {
				boom := tview.NewBox()
				boom.SetDrawFunc(func(tcell.Screen, int, int, int, int) (int, int, int, int) { panic(secret) })
				h.app.pages.AddPage("boom", boom, true, true)
			})
		}},
		{"key handling", func(h *sessionHarness) {
			h.ui(func() {
				boom := tview.NewBox()
				boom.SetInputCapture(func(*tcell.EventKey) *tcell.EventKey { panic(secret) })
				h.app.pages.AddPage("boom", boom, true, true)
				h.app.dialog = boom // onKey hands every key to an open dialog
				h.app.tv.SetFocus(boom)
			})
			h.typeText("x")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newSessionHarness(t, []string{"ali"}, Options{})
			h.key(tcell.KeyEnter)
			h.waitSpawned(1)
			proc := h.sp.Proc(0)

			tt.panic(h)
			err := h.waitRun()
			var pe *PanicError
			if !errors.As(err, &pe) {
				t.Fatalf("Run returned %v, want a *PanicError", err)
			}
			if len(pe.Stack) == 0 {
				t.Error("no stack")
			}
			if strings.Contains(pe.Error(), "Panic-Secret-77") || !strings.Contains(pe.Error(), "[REDACTED]") {
				t.Errorf("panic message %q", pe.Error())
			}
			if !proc.Exited() {
				t.Error("the session's process outlived the panic")
			}
		})
	}
}
