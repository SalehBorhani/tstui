package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/root-ali/tstui/internal/filter"
)

// Interactive search (/): the command line turns into a search bar and
// the server list narrows with every key, kubens/fzf style. The arrows
// move the list's cursor, Enter connects to the highlighted server, Tab
// keeps the filter and returns to the list, and Esc puts back the
// filter that was set before.

// openSearch opens the search bar with the current filter typed in.
func (a *App) openSearch() {
	if !a.canOpenDialog() {
		return
	}
	a.searchOpen = true
	a.searchPrev = a.list.filter
	a.searchErr = ""
	a.openPalette(a.list.filter.String())
}

// searchChanged applies the typed filter at once. A filter that does
// not parse yet (say "=web" mid-typing) leaves the last good one shown.
func (a *App) searchChanged(text string) {
	f, err := filter.Parse(text)
	if err != nil {
		a.searchErr = err.Error()
	} else {
		a.searchErr = ""
		a.list.setFilter(f)
	}
	a.paletteHint()
}

// closeSearch closes the bar; keep false restores the earlier filter.
func (a *App) closeSearch(keep bool) {
	if !keep {
		a.list.setFilter(a.searchPrev)
	}
	a.closePalette()
}

// searchKey handles the search bar's keys; the rest edit the text.
func (a *App) searchKey(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyEscape:
		a.closeSearch(false)
		return nil
	case tcell.KeyEnter:
		a.closeSearch(true)
		if _, ok := a.list.selected(); ok {
			if ev.Modifiers()&tcell.ModShift != 0 {
				a.connect(modeNewTab)
			} else {
				a.connect(modeReuse)
			}
		}
		return nil
	case tcell.KeyTab:
		a.closeSearch(true)
		return nil
	case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn:
		a.list.table.InputHandler()(ev, func(tview.Primitive) {})
		return nil
	case tcell.KeyCtrlP:
		a.list.table.InputHandler()(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), func(tview.Primitive) {})
		return nil
	case tcell.KeyCtrlN:
		a.list.table.InputHandler()(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), func(tview.Primitive) {})
		return nil
	case tcell.KeyCtrlU:
		a.cmdLine.SetText("")
		return nil
	}
	return ev
}

// searchHint is the footer while searching: the match count and keys.
func (a *App) searchHint() string {
	var b strings.Builder
	if a.searchErr != "" {
		fmt.Fprintf(&b, "[%s]%s  ", tag(a.theme.Err), tview.Escape(a.searchErr))
	} else if a.list.all != nil {
		fmt.Fprintf(&b, "[%s::b]%d/%d[-::-]  ", tag(a.theme.Counter), len(a.list.servers), len(a.list.viewServers()))
	}
	for _, h := range []hint{{"↑↓ ctrl-n/p", "Move"}, {"enter", "Connect"}, {"tab", "Keep filter"}, {"esc", "Cancel"}, {"ctrl-u", "Clear"}, {"key=value", "Label"}, {"!text", "Exclude"}} {
		fmt.Fprintf(&b, "[%s::b]<%s>[%s::-]%s ", tag(a.theme.MenuKey), h.key, tag(a.theme.MenuText), h.text)
	}
	return b.String()
}
