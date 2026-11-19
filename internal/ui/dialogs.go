package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// openDialog shows a dialog page and gives focus to focus. Only one
// dialog is open at a time; while it is, it gets every key (onKey).
func (a *App) openDialog(name string, page, focus tview.Primitive) {
	if a.dialog != nil {
		a.removePage(a.dialogName)
	}
	a.dialog, a.dialogName = page, name
	a.pages.AddPage(name, page, false, true)
	a.tv.SetFocus(focus)
	a.updateFooter()
}

// closeDialog closes the dialog called name, if it is the one open.
func (a *App) closeDialog(name string) {
	if a.dialogName != name {
		return
	}
	if name == "pf" {
		a.pfView = nil
	}
	a.removePage(name)
	a.dialog, a.dialogName = nil, ""
	a.setFocus(a.focus)
}

// canOpenDialog reports whether nothing else (login, quit, another
// dialog) is in the way.
func (a *App) canOpenDialog() bool { return a.modal == nil && a.dialog == nil }

// framed puts inner in a bordered box with a k9s-style title.
func (a *App) framed(title string, inner tview.Primitive) *tview.Flex {
	f := tview.NewFlex().SetDirection(tview.FlexRow).AddItem(inner, 0, 1, true)
	f.SetBorder(true).SetBorderColor(a.theme.BorderFocus).SetTitleColor(a.theme.Title).
		SetTitle(" "+title+" ").SetBorderPadding(0, 0, 1, 1).SetBackgroundColor(a.theme.Bg)
	return f
}

// newForm is a tview Form in the theme's colours.
func (a *App) newForm() *tview.Form {
	f := tview.NewForm().
		SetLabelColor(a.theme.DialogLabel).
		SetFieldTextColor(a.theme.DialogField).
		SetFieldBackgroundColor(tcell.ColorDefault).
		SetButtonStyle(tcell.StyleDefault.Foreground(a.theme.ButtonFg).Background(a.theme.ButtonBg)).
		SetButtonActivatedStyle(tcell.StyleDefault.Foreground(a.theme.ButtonFocusFg).Background(a.theme.ButtonFocusBg))
	f.SetFieldStyle(tcell.StyleDefault.Foreground(a.theme.DialogField).Background(tcell.ColorDefault).Underline(true))
	f.SetBackgroundColor(a.theme.Bg)
	f.SetItemPadding(0)
	f.SetBorderPadding(0, 0, 0, 0)
	return f
}

// newText is a non-wrapping, colour-tagged text view.
func (a *App) newText() *tview.TextView {
	t := tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetWordWrap(true).SetTextColor(a.theme.DialogFg)
	t.SetBackgroundColor(a.theme.Bg)
	return t
}

// prompt asks for one line of text. complete, if set, offers completions
// in a drop-down. done gets the text; Esc cancels and calls nothing.
func (a *App) prompt(name, title, label, initial string, complete func(string) []string, done func(string)) {
	if !a.canOpenDialog() {
		return
	}
	in := tview.NewInputField().SetLabel(label).SetText(initial).
		SetLabelColor(a.theme.DialogLabel).SetFieldTextColor(a.theme.DialogField).
		SetFieldBackgroundColor(tcell.ColorDefault)
	in.SetFieldStyle(tcell.StyleDefault.Foreground(a.theme.DialogField).Background(tcell.ColorDefault).Underline(true))
	in.SetBackgroundColor(a.theme.Bg)
	if complete != nil {
		in.SetAutocompleteFunc(complete)
	}
	errLine := a.newText()
	in.SetDoneFunc(func(k tcell.Key) {
		switch k {
		case tcell.KeyEscape:
			a.closeDialog(name)
		case tcell.KeyEnter:
			text := in.GetText()
			a.closeDialog(name)
			done(text)
		}
	})
	body := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(in, 1, 0, true).
		AddItem(errLine, 0, 1, false)
	errLine.SetText(fmt.Sprintf("[%s]enter ok · esc cancel", tag(a.theme.DialogPlaceholder)))
	a.openDialog(name, newModalLayout(a.framed(title, body), a.theme.Bg, 60, 5), in)
}

// pickFrom shows a list and calls pick with the chosen index.
func (a *App) pickFrom(name, title string, items []string, current int, pick func(int)) {
	if !a.canOpenDialog() || len(items) == 0 {
		return
	}
	list := tview.NewList().ShowSecondaryText(false).SetHighlightFullLine(true).
		SetMainTextColor(a.theme.DialogField).
		SetSelectedTextColor(a.theme.TableCursorFg).
		SetSelectedBackgroundColor(a.theme.TableCursorBg)
	list.SetBackgroundColor(a.theme.Bg)
	width := 30
	for _, it := range items {
		list.AddItem(it, "", 0, nil)
		width = max(width, tview.TaggedStringWidth(it)+6)
	}
	list.SetCurrentItem(current)
	list.SetSelectedFunc(func(i int, _, _ string, _ rune) {
		a.closeDialog(name)
		pick(i)
	})
	list.SetDoneFunc(func() { a.closeDialog(name) })
	height := min(len(items), 15) + 2
	a.openDialog(name, newModalLayout(a.framed(title, list), a.theme.Bg, width, height), list)
}

// removePage removes a dialog page. tview then focuses the main page,
// whose focus callback would make the server list the focus area; the
// focus area stays what it was, for setFocus(a.focus) to restore.
func (a *App) removePage(name string) {
	f := a.focus
	a.pages.RemovePage(name)
	a.focus = f
}
