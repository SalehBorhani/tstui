package ui

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// The tstui logo is drawn from a small pixel font: '#' is a lit pixel.
// Two pixel rows make one terminal row, using the half blocks ▀ ▄ █, so
// an 8-pixel-high font takes 4 rows of the header. The block after the I
// is a terminal cursor.
var (
	// 8 pixels high: 4 terminal rows.
	bigT      = []string{"######", "######", "  ##  ", "  ##  ", "  ##  ", "  ##  ", "  ##  ", "  ##  "}
	bigS      = []string{" #####", "######", "##    ", "##### ", " #####", "    ##", "######", "##### "}
	bigU      = []string{"##  ##", "##  ##", "##  ##", "##  ##", "##  ##", "##  ##", "######", " #### "}
	bigI      = []string{"##", "##", "##", "##", "##", "##", "##", "##"}
	bigCursor = []string{"  ", "  ", "  ", "  ", "  ", "  ", "##", "##"}
	logoBig   = joinGlyphs(bigT, bigS, bigT, bigU, bigI, bigCursor)

	// 6 pixels high: 3 terminal rows.
	smallT      = []string{"###", " # ", " # ", " # ", " # ", " # "}
	smallS      = []string{"###", "#  ", "###", "  #", "  #", "###"}
	smallU      = []string{"# #", "# #", "# #", "# #", "# #", "###"}
	smallI      = []string{"#", "#", "#", "#", "#", "#"}
	smallCursor = []string{" ", " ", " ", " ", "#", "#"}
	logoSmall   = joinGlyphs(smallT, smallS, smallT, smallU, smallI, smallCursor)
)

// joinGlyphs sets glyphs of equal height side by side, one column apart.
func joinGlyphs(glyphs ...[]string) []string {
	out := make([]string, len(glyphs[0]))
	for i, g := range glyphs {
		for row := range out {
			if i > 0 {
				out[row] += " "
			}
			out[row] += g[row]
		}
	}
	return out
}

// logoTagline is shown under the logo when there is room.
const logoTagline = "teleport ssh tui"

// Logo sizes, chosen by the header's width.
type logoSize int

const (
	logoNone logoSize = iota
	logoCompact
	logoFull
)

// Header widths from which each logo size is shown; below them the
// header's text needs the room.
const (
	logoFullMinWidth    = 160
	logoCompactMinWidth = 120
)

func logoSizeFor(width int) logoSize {
	switch {
	case width >= logoFullMinWidth:
		return logoFull
	case width >= logoCompactMinWidth:
		return logoCompact
	}
	return logoNone
}

// halfBlocks turns pixel rows into terminal rows of ▀ ▄ █.
func halfBlocks(pixels []string) []string {
	width := 0
	for _, p := range pixels {
		width = max(width, len(p))
	}
	lit := func(row, col int) bool {
		return row < len(pixels) && col < len(pixels[row]) && pixels[row][col] == '#'
	}
	var out []string
	for row := 0; row < len(pixels); row += 2 {
		var b strings.Builder
		for col := range width {
			switch top, bottom := lit(row, col), lit(row+1, col); {
			case top && bottom:
				b.WriteRune('█')
			case top:
				b.WriteRune('▀')
			case bottom:
				b.WriteRune('▄')
			default:
				b.WriteRune(' ')
			}
		}
		out = append(out, b.String())
	}
	return out
}

// logoLines is the logo for size as plain text, without colours.
func logoLines(size logoSize) []string {
	switch size {
	case logoFull:
		return halfBlocks(logoBig)
	case logoCompact:
		return halfBlocks(logoSmall)
	}
	return nil
}

// logoWidth is the number of columns the logo of size takes.
func logoWidth(size logoSize) int {
	w := 0
	for _, l := range logoLines(size) {
		w = max(w, len([]rune(l)))
	}
	return w
}

// logoText is the logo of size as tview colour-tagged text: a horizontal
// gradient from the theme's LogoFrom to LogoTo, then the tagline.
func logoText(size logoSize, theme Theme) string {
	lines := logoLines(size)
	if len(lines) == 0 {
		return ""
	}
	width := logoWidth(size)
	var b strings.Builder
	for _, line := range lines {
		last := ""
		for col, r := range []rune(line) {
			if r != ' ' {
				if c := tag(gradient(theme.LogoFrom, theme.LogoTo, col, width)); c != last {
					fmt.Fprintf(&b, "[%s]", c)
					last = c
				}
			}
			b.WriteRune(r)
		}
		b.WriteString("[-]\n")
	}
	tagline := logoTagline
	if len(tagline) > width {
		tagline = "tstui"
	}
	pad := (width - len(tagline)) / 2
	fmt.Fprintf(&b, "[%s]%s%s[-]", tag(theme.DialogPlaceholder), strings.Repeat(" ", pad), tagline)
	return b.String()
}

// gradient is the colour at col of width, between from and to. Colours
// without an RGB value (the terminal's default) are used as they are.
func gradient(from, to tcell.Color, col, width int) tcell.Color {
	r1, g1, b1 := from.RGB()
	r2, g2, b2 := to.RGB()
	if r1 < 0 || r2 < 0 || width <= 1 {
		return from
	}
	col = min(max(col, 0), width-1)
	mix := func(a, b int32) int32 {
		// Between two 0-255 channel values, so it fits an int32.
		return int32(int(a) + (int(b)-int(a))*col/(width-1)) //nolint:gosec // bounded by a and b
	}
	return tcell.NewRGBColor(mix(r1, r2), mix(g1, g2), mix(b1, b2))
}
