package vterm

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/gdamore/tcell/v2"
)

// Cell is one screen cell, ready for tcell.
type Cell struct {
	// Content is one grapheme cluster; empty for a blank cell.
	Content string
	// Width is the number of columns the content takes: 2 for a wide
	// character, 0 for the column after one.
	Width int
	Style tcell.Style
}

// Runes splits Content into the main rune and combining runes, the way
// tcell.Screen.SetContent takes them. A blank cell is a space.
func (c Cell) Runes() (rune, []rune) {
	if c.Content == "" {
		return ' ', nil
	}
	rs := []rune(c.Content)
	return rs[0], rs[1:]
}

var blankCell = Cell{Content: " ", Width: 1, Style: tcell.StyleDefault}

func convertCell(c *uv.Cell) Cell {
	if c == nil {
		return blankCell
	}
	return Cell{Content: c.Content, Width: c.Width, Style: convertStyle(c.Style, c.Link)}
}

func convertStyle(s uv.Style, link uv.Link) tcell.Style {
	st := tcell.StyleDefault.Foreground(convertColor(s.Fg)).Background(convertColor(s.Bg))
	a := s.Attrs
	st = st.Bold(a&uv.AttrBold != 0).
		Dim(a&uv.AttrFaint != 0).
		Italic(a&uv.AttrItalic != 0).
		Blink(a&(uv.AttrBlink|uv.AttrRapidBlink) != 0).
		Reverse(a&uv.AttrReverse != 0).
		StrikeThrough(a&uv.AttrStrikethrough != 0)
	if a&uv.AttrConceal != 0 {
		st = st.Foreground(convertColor(s.Bg))
	}
	if s.Underline != uv.UnderlineNone {
		// uv and tcell number the underline styles the same way.
		params := []any{tcell.UnderlineStyle(s.Underline)}
		if s.UnderlineColor != nil {
			params = append(params, convertColor(s.UnderlineColor))
		}
		st = st.Underline(params...)
	}
	if link.URL != "" {
		st = st.Url(link.URL)
	}
	return st
}

// convertColor maps an emulator colour to tcell. Nil is the default
// colour, which leaves the host terminal's own colour in place.
func convertColor(c color.Color) tcell.Color {
	switch v := c.(type) {
	case nil:
		return tcell.ColorDefault
	case ansi.BasicColor:
		return tcell.PaletteColor(int(v))
	case ansi.IndexedColor:
		return tcell.PaletteColor(int(v))
	default:
		r, g, b, _ := c.RGBA()
		return tcell.NewRGBColor(int32(r>>8), int32(g>>8), int32(b>>8))
	}
}
