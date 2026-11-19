package ui

import (
	"fmt"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/root-ali/tstui/internal/layout"
	"github.com/root-ali/tstui/internal/transfer"
	"github.com/root-ali/tstui/internal/tsh"
)

// tinySizes go from a usable terminal down to nothing.
var tinySizes = [][2]int{{80, 24}, {79, 23}, {60, 15}, {40, 10}, {20, 6}, {10, 3}, {5, 2}, {1, 1}, {0, 0}}

// drawAt resizes the screen and draws once; a panic fails the test (tview
// would re-panic it after restoring the screen).
func (h *sessionHarness) drawAt(t *testing.T, what string, w, hgt int) {
	t.Helper()
	var p any
	h.ui(func() {
		defer func() { p = recover() }()
		h.screen.SetSize(w, hgt)
		h.app.tv.ForceDraw()
	})
	if p != nil {
		t.Fatalf("%s at %dx%d: panic: %v", what, w, hgt, p)
	}
}

// Nothing panics on tiny terminals, in any of the app's states.
func TestAppTinyTerminal(t *testing.T) {
	twoSessions := func(h *sessionHarness) {
		h.key(tcell.KeyEnter)
		h.waitSpawned(1)
		h.ui(func() { h.app.setFocus(focusList); h.app.splitSelected(layout.Horizontal) })
		h.waitSpawned(2)
	}
	states := []struct {
		name  string
		enter func(h *sessionHarness)
	}{
		{"server list", func(*sessionHarness) {}},
		{"one session", func(h *sessionHarness) { h.key(tcell.KeyEnter); h.waitSpawned(1) }},
		{"split panes", twoSessions},
		{"fullscreen", func(h *sessionHarness) { twoSessions(h); h.ui(h.app.toggleFullscreen) }},
		{"palette", func(h *sessionHarness) { h.ui(func() { h.app.openPalette("ssh web") }) }},
		{"help", func(h *sessionHarness) { h.ui(h.app.openHelp) }},
		{"proxy picker", func(h *sessionHarness) { h.ui(h.app.openProxyPicker) }},
		{"session picker", func(h *sessionHarness) { twoSessions(h); h.ui(h.app.openSessionPicker) }},
		{"split picker", func(h *sessionHarness) { twoSessions(h); h.ui(func() { h.app.splitPicker(layout.Vertical) }) }},
		{"scp dialog", func(h *sessionHarness) { h.ui(func() { h.app.openSCP(transfer.Upload) }) }},
		{"port forwards", func(h *sessionHarness) { h.ui(h.app.openPF) }},
		{"new forward", func(h *sessionHarness) { h.ui(h.app.openForwardForm) }},
		{"login dialog", func(h *sessionHarness) {
			h.ui(func() { h.app.showLogin("Log in.") })
			h.eventually("login dialog", h.modalOpen)
		}},
	}
	for _, st := range states {
		t.Run(st.name, func(t *testing.T) {
			h := newSessionHarness(t, []string{"ali"}, Options{})
			st.enter(h)
			for _, sz := range tinySizes {
				h.drawAt(t, st.name, sz[0], sz[1])
			}
			h.drawAt(t, st.name, 140, 40)
		})
	}
}

// The header, list and tab bar render at every width without panicking,
// with long names that cannot fit.
func TestTinyHeaderAndList(t *testing.T) {
	th := DefaultTheme()
	hd := newHeader(th)
	l := newServerList(th)
	long := make([]tsh.Server, 30)
	for i := range long {
		long[i] = tsh.Server{Hostname: fmt.Sprintf("very-long-host-name-%02d.with.a.domain.example", i),
			Labels: map[string]string{"env": "production-environment"}}
	}
	l.setServers(long, time.Now())
	for _, sz := range tinySizes {
		s := tcell.NewSimulationScreen("UTF-8")
		if err := s.Init(); err != nil {
			t.Fatal(err)
		}
		s.SetSize(sz[0], sz[1])
		hd.view.SetRect(0, 0, sz[0], min(sz[1], headerHeight))
		l.table.SetRect(0, 0, sz[0], sz[1])
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("%dx%d: panic: %v", sz[0], sz[1], p)
				}
			}()
			hd.render(headerInfo{Version: "test", Proxy: "tp.a-very-long-proxy-name.example.com:443", User: "someone"}, time.Now())
			hd.view.Draw(s)
			l.table.Draw(s)
		}()
		s.Fini()
	}
}
